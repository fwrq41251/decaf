package lsp

import (
	"testing"

	"github.com/fwrq41251/decaf/internal/index"
)

func TestMethodCompletionItem(t *testing.T) {
	// Method with params: snippet with tabstop inside parens.
	sig := &index.SignatureInfo{
		Label:  "void doWork(String name)",
		Params: []string{"String name"},
	}
	item := methodCompletionItem("doWork", CompletionKindMethod, sig, "")
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
	item2 := methodCompletionItem("getCount", CompletionKindMethod, sigNoParams, "")
	if item2.InsertText != "getCount()$0" {
		t.Errorf("method no params: InsertText = %q, want %q", item2.InsertText, "getCount()$0")
	}
	if item2.InsertTextFormat != InsertTextFormatSnippet {
		t.Errorf("method no params: InsertTextFormat = %d, want %d", item2.InsertTextFormat, InsertTextFormatSnippet)
	}

	// Field (non-method kind): plain text, no parens.
	item3 := methodCompletionItem("value", CompletionKindField, nil, "")
	if item3.InsertText != "value" {
		t.Errorf("field: InsertText = %q, want %q", item3.InsertText, "value")
	}
	if item3.InsertTextFormat != 0 {
		t.Errorf("field: InsertTextFormat = %d, want 0", item3.InsertTextFormat)
	}
}
