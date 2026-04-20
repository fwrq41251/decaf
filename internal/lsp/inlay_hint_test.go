package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unsafe"

	"github.com/fwrq41251/decaf/internal/index"
	"github.com/fwrq41251/decaf/internal/jsonrpc"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"google.golang.org/protobuf/proto"
)

// newTestHandler creates a Handler with a ready index for testing.
func newTestHandler(t *testing.T) (*Handler, *index.Index, string) {
	t.Helper()
	tmpDir := t.TempDir()
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	h := NewHandler(logger, jsonrpc.NewTransport(&bytes.Buffer{}, &bytes.Buffer{}))
	idx := index.NewIndex(logger, tmpDir)
	h.markIndexReadyForTest()
	h.setIndexForTest(idx)
	h.rootURI = "file://" + tmpDir
	return h, idx, tmpDir
}

func loadSDB(t *testing.T, tmpDir string, idx *index.Index, docs *sdb.TextDocuments) {
	t.Helper()
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)
	writeSourcePlaceholdersForDocs(t, tmpDir, docs)
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "test.semanticdb"), data, 0644)
	idx.Load()
}

func setOwnerMemberSignature(t *testing.T, idx *index.Index, owner, memberName, label string, hasParams bool) {
	t.Helper()

	v := reflect.ValueOf(idx).Elem().FieldByName("ownerMembers")
	membersValue := reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
	members := membersValue.Interface().(map[string][]index.SymbolID)

	for _, id := range members[owner] {
		member := idx.SymbolForTest(id)
		if member.Name == memberName {
			member.Signature = &index.SignatureInfo{Label: label, HasParams: hasParams}
			return
		}
	}

	t.Fatalf("member %s not found on owner %s", memberName, owner)
}

