package lsp

import (
	"testing"

	"github.com/fwrq41251/decaf/internal/index"
)

func TestMethodCompletionItem(t *testing.T) {
	// Method with params: snippet with tabstop inside parens.
	sig := &index.SignatureInfo{
		Label:     "void doWork(String name)",
		HasParams: true,
		Params: []index.ParamInfo{
			{Name: "name", Type: "String"},
		},
	}
	item := methodCompletionItem("doWork", CompletionKindMethod, sig, "", "")
	if item.Label != "doWork(String name)" {
		t.Errorf("method with params: Label = %q, want %q", item.Label, "doWork(String name)")
	}
	if item.InsertText != "doWork(${1:name})$0" {
		t.Errorf("method with params: InsertText = %q, want %q", item.InsertText, "doWork(${1:name})$0")
	}
	if item.InsertTextFormat != InsertTextFormatSnippet {
		t.Errorf("method with params: InsertTextFormat = %d, want %d", item.InsertTextFormat, InsertTextFormatSnippet)
	}

	// Method with no params: empty parens snippet.
	sigNoParams := &index.SignatureInfo{
		Label: "int getCount()",
	}
	item2 := methodCompletionItem("getCount", CompletionKindMethod, sigNoParams, "", "")
	if item2.Label != "getCount()" {
		t.Errorf("method no params: Label = %q, want %q", item2.Label, "getCount()")
	}
	if item2.InsertText != "getCount()$0" {
		t.Errorf("method no params: InsertText = %q, want %q", item2.InsertText, "getCount()$0")
	}
	if item2.InsertTextFormat != InsertTextFormatSnippet {
		t.Errorf("method no params: InsertTextFormat = %d, want %d", item2.InsertTextFormat, InsertTextFormatSnippet)
	}

	// Field (non-method kind): plain text, no parens.
	item3 := methodCompletionItem("value", CompletionKindField, nil, "", "")
	if item3.InsertText != "value" {
		t.Errorf("field: InsertText = %q, want %q", item3.InsertText, "value")
	}
	if item3.InsertTextFormat != 0 {
		t.Errorf("field: InsertTextFormat = %d, want 0", item3.InsertTextFormat)
	}
}

func TestMethodCompletionItem_FallbackSnippet(t *testing.T) {
	sig := &index.SignatureInfo{
		Label:     "void doWork(String)",
		HasParams: true,
	}
	item := methodCompletionItem("doWork", CompletionKindMethod, sig, "", "")
	if item.InsertText != "doWork($1)$0" {
		t.Errorf("fallback snippet InsertText = %q, want %q", item.InsertText, "doWork($1)$0")
	}
}

func TestBuildLocalMethodInsertText(t *testing.T) {
	got := buildLocalMethodInsertText(MethodDecl{
		Name:   "doWork",
		Params: []string{"name", "count"},
	})
	if got != "doWork(${1:name}, ${2:count})$0" {
		t.Errorf("buildLocalMethodInsertText() = %q, want %q", got, "doWork(${1:name}, ${2:count})$0")
	}
}

func TestMethodCompletionLabel(t *testing.T) {
	sig := &index.SignatureInfo{
		Label:     "void get(String name, int count)",
		HasParams: true,
		Params: []index.ParamInfo{
			{Name: "name", Type: "String"},
			{Name: "count", Type: "int"},
		},
	}
	got := methodCompletionLabel("get", sig)
	if got != "get(String name, int count)" {
		t.Errorf("methodCompletionLabel() = %q, want %q", got, "get(String name, int count)")
	}
}

func TestFormatMethodDeclDetail(t *testing.T) {
	got := formatMethodDeclDetail(MethodDecl{Name: "doWork", Params: []string{"name", "count"}})
	if got != "doWork(name, count)" {
		t.Errorf("formatMethodDeclDetail() = %q, want %q", got, "doWork(name, count)")
	}
}

func TestExtractParamTypeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"String name", "String"},
		{"int x", "int"},
		{"String[] args", "String[]"},
		{"List<String> items", "List<String>"},
		{"Map<String, Integer> map", "Map<String, Integer>"},
		{"", ""},
		{"x", ""},
	}
	for _, tt := range tests {
		got := extractParamTypeName(tt.input)
		if got != tt.want {
			t.Errorf("extractParamTypeName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTypeMatchesExpected(t *testing.T) {
	tests := []struct {
		candidate string
		expected  string
		want      bool
	}{
		{"String", "String", true},
		{"int", "int", true},
		{"String", "int", false},
		{"java/lang/String#", "String", true},
		{"String", "java/lang/String#", true},
		{"List<String>", "List", true},
		{"List<String>", "List<Integer>", true}, // base type match
		{"", "String", false},
		{"String", "", false},
	}
	for _, tt := range tests {
		got := typeMatchesExpected(tt.candidate, tt.expected)
		if got != tt.want {
			t.Errorf("typeMatchesExpected(%q, %q) = %v, want %v", tt.candidate, tt.expected, got, tt.want)
		}
	}
}

func TestCompleteSnippets(t *testing.T) {
	h := &Handler{}

	// Test case 1: Inside a class, prefix "ma"
	ctx1 := &CompletionCtx{
		Scope:  ScopeClass,
		Prefix: "ma",
	}
	items1 := h.completeSnippets(ctx1)
	foundMain := false
	for _, item := range items1 {
		if item.Label == "main" {
			foundMain = true
			if item.InsertTextFormat != InsertTextFormatSnippet {
				t.Errorf("main snippet: expected InsertTextFormatSnippet, got %v", item.InsertTextFormat)
			}
			break
		}
	}
	if !foundMain {
		t.Error("expected 'main' snippet in ScopeClass with prefix 'ma'")
	}

	// Test case 2: Inside a block, prefix "sou"
	ctx2 := &CompletionCtx{
		Scope:  ScopeBlock,
		Prefix: "sou",
	}
	items2 := h.completeSnippets(ctx2)
	foundSout := false
	for _, item := range items2 {
		if item.Label == "sout" {
			foundSout = true
			break
		}
	}
	if !foundSout {
		t.Error("expected 'sout' snippet in ScopeBlock with prefix 'sou'")
	}

	// Test case 3: Inside a block, prefix "ma" - should NOT show main
	ctx3 := &CompletionCtx{
		Scope:  ScopeBlock,
		Prefix: "ma",
	}
	items3 := h.completeSnippets(ctx3)
	for _, item := range items3 {
		if item.Label == "main" {
			t.Error("did not expect 'main' snippet in ScopeBlock")
		}
	}
}
