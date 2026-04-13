package lsp

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		name       string
		lines      []string
		wantStart  int
		wantEnd    int
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