func TestInlayHint_VarTypeHint(t *testing.T) {
	h, idx, tmpDir := newTestHandler(t)
	defer idx.Close()

	// Index ArrayList class so type resolution works.
	loadSDB(t, tmpDir, idx, &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/ArrayList.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "java/util/ArrayList#", DisplayName: "ArrayList", Kind: sdb.SymbolInformation_CLASS},
			},
		}},
	})

	fileURI := "file://" + tmpDir + "/src/Test.java"
	content := `package java.util;
import java.util.ArrayList;
public class Test {
    void test() {
        var list = new ArrayList<String>();
        var x = 42;
        String name = "hello";
    }
}`
	h.docs.Open(fileURI, content)

	params := InlayHintParams{
		TextDocument: TextDocumentIdentifier{URI: fileURI},
		Range: Range{
			Start: Position{Line: 0, Character: 0},
			End:   Position{Line: 10, Character: 0},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleInlayHint(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleInlayHint failed: %v", err)
	}

	hints := got.([]InlayHint)

	// We expect hints for "var list" (ArrayList) and "var x" (int).
	// "String name" is not a var declaration, so no hint.
	var typeHints []InlayHint
	for _, hint := range hints {
		if hint.Kind == InlayHintKindType {
			typeHints = append(typeHints, hint)
		}
	}

	if len(typeHints) < 1 {
		t.Fatalf("expected at least 1 type hint, got %d", len(typeHints))
	}

	// Check the "var x = 42" hint → should show ": int"
	found := false
	for _, hint := range typeHints {
		if hint.Label == ": int" {
			found = true
			if hint.Line() != 5 {
				t.Errorf("int hint at wrong line: got %d, want 5", hint.Line())
			}
			break
		}
	}
	if !found {
		t.Errorf("expected a ': int' type hint for 'var x = 42', got hints: %v", typeHints)
	}
}

func TestInlayHint_ParameterNameHints(t *testing.T) {
	h, idx, tmpDir := newTestHandler(t)
	defer idx.Close()

	// Index Foo class with a method bar(int count, String name).
	loadSDB(t, tmpDir, idx, &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Foo.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "com/example/Foo#", DisplayName: "Foo", Kind: sdb.SymbolInformation_CLASS},
				{
					Symbol: "com/example/Foo#bar().", DisplayName: "bar", Kind: sdb.SymbolInformation_METHOD,
					Signature: &sdb.Signature{
						SealedValue: &sdb.Signature_MethodSignature{
							MethodSignature: &sdb.MethodSignature{
								ReturnType: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "scala/Unit#"}}},
								ParameterLists: []*sdb.Scope{{
									Hardlinks: []*sdb.SymbolInformation{
										{DisplayName: "count", Signature: &sdb.Signature{SealedValue: &sdb.Signature_ValueSignature{ValueSignature: &sdb.ValueSignature{Tpe: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "scala/Int#"}}}}}}},
										{DisplayName: "name", Signature: &sdb.Signature{SealedValue: &sdb.Signature_ValueSignature{ValueSignature: &sdb.ValueSignature{Tpe: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "java/lang/String#"}}}}}}},
									},
								}},
							},
						},
					},
				},
			},
		}},
	})

	fileURI := "file://" + tmpDir + "/src/Caller.java"
	content := `package com.example;
public class Caller {
    void test() {
        Foo foo = new Foo();
        foo.bar(1, "hello");
    }
}`
	h.docs.Open(fileURI, content)

	params := InlayHintParams{
		TextDocument: TextDocumentIdentifier{URI: fileURI},
		Range: Range{
			Start: Position{Line: 0, Character: 0},
			End:   Position{Line: 10, Character: 0},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleInlayHint(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleInlayHint failed: %v", err)
	}

	hints := got.([]InlayHint)

	var paramHints []InlayHint
	for _, hint := range hints {
		if hint.Kind == InlayHintKindParameter {
			paramHints = append(paramHints, hint)
		}
	}

	if len(paramHints) != 2 {
		t.Fatalf("expected 2 parameter hints, got %d: %v", len(paramHints), paramHints)
	}

	if paramHints[0].Label != "count:" {
		t.Errorf("first param hint = %q, want %q", paramHints[0].Label, "count:")
	}
	if paramHints[1].Label != "name:" {
		t.Errorf("second param hint = %q, want %q", paramHints[1].Label, "name:")
	}
}

func TestInlayHint_SkipMatchingArgName(t *testing.T) {
	h, idx, tmpDir := newTestHandler(t)
	defer idx.Close()

	loadSDB(t, tmpDir, idx, &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Foo.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "com/example/Foo#", DisplayName: "Foo", Kind: sdb.SymbolInformation_CLASS},
				{
					Symbol: "com/example/Foo#set().", DisplayName: "set", Kind: sdb.SymbolInformation_METHOD,
					Signature: &sdb.Signature{
						SealedValue: &sdb.Signature_MethodSignature{
							MethodSignature: &sdb.MethodSignature{
								ReturnType: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "scala/Unit#"}}},
								ParameterLists: []*sdb.Scope{{
									Hardlinks: []*sdb.SymbolInformation{
										{DisplayName: "name", Signature: &sdb.Signature{SealedValue: &sdb.Signature_ValueSignature{ValueSignature: &sdb.ValueSignature{Tpe: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "java/lang/String#"}}}}}}},
									},
								}},
							},
						},
					},
				},
			},
		}},
	})

	fileURI := "file://" + tmpDir + "/src/Caller.java"
	// Argument "name" matches parameter name "name" — should be skipped.
	content := `package com.example;
public class Caller {
    void test() {
        Foo foo = new Foo();
        String name = "Alice";
        foo.set(name);
    }
}`
	h.docs.Open(fileURI, content)

	params := InlayHintParams{
		TextDocument: TextDocumentIdentifier{URI: fileURI},
		Range: Range{
			Start: Position{Line: 0, Character: 0},
			End:   Position{Line: 10, Character: 0},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleInlayHint(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleInlayHint failed: %v", err)
	}

	hints := got.([]InlayHint)
	for _, hint := range hints {
		if hint.Kind == InlayHintKindParameter {
			t.Errorf("expected no parameter hint when arg matches param name, got %q", hint.Label)
		}
	}
}

func TestInlayHint_EmptyRange(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()

	fileURI := "file:///tmp/Empty.java"
	h.docs.Open(fileURI, "")

	params := InlayHintParams{
		TextDocument: TextDocumentIdentifier{URI: fileURI},
		Range: Range{
			Start: Position{Line: 0, Character: 0},
			End:   Position{Line: 0, Character: 0},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleInlayHint(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleInlayHint failed: %v", err)
	}

	hints := got.([]InlayHint)
	if len(hints) != 0 {
		t.Errorf("expected 0 hints for empty file, got %d", len(hints))
	}
}

func TestInlayHint_RespectsCharacterOffsets(t *testing.T) {
	h, idx, tmpDir := newTestHandler(t)
	defer idx.Close()

	loadSDB(t, tmpDir, idx, &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Foo.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "com/example/Foo#", DisplayName: "Foo", Kind: sdb.SymbolInformation_CLASS},
				{
					Symbol: "com/example/Foo#bar().", DisplayName: "bar", Kind: sdb.SymbolInformation_METHOD,
					Signature: &sdb.Signature{
						SealedValue: &sdb.Signature_MethodSignature{
							MethodSignature: &sdb.MethodSignature{
								ReturnType: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "scala/Unit#"}}},
								ParameterLists: []*sdb.Scope{{
									Hardlinks: []*sdb.SymbolInformation{
										{DisplayName: "count", Signature: &sdb.Signature{SealedValue: &sdb.Signature_ValueSignature{ValueSignature: &sdb.ValueSignature{Tpe: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "scala/Int#"}}}}}}},
										{DisplayName: "name", Signature: &sdb.Signature{SealedValue: &sdb.Signature_ValueSignature{ValueSignature: &sdb.ValueSignature{Tpe: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "java/lang/String#"}}}}}}},
									},
								}},
							},
						},
					},
				},
			},
		}},
	})

	fileURI := "file://" + tmpDir + "/src/Caller.java"
	content := `package com.example;
public class Caller {
    void test() { Foo foo = new Foo(); foo.bar(1, "hello"); }
}`
	h.docs.Open(fileURI, content)

	params := InlayHintParams{
		TextDocument: TextDocumentIdentifier{URI: fileURI},
		Range: Range{
			Start: Position{Line: 2, Character: 39},
			End:   Position{Line: 2, Character: 57},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleInlayHint(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleInlayHint failed: %v", err)
	}

	hints := got.([]InlayHint)
	if len(hints) != 2 {
		t.Fatalf("expected 2 parameter hints inside same-line range, got %d: %v", len(hints), hints)
	}
	for _, hint := range hints {
		if hint.Position.Line != 2 {
			t.Fatalf("hint returned on wrong line: %v", hint)
		}
		if hint.Position.Character < params.Range.Start.Character || hint.Position.Character > params.Range.End.Character {
			t.Fatalf("hint returned outside requested range: %v", hint)
		}
	}
}

func TestInlayHint_VarargsParameterHints(t *testing.T) {
	h, idx, tmpDir := newTestHandler(t)
	defer idx.Close()

	loadSDB(t, tmpDir, idx, &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Foo.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "com/example/Foo#", DisplayName: "Foo", Kind: sdb.SymbolInformation_CLASS},
				{
					Symbol: "com/example/Foo#log().", DisplayName: "log", Kind: sdb.SymbolInformation_METHOD,
					Signature: &sdb.Signature{
						SealedValue: &sdb.Signature_MethodSignature{
							MethodSignature: &sdb.MethodSignature{
								ReturnType: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "scala/Unit#"}}},
								ParameterLists: []*sdb.Scope{{
									Hardlinks: []*sdb.SymbolInformation{
										{DisplayName: "prefix", Signature: &sdb.Signature{SealedValue: &sdb.Signature_ValueSignature{ValueSignature: &sdb.ValueSignature{Tpe: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "java/lang/String#"}}}}}}},
										{DisplayName: "args", Signature: &sdb.Signature{SealedValue: &sdb.Signature_ValueSignature{ValueSignature: &sdb.ValueSignature{Tpe: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "java/lang/String#"}}}}}}},
									},
								}},
							},
						},
					},
				},
			},
		}},
	})
	setOwnerMemberSignature(t, idx, "com/example/Foo#", "log", "void log(String prefix, String... args)", true)

	fileURI := "file://" + tmpDir + "/src/Caller.java"
	content := `package com.example;
public class Caller {
    void test() {
        Foo foo = new Foo();
        foo.log("tag", "a", "b");
    }
}`
	h.docs.Open(fileURI, content)

	params := InlayHintParams{
		TextDocument: TextDocumentIdentifier{URI: fileURI},
		Range: Range{
			Start: Position{Line: 0, Character: 0},
			End:   Position{Line: 10, Character: 0},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleInlayHint(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleInlayHint failed: %v", err)
	}

	var paramHints []InlayHint
	for _, hint := range got.([]InlayHint) {
		if hint.Kind == InlayHintKindParameter {
			paramHints = append(paramHints, hint)
		}
	}

	if len(paramHints) != 2 {
		t.Fatalf("expected 2 parameter hints for varargs call, got %d: %v", len(paramHints), paramHints)
	}
	if paramHints[0].Label != "prefix:" {
		t.Errorf("first param hint = %q, want %q", paramHints[0].Label, "prefix:")
	}
	if paramHints[1].Label != "args:" {
		t.Errorf("second param hint = %q, want %q", paramHints[1].Label, "args:")
	}

	sym := h.findMethodByArgCount("com/example/Foo#", "log", 3)
	if sym == nil {
		t.Fatal("expected varargs fallback method match, got nil")
	}
}

func TestExtractParamNames(t *testing.T) {
	tests := []struct {
		label string
		want  []string
	}{
		{"void add(String name, int x)", []string{"name", "x"}},
		{"void doWork(Map<String, Integer> map, int count)", []string{"map", "count"}},
		{"int size()", nil},
		{"void set(String... args)", []string{"args"}},
	}
	for _, tt := range tests {
		sig := &index.SignatureInfo{Label: tt.label, HasParams: true}
		if tt.want == nil {
			sig.HasParams = false
		}
		got := extractParamNames(sig)
		if len(got) != len(tt.want) {
			t.Errorf("extractParamNames(%q) = %v, want %v", tt.label, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("extractParamNames(%q)[%d] = %q, want %q", tt.label, i, got[i], tt.want[i])
			}
		}
	}
}

func TestExtractLastWord(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"String name", "name"},
		{"int x", "x"},
		{"Map<String, Integer> map", "map"},
		{"String[] args", "args"},
		{"String... args", "args"},
		{"int", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractLastWord(tt.input)
		if got != tt.want {
			t.Errorf("extractLastWord(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSupportsVarargs(t *testing.T) {
	tests := []struct {
		name     string
		params   []string
		argCount int
		want     bool
	}{
		{name: "non varargs", params: []string{"String name"}, argCount: 2, want: false},
		{name: "varargs exact fixed prefix", params: []string{"String prefix", "String... args"}, argCount: 1, want: true},
		{name: "varargs with extra args", params: []string{"String prefix", "String... args"}, argCount: 3, want: true},
		{name: "varargs too few args", params: []string{"String prefix", "String... args"}, argCount: 0, want: false},
	}
	for _, tt := range tests {
		got := supportsVarargs(tt.params, tt.argCount)
		if got != tt.want {
			t.Errorf("supportsVarargs(%q, %d) = %v, want %v", tt.params, tt.argCount, got, tt.want)
		}
	}
}

func TestFormatTypeExprSimple(t *testing.T) {
	tests := []struct {
		input *index.TypeExpr
		want  string
	}{
		{nil, ""},
		{&index.TypeExpr{Sym: "int"}, "int"},
		{&index.TypeExpr{Sym: "String"}, "String"},
		{&index.TypeExpr{Sym: "java/util/List#"}, "List"},
		{
			&index.TypeExpr{Sym: "java/util/List#", Args: []*index.TypeExpr{{Sym: "java/lang/String#"}}},
			"List<String>",
		},
		{
			&index.TypeExpr{Sym: "java/util/Map#", Args: []*index.TypeExpr{
				{Sym: "java/lang/String#"},
				{Sym: "java/lang/Integer#"},
			}},
			"Map<String, Integer>",
		},
	}
	for _, tt := range tests {
		got := formatTypeExprSimple(tt.input)
		if got != tt.want {
			t.Errorf("formatTypeExprSimple(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// Line is a test helper to extract the line from a hint's position.
func (h InlayHint) Line() int {
	return h.Position.Line
}
