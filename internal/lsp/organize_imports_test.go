package lsp

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"github.com/fwrq41251/decaf/internal/uri"
	"google.golang.org/protobuf/proto"
)

// setupTestIndex creates a temp workspace with a Java file and SemanticDB data,
// then returns the file URI and index.
func setupTestIndex(t *testing.T, javaSource string, occs []*sdb.SymbolOccurrence, syms []*sdb.SymbolInformation) (string, *index.Index) {
	t.Helper()

	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src", "main", "java", "com", "example")
	os.MkdirAll(srcDir, 0755)

	javaPath := filepath.Join(srcDir, "App.java")
	os.WriteFile(javaPath, []byte(javaSource), 0644)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := index.NewIndex(logger, tmpDir)

	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)

	relURI := "src/main/java/com/example/App.java"
	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{
			{
				Uri:         relURI,
				Occurrences: occs,
				Symbols:     syms,
			},
		},
	}
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "App.java.semanticdb"), data, 0644)
	idx.Load()

	fileURI := uri.FromPath(javaPath)
	return fileURI, idx
}

func setOrganizeIndexField(t *testing.T, idx *index.Index, field string, value any) {
	t.Helper()
	v := reflect.ValueOf(idx).Elem().FieldByName(field)
	if !v.IsValid() {
		t.Fatalf("field %s not found", field)
	}
	switch typed := value.(type) {
	case map[string][]*index.Symbol:
		converted := make(map[string][]index.SymbolID, len(typed))
		for key, syms := range typed {
			ids := make([]index.SymbolID, len(syms))
			for i, sym := range syms {
				if sym == nil {
					continue
				}
				ids[i] = idx.AddSymbolForTest(*sym)
			}
			converted[key] = ids
		}
		value = converted
	}
	reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}

func TestOrganizeImports_RemoveUnused(t *testing.T) {
	source := `package com.example;

import java.util.List;
import java.util.Map;
import java.util.Set;

public class App {
    List<String> items;
}
`
	// Only "List" is used (referenced via java/util/List#).
	occs := []*sdb.SymbolOccurrence{
		{Symbol: "java/util/List#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 7, StartCharacter: 4, EndLine: 7, EndCharacter: 8}},
		{Symbol: "com/example/App#", Role: sdb.SymbolOccurrence_DEFINITION,
			Range: &sdb.Range{StartLine: 6, StartCharacter: 13, EndLine: 6, EndCharacter: 16}},
	}
	syms := []*sdb.SymbolInformation{
		{Symbol: "com/example/App#", DisplayName: "App", Kind: sdb.SymbolInformation_CLASS},
	}

	fileURI, idx := setupTestIndex(t, source, occs, syms)
	edit := organizeImports(fileURI, idx, "")

	if edit == nil {
		t.Fatal("expected a non-nil edit")
	}
	edits := edit.Changes[fileURI]
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}

	// Should contain only List, not Map or Set.
	text := edits[0].NewText
	if !contains(text, "import java.util.List;") {
		t.Errorf("should keep java.util.List, got:\n%s", text)
	}
	if contains(text, "java.util.Map") {
		t.Errorf("should remove java.util.Map, got:\n%s", text)
	}
	if contains(text, "java.util.Set") {
		t.Errorf("should remove java.util.Set, got:\n%s", text)
	}
}

