package lsp

import (
	"testing"

	"github.com/fwrq41251/decaf/internal/index"
)

func TestOverloadDetail(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Increment 1 → 2.
		{"void add(String) (+1 overload)", "void add(String) (+2 overloads)"},
		// Increment 2 → 3.
		{"void add(String) (+2 overloads)", "void add(String) (+3 overloads)"},
		// Increment 9 → 10.
		{"void add(String) (+9 overloads)", "void add(String) (+10 overloads)"},
		// No " (+" marker: return as-is.
		{"int size()", "int size()"},
		// Empty string: return as-is.
		{"", ""},
	}
	for _, tt := range tests {
		got := overloadDetail(tt.input)
		if got != tt.want {
			t.Errorf("overloadDetail(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMethodCompletionItem(t *testing.T) {
	// Method with params: snippet with tabstop inside parens.
	sig := &index.SignatureInfo{
		Label:  "void doWork(String name)",
		Params: []string{"String name"},
	}
	item := methodCompletionItem("doWork", CompletionKindMethod, sig, "", "")
	if item.InsertText != "doWork($1)$0" {
		t.Errorf("method with params: InsertText = %q, want %q", item.InsertText, "doWork($1)$0")
	}
	if item.InsertTextFormat != InsertTextFormatSnippet {
		t.Errorf("method with params: InsertTextFormat = %d, want %d", item.InsertTextFormat, InsertTextFormatSnippet)
	}

	// Method with no params: empty parens snippet.
	sigNoParams := &index.SignatureInfo{
		Label: "int getCount()",
	}
	item2 := methodCompletionItem("getCount", CompletionKindMethod, sigNoParams, "", "")
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