func TestOrganizeImports_SortImports(t *testing.T) {
	source := `package com.example;

import org.apache.commons.Lang;
import java.util.List;
import javax.servlet.Servlet;

public class App {
    List<String> items;
    Servlet s;
    Lang l;
}
`
	occs := []*sdb.SymbolOccurrence{
		{Symbol: "java/util/List#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 7, StartCharacter: 4, EndLine: 7, EndCharacter: 8}},
		{Symbol: "javax/servlet/Servlet#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 8, StartCharacter: 4, EndLine: 8, EndCharacter: 11}},
		{Symbol: "org/apache/commons/Lang#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 9, StartCharacter: 4, EndLine: 9, EndCharacter: 8}},
	}
	syms := []*sdb.SymbolInformation{
		{Symbol: "com/example/App#", DisplayName: "App", Kind: sdb.SymbolInformation_CLASS},
	}

	fileURI, idx := setupTestIndex(t, source, occs, syms)
	edit := organizeImports(fileURI, idx, "")

	if edit == nil {
		t.Fatal("expected a non-nil edit")
	}
	edits := edit.Changes[fileURI]
	text := edits[0].NewText

	// java.* should come first, then javax.*, then org.* with blank separators.
	lines := splitNonEmpty(text)
	if len(lines) != 3 {
		t.Fatalf("expected 3 import lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "import java.util.List;" {
		t.Errorf("first import should be java.util.List, got %s", lines[0])
	}
	if lines[1] != "import javax.servlet.Servlet;" {
		t.Errorf("second import should be javax.servlet.Servlet, got %s", lines[1])
	}
	if lines[2] != "import org.apache.commons.Lang;" {
		t.Errorf("third import should be org.apache.commons.Lang, got %s", lines[2])
	}

	// Check that there are blank lines between groups.
	if !contains(text, "List;\n\nimport javax") {
		t.Errorf("expected blank line between java and javax groups, got:\n%s", text)
	}
	if !contains(text, "Servlet;\n\nimport org") {
		t.Errorf("expected blank line between javax and org groups, got:\n%s", text)
	}
}

func TestOrganizeImports_NoChanges(t *testing.T) {
	source := `package com.example;

import java.util.List;

public class App {
    List<String> items;
}
`
	occs := []*sdb.SymbolOccurrence{
		{Symbol: "java/util/List#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 5, StartCharacter: 4, EndLine: 5, EndCharacter: 8}},
	}

	fileURI, idx := setupTestIndex(t, source, occs, nil)
	edit := organizeImports(fileURI, idx, "")

	// Should still return an edit (sorted single import is idempotent).
	if edit == nil {
		return // acceptable — no changes needed
	}
	edits := edit.Changes[fileURI]
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if !contains(edits[0].NewText, "import java.util.List;") {
		t.Errorf("should keep java.util.List")
	}
}

func TestOrganizeImports_WildcardPreserved(t *testing.T) {
	source := `package com.example;

import java.util.*;

public class App {
    List<String> items;
}
`
	occs := []*sdb.SymbolOccurrence{
		{Symbol: "java/util/List#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 5, StartCharacter: 4, EndLine: 5, EndCharacter: 8}},
	}

	fileURI, idx := setupTestIndex(t, source, occs, nil)
	edit := organizeImports(fileURI, idx, "")

	if edit == nil {
		t.Fatal("expected a non-nil edit")
	}
	edits := edit.Changes[fileURI]
	if !contains(edits[0].NewText, "java.util.*") {
		t.Errorf("wildcard import should be preserved, got:\n%s", edits[0].NewText)
	}
}

func TestOrganizeImports_WildcardSuppressesSpecific(t *testing.T) {
	source := `package com.example;

import jp.smartcompany.saas.kintai.dao.*;
import jp.smartcompany.saas.kintai.dto.paidholidayautogrant.*;

public class App {
    MemberDao dao;
    ScheduleRecordDao srd;
    CalculateType ct;
    GrantCondition gc;
}
`
	// These symbols are from packages already covered by wildcard imports.
	occs := []*sdb.SymbolOccurrence{
		{Symbol: "jp/smartcompany/saas/kintai/dao/MemberDao#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 6, StartCharacter: 4, EndLine: 6, EndCharacter: 13}},
		{Symbol: "jp/smartcompany/saas/kintai/dao/ScheduleRecordDao#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 7, StartCharacter: 4, EndLine: 7, EndCharacter: 21}},
		{Symbol: "jp/smartcompany/saas/kintai/dto/paidholidayautogrant/CalculateType#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 8, StartCharacter: 4, EndLine: 8, EndCharacter: 17}},
		{Symbol: "jp/smartcompany/saas/kintai/dto/paidholidayautogrant/GrantCondition#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 9, StartCharacter: 4, EndLine: 9, EndCharacter: 18}},
		{Symbol: "com/example/App#", Role: sdb.SymbolOccurrence_DEFINITION,
			Range: &sdb.Range{StartLine: 5, StartCharacter: 13, EndLine: 5, EndCharacter: 16}},
	}
	syms := []*sdb.SymbolInformation{
		{Symbol: "com/example/App#", DisplayName: "App", Kind: sdb.SymbolInformation_CLASS},
		{Symbol: "jp/smartcompany/saas/kintai/dao/MemberDao#", DisplayName: "MemberDao", Kind: sdb.SymbolInformation_CLASS},
		{Symbol: "jp/smartcompany/saas/kintai/dao/ScheduleRecordDao#", DisplayName: "ScheduleRecordDao", Kind: sdb.SymbolInformation_CLASS},
		{Symbol: "jp/smartcompany/saas/kintai/dto/paidholidayautogrant/CalculateType#", DisplayName: "CalculateType", Kind: sdb.SymbolInformation_CLASS},
		{Symbol: "jp/smartcompany/saas/kintai/dto/paidholidayautogrant/GrantCondition#", DisplayName: "GrantCondition", Kind: sdb.SymbolInformation_CLASS},
	}

	fileURI, idx := setupTestIndex(t, source, occs, syms)
	edit := organizeImports(fileURI, idx, "")

	if edit == nil {
		t.Fatal("expected a non-nil edit")
	}
	edits := edit.Changes[fileURI]
	text := edits[0].NewText

	// Should keep only the two wildcard imports, no specific ones added.
	lines := splitNonEmpty(text)
	for _, line := range lines {
		if line != "import jp.smartcompany.saas.kintai.dao.*;" &&
			line != "import jp.smartcompany.saas.kintai.dto.paidholidayautogrant.*;" {
			t.Errorf("unexpected import line: %s", line)
		}
	}
	if len(lines) != 2 {
		t.Errorf("expected 2 import lines, got %d:\n%s", len(lines), text)
	}
}

func TestOrganizeImports_WildcardAndSpecificMixed(t *testing.T) {
	// Wildcard for dao.*, but dto.kintaiform.KintaiForm should stay (no wildcard).
	source := `package com.example;

import jp.smartcompany.saas.kintai.dao.*;
import jp.smartcompany.saas.kintai.dao.MemberDao;
import jp.smartcompany.saas.kintai.dto.kintaiform.KintaiForm;

public class App {
    MemberDao dao;
    KintaiForm form;
}
`
	occs := []*sdb.SymbolOccurrence{
		{Symbol: "jp/smartcompany/saas/kintai/dao/MemberDao#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 7, StartCharacter: 4, EndLine: 7, EndCharacter: 13}},
		{Symbol: "jp/smartcompany/saas/kintai/dto/kintaiform/KintaiForm#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 8, StartCharacter: 4, EndLine: 8, EndCharacter: 14}},
	}

	fileURI, idx := setupTestIndex(t, source, occs, nil)
	edit := organizeImports(fileURI, idx, "")

	if edit == nil {
		t.Fatal("expected a non-nil edit")
	}
	edits := edit.Changes[fileURI]
	text := edits[0].NewText

	lines := splitNonEmpty(text)
	// Should keep dao.* (wildcard), drop dao.MemberDao (redundant), keep dto.kintaiform.KintaiForm.
	if len(lines) != 2 {
		t.Errorf("expected 2 import lines, got %d:\n%s", len(lines), text)
	}
	if !containsStr(text, "jp.smartcompany.saas.kintai.dao.*") {
		t.Error("should keep wildcard import dao.*")
	}
	if containsStr(text, "dao.MemberDao") {
		t.Error("should drop MemberDao — covered by dao.*")
	}
	if !containsStr(text, "dto.kintaiform.KintaiForm") {
		t.Error("should keep KintaiForm — not covered by any wildcard")
	}
}

func TestOrganizeImports_DoesNotAddExtraBlankLineWhenStaticImportsExist(t *testing.T) {
	source := `package com.example;

import static java.util.Collections.emptyList;

public class App {
    Helper helper = emptyList();
}
`
	occs := []*sdb.SymbolOccurrence{
		{Symbol: "org/example/Helper#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 5, StartCharacter: 4, EndLine: 5, EndCharacter: 10}},
	}
	syms := []*sdb.SymbolInformation{
		{Symbol: "org/example/Helper#", DisplayName: "Helper", Kind: sdb.SymbolInformation_CLASS},
	}

	fileURI, idx := setupTestIndex(t, source, occs, syms)
	edit := organizeImports(fileURI, idx, "")

	if edit == nil {
		t.Fatal("expected a non-nil edit")
	}
	text := edit.Changes[fileURI][0].NewText
	if strings.HasPrefix(text, "\n") {
		t.Fatalf("organizeImports should not prepend an extra blank line when imports already exist, got:\n%q", text)
	}
	if !containsStr(text, "import static java.util.Collections.emptyList;\n\nimport org.example.Helper;\n") {
		t.Fatalf("expected static and regular import groups with a single separator, got:\n%s", text)
	}
}

func TestOrganizeImports_AddsMissingStaticMethodImport(t *testing.T) {
	source := `package com.example;

public class App {
    void test() {
        assertThat("x");
    }
}
`
	fileURI, idx := setupTestIndex(t, source, nil, nil)
	setOrganizeIndexField(t, idx, "memberBySimpleName", map[string][]*index.Symbol{
		"assertthat": {
			{
				Name:     "assertThat",
				Symbol:   "org/assertj/core/api/Assertions#assertThat().",
				Kind:     sdb.SymbolInformation_METHOD,
				IsStatic: true,
				Signature: &index.SignatureInfo{
					Label:     "assertThat(String actual)",
					HasParams: true,
					Params:    []index.ParamInfo{{Name: "actual", Type: "String", TypeSym: "java/lang/String#"}},
				},
			},
		},
	})

	edit := organizeImports(fileURI, idx, "")
	if edit == nil {
		t.Fatal("expected non-nil edit for missing static import")
	}
	text := edit.Changes[fileURI][0].NewText
	if !containsStr(text, "import static org.assertj.core.api.Assertions.assertThat;") {
		t.Fatalf("should add static method import, got:\n%s", text)
	}
}

func TestOrganizeImports_SkipsAmbiguousStaticMethodImport(t *testing.T) {
	source := `package com.example;

public class App {
    void test() {
        assertThat("x");
    }
}
`
	fileURI, idx := setupTestIndex(t, source, nil, nil)
	setOrganizeIndexField(t, idx, "memberBySimpleName", map[string][]*index.Symbol{
		"assertthat": {
			{
				Name:     "assertThat",
				Symbol:   "org/assertj/core/api/Assertions#assertThat().",
				Kind:     sdb.SymbolInformation_METHOD,
				IsStatic: true,
				Signature: &index.SignatureInfo{
					Label:     "assertThat(String actual)",
					HasParams: true,
					Params:    []index.ParamInfo{{Name: "actual", Type: "String", TypeSym: "java/lang/String#"}},
				},
			},
			{
				Name:     "assertThat",
				Symbol:   "org/example/TestDsl#assertThat().",
				Kind:     sdb.SymbolInformation_METHOD,
				IsStatic: true,
				Signature: &index.SignatureInfo{
					Label:     "assertThat(String actual)",
					HasParams: true,
					Params:    []index.ParamInfo{{Name: "actual", Type: "String", TypeSym: "java/lang/String#"}},
				},
			},
		},
	})

	edit := organizeImports(fileURI, idx, "")
	if edit != nil {
		text := edit.Changes[fileURI][0].NewText
		if containsStr(text, "import static ") {
			t.Fatalf("should skip ambiguous static import, got:\n%s", text)
		}
	}
}

func TestOrganizeImports_DisambiguatesStaticMethodImportByArgumentType(t *testing.T) {
	source := `package com.example;

public class App {
    void test() {
        assertThat("x");
    }
}
`
	fileURI, idx := setupTestIndex(t, source, nil, nil)
	setOrganizeIndexField(t, idx, "memberBySimpleName", map[string][]*index.Symbol{
		"assertthat": {
			{
				Name:     "assertThat",
				Symbol:   "org/assertj/core/api/Assertions#assertThat().",
				Kind:     sdb.SymbolInformation_METHOD,
				IsStatic: true,
				Signature: &index.SignatureInfo{
					Label:     "assertThat(String actual)",
					HasParams: true,
					Params:    []index.ParamInfo{{Name: "actual", Type: "String", TypeSym: "java/lang/String#"}},
				},
			},
			{
				Name:     "assertThat",
				Symbol:   "org/example/IntAssertions#assertThat().",
				Kind:     sdb.SymbolInformation_METHOD,
				IsStatic: true,
				Signature: &index.SignatureInfo{
					Label:     "assertThat(int actual)",
					HasParams: true,
					Params:    []index.ParamInfo{{Name: "actual", Type: "int"}},
				},
			},
		},
	})
	setOrganizeIndexField(t, idx, "typeBySimpleName", map[string][]*index.Symbol{
		"string": {
			{Name: "String", Symbol: "java/lang/String#", Kind: sdb.SymbolInformation_CLASS},
		},
	})

	edit := organizeImports(fileURI, idx, "")
	if edit == nil {
		t.Fatal("expected non-nil edit for type-disambiguated static import")
	}
	text := edit.Changes[fileURI][0].NewText
	if !containsStr(text, "import static org.assertj.core.api.Assertions.assertThat;") {
		t.Fatalf("should choose String overload owner, got:\n%s", text)
	}
	if containsStr(text, "import static org.example.IntAssertions.assertThat;") {
		t.Fatalf("should not choose int overload owner, got:\n%s", text)
	}
}

func TestOrganizeImports_AddsMissingStaticFieldImport(t *testing.T) {
	source := `package com.example;

public class App {
    String test() {
        return EMPTY;
    }
}
`
	fileURI, idx := setupTestIndex(t, source, nil, nil)
	setOrganizeIndexField(t, idx, "memberBySimpleName", map[string][]*index.Symbol{
		"empty": {
			{
				Name:     "EMPTY",
				Symbol:   "org/apache/commons/lang3/StringUtils#EMPTY.",
				Kind:     sdb.SymbolInformation_FIELD,
				IsStatic: true,
			},
		},
	})

	edit := organizeImports(fileURI, idx, "")
	if edit == nil {
		t.Fatal("expected non-nil edit for missing static field import")
	}
	text := edit.Changes[fileURI][0].NewText
	if !containsStr(text, "import static org.apache.commons.lang3.StringUtils.EMPTY;") {
		t.Fatalf("should add static field import, got:\n%s", text)
	}
}

func TestOrganizeImports_SkipsAmbiguousStaticFieldImport(t *testing.T) {
	source := `package com.example;

public class App {
    String test() {
        return EMPTY;
    }
}
`
	fileURI, idx := setupTestIndex(t, source, nil, nil)
	setOrganizeIndexField(t, idx, "memberBySimpleName", map[string][]*index.Symbol{
		"empty": {
			{
				Name:     "EMPTY",
				Symbol:   "org/apache/commons/lang3/StringUtils#EMPTY.",
				Kind:     sdb.SymbolInformation_FIELD,
				IsStatic: true,
			},
			{
				Name:     "EMPTY",
				Symbol:   "org/example/Constants#EMPTY.",
				Kind:     sdb.SymbolInformation_FIELD,
				IsStatic: true,
			},
		},
	})

	edit := organizeImports(fileURI, idx, "")
	if edit != nil {
		text := edit.Changes[fileURI][0].NewText
		if containsStr(text, "import static ") {
			t.Fatalf("should skip ambiguous static field import, got:\n%s", text)
		}
	}
}

func TestOrganizeImports_SkipsStaticFieldImportWhenLocalShadowsName(t *testing.T) {
	source := `package com.example;

public class App {
    String test() {
        String EMPTY = "";
        return EMPTY;
    }
}
`
	fileURI, idx := setupTestIndex(t, source, nil, nil)
	setOrganizeIndexField(t, idx, "memberBySimpleName", map[string][]*index.Symbol{
		"empty": {
			{
				Name:     "EMPTY",
				Symbol:   "org/apache/commons/lang3/StringUtils#EMPTY.",
				Kind:     sdb.SymbolInformation_FIELD,
				IsStatic: true,
			},
		},
	})

	edit := organizeImports(fileURI, idx, "")
	if edit != nil {
		text := edit.Changes[fileURI][0].NewText
		if containsStr(text, "import static org.apache.commons.lang3.StringUtils.EMPTY;") {
			t.Fatalf("should skip static field import when local variable shadows name, got:\n%s", text)
		}
	}
}

func TestOrganizeImports_DeduplicatesExistingImports(t *testing.T) {
	source := `package com.example;

import java.util.List;
import java.util.List;

public class App {
    List<String> items;
}
`
	occs := []*sdb.SymbolOccurrence{
		{Symbol: "java/util/List#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 6, StartCharacter: 4, EndLine: 6, EndCharacter: 8}},
	}

	fileURI, idx := setupTestIndex(t, source, occs, nil)
	edit := organizeImports(fileURI, idx, "")

	if edit == nil {
		t.Fatal("expected a non-nil edit")
	}
	lines := splitNonEmpty(edit.Changes[fileURI][0].NewText)
	if len(lines) != 1 || lines[0] != "import java.util.List;" {
		t.Fatalf("expected a single deduplicated import, got %v", lines)
	}
}

func TestOrganizeImports_EmptyFile(t *testing.T) {
	source := `package com.example;

public class App {}
`
	fileURI, idx := setupTestIndex(t, source, nil, nil)
	edit := organizeImports(fileURI, idx, "")

	if edit != nil {
		edits := edit.Changes[fileURI]
		if len(edits) == 1 && edits[0].NewText != "" {
			t.Errorf("expected no imports added for empty file, got:\n%s", edits[0].NewText)
		}
	}
}

func TestOrganizeImports_FallbackTreeSitter(t *testing.T) {
	// Simulate a file with no SemanticDB data (compile error) but with type references.
	source := `package com.example;

public class App {
    List<String> items;
    Map<String, String> map;
}
`
	// No occurrences — simulates compile failure (no .semanticdb generated).
	// But provide symbol definitions so the index knows about these types.
	syms := []*sdb.SymbolInformation{
		{Symbol: "com/example/App#", DisplayName: "App", Kind: sdb.SymbolInformation_CLASS},
		{Symbol: "java/util/List#", DisplayName: "List", Kind: sdb.SymbolInformation_CLASS},
		{Symbol: "java/util/Map#", DisplayName: "Map", Kind: sdb.SymbolInformation_CLASS},
	}

	fileURI, idx := setupTestIndex(t, source, nil, syms)
	edit := organizeImports(fileURI, idx, "")

	if edit == nil {
		t.Fatal("expected non-nil edit from tree-sitter fallback")
	}
	edits := edit.Changes[fileURI]
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	text := edits[0].NewText
	if !containsStr(text, "import java.util.List;") {
		t.Errorf("should add java.util.List import, got:\n%s", text)
	}
	if !containsStr(text, "import java.util.Map;") {
		t.Errorf("should add java.util.Map import, got:\n%s", text)
	}
}

func TestOrganizeImports_FallbackSkipsAmbiguous(t *testing.T) {
	// When multiple types match the same name, the fallback should skip it.
	source := `package com.example;

public class App {
    Request request;
}
`
	syms := []*sdb.SymbolInformation{
		{Symbol: "com/example/App#", DisplayName: "App", Kind: sdb.SymbolInformation_CLASS},
		{Symbol: "org/example/Request#", DisplayName: "Request", Kind: sdb.SymbolInformation_CLASS},
		{Symbol: "com/sun/net/httpserver/Request#", DisplayName: "Request", Kind: sdb.SymbolInformation_CLASS},
	}

	fileURI, idx := setupTestIndex(t, source, nil, syms)
	edit := organizeImports(fileURI, idx, "")

	// Should not add any import since "Request" is ambiguous and not JDK-preferred.
	if edit != nil {
		edits := edit.Changes[fileURI]
		if len(edits) == 1 && edits[0].NewText != "" {
			t.Errorf("should not add ambiguous import, got:\n%s", edits[0].NewText)
		}
	}
}

func TestOrganizeImports_FallbackSupplementsPartialSemanticDB(t *testing.T) {
	source := `package com.example;

public class App {
    List<String> items;
    Map<String, String> map;
}
`
	// Simulate partial SemanticDB output: the file itself is indexed, but type
	// references are missing because compilation did not fully succeed.
	occs := []*sdb.SymbolOccurrence{
		{Symbol: "com/example/App#", Role: sdb.SymbolOccurrence_DEFINITION,
			Range: &sdb.Range{StartLine: 2, StartCharacter: 13, EndLine: 2, EndCharacter: 16}},
	}
	syms := []*sdb.SymbolInformation{
		{Symbol: "com/example/App#", DisplayName: "App", Kind: sdb.SymbolInformation_CLASS},
		{Symbol: "java/util/List#", DisplayName: "List", Kind: sdb.SymbolInformation_CLASS},
		{Symbol: "java/util/Map#", DisplayName: "Map", Kind: sdb.SymbolInformation_CLASS},
	}

	fileURI, idx := setupTestIndex(t, source, occs, syms)
	edit := organizeImports(fileURI, idx, "")

	if edit == nil {
		t.Fatal("expected non-nil edit from supplemental tree-sitter fallback")
	}
	edits := edit.Changes[fileURI]
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	text := edits[0].NewText
	if !containsStr(text, "import java.util.List;") {
		t.Errorf("should add java.util.List import, got:\n%s", text)
	}
	if !containsStr(text, "import java.util.Map;") {
		t.Errorf("should add java.util.Map import, got:\n%s", text)
	}
}

func TestOrganizeImports_PrefersContextualPackageForAmbiguousType(t *testing.T) {
	source := `package com.example;

import java.util.Map;

public class App {
    private Map<String, List<String>> values;
}
`
	syms := []*sdb.SymbolInformation{
		{Symbol: "com/example/App#", DisplayName: "App", Kind: sdb.SymbolInformation_CLASS},
		{Symbol: "java/util/Map#", DisplayName: "Map", Kind: sdb.SymbolInformation_INTERFACE},
		{Symbol: "java/util/List#", DisplayName: "List", Kind: sdb.SymbolInformation_INTERFACE},
		{Symbol: "java/awt/List#", DisplayName: "List", Kind: sdb.SymbolInformation_CLASS},
	}

	fileURI, idx := setupTestIndex(t, source, nil, syms)
	edit := organizeImports(fileURI, idx, "")

	if edit == nil {
		t.Fatal("expected non-nil edit for contextual ambiguous import")
	}
	text := edit.Changes[fileURI][0].NewText
	if !containsStr(text, "import java.util.List;") {
		t.Fatalf("should add java.util.List based on java.util context, got:\n%s", text)
	}
	if containsStr(text, "java.awt.List") {
		t.Fatalf("should not add java.awt.List, got:\n%s", text)
	}
}

func TestOrganizeImports_RemovesConflictingImportWhenExactMatchKnown(t *testing.T) {
	source := `package com.example;

import com.sun.net.httpserver.Request;
import org.winry.RequestHandler;
import org.winry.model.Request;

public class LRangeHandler implements RequestHandler {

    @Override
    public String handleRequest(Request request) {
        return null;
    }
}
`
	occs := []*sdb.SymbolOccurrence{
		{Symbol: "org/winry/RequestHandler#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 5, StartCharacter: 38, EndLine: 5, EndCharacter: 52}},
		{Symbol: "org/winry/model/Request#", Role: sdb.SymbolOccurrence_REFERENCE,
			Range: &sdb.Range{StartLine: 8, StartCharacter: 32, EndLine: 8, EndCharacter: 39}},
	}
	syms := []*sdb.SymbolInformation{
		{Symbol: "com/example/LRangeHandler#", DisplayName: "LRangeHandler", Kind: sdb.SymbolInformation_CLASS},
		{Symbol: "org/winry/RequestHandler#", DisplayName: "RequestHandler", Kind: sdb.SymbolInformation_INTERFACE},
		{Symbol: "org/winry/model/Request#", DisplayName: "Request", Kind: sdb.SymbolInformation_CLASS},
		{Symbol: "com/sun/net/httpserver/Request#", DisplayName: "Request", Kind: sdb.SymbolInformation_CLASS},
	}

	fileURI, idx := setupTestIndex(t, source, occs, syms)
	edit := organizeImports(fileURI, idx, "")

	if edit == nil {
		t.Fatal("expected non-nil edit for conflicting imports")
	}
	text := edit.Changes[fileURI][0].NewText
	if containsStr(text, "import com.sun.net.httpserver.Request;") {
		t.Fatalf("should remove unrelated Request import, got:\n%s", text)
	}
	if !containsStr(text, "import org.winry.model.Request;") {
		t.Fatalf("should keep exact Request import, got:\n%s", text)
	}
}

func TestOrganizeImports_PrefersWorkspaceAndJDKTypesForAmbiguousImports(t *testing.T) {
	source := `package com.example;

import java.util.Map;
import org.winry.RequestHandler;

public class LRangeHandler implements RequestHandler {

    private Map<String, List<String>> listStore;

    public LRangeHandler(Map<String, List<String>> listStore) {
        this.listStore = listStore;
    }

    @Override
    public String handleRequest(Request request) {
        return null;
    }
}
`
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src", "main", "java", "com", "example")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("mkdir src dir: %v", err)
	}
	javaPath := filepath.Join(srcDir, "LRangeHandler.java")
	if err := os.WriteFile(javaPath, []byte(source), 0644); err != nil {
		t.Fatalf("write java source: %v", err)
	}

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := index.NewIndex(logger, tmpDir)

	setOrganizeIndexField(t, idx, "typeBySimpleName", map[string][]*index.Symbol{
		"requesthandler": {
			{Name: "RequestHandler", Symbol: "org/winry/RequestHandler#", Kind: sdb.SymbolInformation_INTERFACE, URI: "src/main/java/org/winry/RequestHandler.java"},
		},
		"request": {
			{Name: "Request", Symbol: "org/winry/model/Request#", Kind: sdb.SymbolInformation_CLASS, URI: "src/main/java/org/winry/model/Request.java"},
			{Name: "Request", Symbol: "com/sun/net/httpserver/Request#", Kind: sdb.SymbolInformation_CLASS},
		},
		"string": {
			{Name: "String", Symbol: "java/lang/String#", Kind: sdb.SymbolInformation_CLASS},
			{Name: "String", Symbol: "com/sun/org/apache/xpath/internal/operations/String#", Kind: sdb.SymbolInformation_CLASS},
		},
		"map": {
			{Name: "Map", Symbol: "java/util/Map#", Kind: sdb.SymbolInformation_INTERFACE},
		},
		"list": {
			{Name: "List", Symbol: "java/util/List#", Kind: sdb.SymbolInformation_INTERFACE},
			{Name: "List", Symbol: "java/awt/List#", Kind: sdb.SymbolInformation_CLASS},
		},
	})
	setOrganizeIndexField(t, idx, "definitions", map[string][]*index.Symbol{
		"org/winry/RequestHandler#": {
			{Name: "RequestHandler", Symbol: "org/winry/RequestHandler#", Kind: sdb.SymbolInformation_INTERFACE, URI: "src/main/java/org/winry/RequestHandler.java"},
		},
		"org/winry/model/Request#": {
			{Name: "Request", Symbol: "org/winry/model/Request#", Kind: sdb.SymbolInformation_CLASS, URI: "src/main/java/org/winry/model/Request.java"},
		},
		"com/sun/net/httpserver/Request#": {
			{Name: "Request", Symbol: "com/sun/net/httpserver/Request#", Kind: sdb.SymbolInformation_CLASS},
		},
		"java/lang/String#": {
			{Name: "String", Symbol: "java/lang/String#", Kind: sdb.SymbolInformation_CLASS},
		},
		"com/sun/org/apache/xpath/internal/operations/String#": {
			{Name: "String", Symbol: "com/sun/org/apache/xpath/internal/operations/String#", Kind: sdb.SymbolInformation_CLASS},
		},
		"java/util/Map#": {
			{Name: "Map", Symbol: "java/util/Map#", Kind: sdb.SymbolInformation_INTERFACE},
		},
		"java/util/List#": {
			{Name: "List", Symbol: "java/util/List#", Kind: sdb.SymbolInformation_INTERFACE},
		},
		"java/awt/List#": {
			{Name: "List", Symbol: "java/awt/List#", Kind: sdb.SymbolInformation_CLASS},
		},
	})

	fileURI := uri.FromPath(javaPath)
	edit := organizeImports(fileURI, idx, "")

	if edit == nil {
		t.Fatal("expected non-nil edit for ambiguous import resolution")
	}
	text := edit.Changes[fileURI][0].NewText
	if containsStr(text, "com.sun.net.httpserver.Request") {
		t.Fatalf("should not import external Request when workspace type exists, got:\n%s", text)
	}
	if !containsStr(text, "import org.winry.model.Request;") {
		t.Fatalf("should import workspace Request, got:\n%s", text)
	}
	if containsStr(text, "com.sun.org.apache.xpath.internal.operations.String") {
		t.Fatalf("should not import non-java.lang String, got:\n%s", text)
	}
	if containsStr(text, "import java.lang.String;") {
		t.Fatalf("should not import java.lang.String, got:\n%s", text)
	}
	if !containsStr(text, "import java.util.List;") {
		t.Fatalf("should import java.util.List from JDK preference, got:\n%s", text)
	}
}

func TestOrganizeImports_UsesOverrideSignatureTypes(t *testing.T) {
	source := `package com.example;

import java.util.Map;
import org.winry.RequestHandler;

public class LRangeHandler implements RequestHandler {

    private Map<String, List<String>> listStore;

    @Override
    public String handleRequest(Request request) {
        return null;
    }
}
`
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src", "main", "java", "com", "example")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("mkdir src dir: %v", err)
	}
	javaPath := filepath.Join(srcDir, "LRangeHandler.java")
	if err := os.WriteFile(javaPath, []byte(source), 0644); err != nil {
		t.Fatalf("write java source: %v", err)
	}

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := index.NewIndex(logger, tmpDir)

	relURI := "src/main/java/com/example/LRangeHandler.java"
	setOrganizeIndexField(t, idx, "fileSymbols", map[string][]*index.Symbol{
		relURI: {
			{Name: "LRangeHandler", Symbol: "com/example/LRangeHandler#", Kind: sdb.SymbolInformation_CLASS, URI: relURI},
		},
	})
	setOrganizeIndexField(t, idx, "definitions", map[string][]*index.Symbol{
		"com/example/LRangeHandler#": {
			{Name: "LRangeHandler", Symbol: "com/example/LRangeHandler#", Kind: sdb.SymbolInformation_CLASS, URI: relURI},
		},
		"org/winry/RequestHandler#": {
			{Name: "RequestHandler", Symbol: "org/winry/RequestHandler#", Kind: sdb.SymbolInformation_INTERFACE},
		},
		"org/winry/RequestHandler#handleRequest().": {
			{Name: "handleRequest", Symbol: "org/winry/RequestHandler#handleRequest().", Kind: sdb.SymbolInformation_METHOD, Signature: &index.SignatureInfo{
				Label:         "String handleRequest(Request request)",
				ReturnTypeSym: "java/lang/String#",
				HasParams:     true,
				Params:        []index.ParamInfo{{Name: "request", Type: "Request", TypeSym: "org/winry/model/Request#"}},
			}},
		},
		"org/winry/model/Request#": {
			{Name: "Request", Symbol: "org/winry/model/Request#", Kind: sdb.SymbolInformation_CLASS},
		},
		"com/sun/net/httpserver/Request#": {
			{Name: "Request", Symbol: "com/sun/net/httpserver/Request#", Kind: sdb.SymbolInformation_CLASS},
		},
		"java/lang/String#": {
			{Name: "String", Symbol: "java/lang/String#", Kind: sdb.SymbolInformation_CLASS},
		},
		"com/sun/org/apache/xpath/internal/operations/String#": {
			{Name: "String", Symbol: "com/sun/org/apache/xpath/internal/operations/String#", Kind: sdb.SymbolInformation_CLASS},
		},
		"java/util/Map#": {
			{Name: "Map", Symbol: "java/util/Map#", Kind: sdb.SymbolInformation_INTERFACE},
		},
		"java/util/List#": {
			{Name: "List", Symbol: "java/util/List#", Kind: sdb.SymbolInformation_INTERFACE},
		},
		"java/awt/List#": {
			{Name: "List", Symbol: "java/awt/List#", Kind: sdb.SymbolInformation_CLASS},
		},
	})
	setOrganizeIndexField(t, idx, "typeBySimpleName", map[string][]*index.Symbol{
		"requesthandler": {
			{Name: "RequestHandler", Symbol: "org/winry/RequestHandler#", Kind: sdb.SymbolInformation_INTERFACE},
		},
		"request": {
			{Name: "Request", Symbol: "org/winry/model/Request#", Kind: sdb.SymbolInformation_CLASS},
			{Name: "Request", Symbol: "com/sun/net/httpserver/Request#", Kind: sdb.SymbolInformation_CLASS},
		},
		"string": {
			{Name: "String", Symbol: "java/lang/String#", Kind: sdb.SymbolInformation_CLASS},
			{Name: "String", Symbol: "com/sun/org/apache/xpath/internal/operations/String#", Kind: sdb.SymbolInformation_CLASS},
		},
		"map": {
			{Name: "Map", Symbol: "java/util/Map#", Kind: sdb.SymbolInformation_INTERFACE},
		},
		"list": {
			{Name: "List", Symbol: "java/util/List#", Kind: sdb.SymbolInformation_INTERFACE},
			{Name: "List", Symbol: "java/awt/List#", Kind: sdb.SymbolInformation_CLASS},
		},
	})
	setOrganizeIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"org/winry/RequestHandler#": {
			{Name: "handleRequest", Symbol: "org/winry/RequestHandler#handleRequest().", Kind: sdb.SymbolInformation_METHOD, Signature: &index.SignatureInfo{
				Label:         "String handleRequest(Request request)",
				ReturnTypeSym: "java/lang/String#",
				HasParams:     true,
				Params:        []index.ParamInfo{{Name: "request", Type: "Request", TypeSym: "org/winry/model/Request#"}},
			}},
		},
	})
	setOrganizeIndexField(t, idx, "parentTypes", map[string][]*index.TypeExpr{
		"com/example/LRangeHandler#": {{Sym: "org/winry/RequestHandler#"}},
	})
	setOrganizeIndexField(t, idx, "childToParents", map[string][]string{
		"com/example/LRangeHandler#": {"org/winry/RequestHandler#"},
	})
	setOrganizeIndexField(t, idx, "symbolDeclType", map[string]*index.TypeExpr{
		"org/winry/RequestHandler#handleRequest().": {Sym: "java/lang/String#"},
	})
	setOrganizeIndexField(t, idx, "symbolDeclParamTypes", map[string][]*index.TypeExpr{
		"org/winry/RequestHandler#handleRequest().": {{Sym: "org/winry/model/Request#"}},
	})

	fileURI := uri.FromPath(javaPath)
	edit := organizeImports(fileURI, idx, "")

	if edit == nil {
		t.Fatal("expected non-nil edit for override signature import resolution")
	}
	text := edit.Changes[fileURI][0].NewText
	if !containsStr(text, "import org.winry.model.Request;") {
		t.Fatalf("should import Request from override signature, got:\n%s", text)
	}
	if containsStr(text, "com.sun.net.httpserver.Request") {
		t.Fatalf("should not import conflicting Request when override signature is known, got:\n%s", text)
	}
	if containsStr(text, "com.sun.org.apache.xpath.internal.operations.String") || containsStr(text, "import java.lang.String;") {
		t.Fatalf("should not import String when override return type resolves to java.lang.String, got:\n%s", text)
	}
	if !containsStr(text, "import java.util.List;") {
		t.Fatalf("should still import java.util.List, got:\n%s", text)
	}
}

func TestComputeImportEdit(t *testing.T) {
	source := []byte(`package com.example;

import java.util.List;
import java.util.*;

public class App {}
`)

	imports := []ImportSpec{
		{Path: "java.util.List"},
		{Path: "java.util.*", Wildcard: true},
	}
	pkg := "com.example"

	// Basic case: class not yet imported should produce an edit.
	edit := computeImportEdit(source, imports, pkg, "org.apache.commons.Lang")
	if edit == nil {
		t.Fatal("expected non-nil edit for org.apache.commons.Lang")
	}
	if !containsStr(edit.NewText, "import org.apache.commons.Lang;") {
		t.Errorf("edit text should contain import statement, got: %s", edit.NewText)
	}

	// Already-imported case: returns nil.
	edit = computeImportEdit(source, imports, pkg, "java.util.List")
	if edit != nil {
		t.Error("expected nil edit for already-imported java.util.List")
	}

	// java.lang case: returns nil (no import needed).
	edit = computeImportEdit(source, imports, pkg, "java.lang.String")
	if edit != nil {
		t.Error("expected nil edit for java.lang.String")
	}

	// Same-package case: returns nil.
	edit = computeImportEdit(source, imports, pkg, "com.example.Other")
	if edit != nil {
		t.Error("expected nil edit for same-package class")
	}

	// Wildcard-imported case: returns nil.
	edit = computeImportEdit(source, imports, pkg, "java.util.Map")
	if edit != nil {
		t.Error("expected nil edit for wildcard-covered java.util.Map")
	}
}

func TestParseImportBlock(t *testing.T) {
	tests := []struct {
		name        string
		lines       []string
		wantStart   int
		wantEnd     int
		wantImports int
	}{
		{
			name:        "standard imports",
			lines:       []string{"package com.example;", "", "import java.util.List;", "import java.util.Map;", "", "public class App {}"},
			wantStart:   2,
			wantEnd:     4,
			wantImports: 2,
		},
		{
			name:        "imports with blank line in between",
			lines:       []string{"package com.example;", "", "import java.util.List;", "", "import java.util.Map;", "", "public class App {}"},
			wantStart:   2,
			wantEnd:     5,
			wantImports: 2,
		},
		{
			name:        "no imports",
			lines:       []string{"package com.example;", "", "public class App {}"},
			wantStart:   1,
			wantEnd:     1,
			wantImports: 0,
		},
		{
			name:        "no package no imports",
			lines:       []string{"public class App {}"},
			wantStart:   0,
			wantEnd:     0,
			wantImports: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := []byte(strings.Join(tt.lines, "\n"))
			tree, _ := getTree(content)
			block := parseImportBlock(tree.RootNode(), content)
			if block.startLine != tt.wantStart {
				t.Errorf("startLine = %d, want %d", block.startLine, tt.wantStart)
			}
			if block.endLine != tt.wantEnd {
				t.Errorf("endLine = %d, want %d", block.endLine, tt.wantEnd)
			}
			if len(block.imports) != tt.wantImports {
				t.Errorf("len(imports) = %d, want %d", len(block.imports), tt.wantImports)
			}
		})
	}
}

func TestFqnFromSymbol(t *testing.T) {
	tests := []struct {
		sym  string
		want string
	}{
		{"java/util/List#", "java.util.List"},
		{"com/example/Outer#Inner#", "com.example.Outer.Inner"},
		{"com/example/Outer#Middle#Inner#", "com.example.Outer.Middle.Inner"},
		{"com/example/Foo#bar().", ""},
		{"com/example/Foo#", "com.example.Foo"},
		{"int#", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := fqnFromSymbol(tt.sym)
		if got != tt.want {
			t.Errorf("fqnFromSymbol(%q) = %q, want %q", tt.sym, got, tt.want)
		}
	}
}

func TestSimpleNameFromSymbol(t *testing.T) {
	tests := []struct {
		sym  string
		want string
	}{
		{"java/util/List#", "List"},
		{"com/example/Foo#bar().", "Foo"},
		{"com/example/Outer#Inner#", "Inner"},
		{"com/example/Outer#Inner#member.", "Inner"},
		{"String#", "String"},
		{"", ""},
	}
	for _, tt := range tests {
		got := simpleNameFromSymbol(tt.sym)
		if got != tt.want {
			t.Errorf("simpleNameFromSymbol(%q) = %q, want %q", tt.sym, got, tt.want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func splitNonEmpty(s string) []string {
	var result []string
	for _, line := range splitLines(s) {
		trimmed := trimSpace(line)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
