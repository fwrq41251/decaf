package lsp

import (
	"strings"
	"testing"
)

func TestCompletePostfix_BasicTemplates(t *testing.T) {
	// Simulate: "result.var|" at line 3
	content := []byte("package test;\nclass Foo {\n  void m() {\n    result.var\n  }\n}")
	cctx := parseCompletionCtx(nil, content, 3, 14) // cursor after "result.var"

	if cctx.Kind != CompletionDot {
		t.Fatalf("expected CompletionDot, got %d", cctx.Kind)
	}
	if cctx.Receiver != "result" {
		t.Fatalf("expected receiver 'result', got %q", cctx.Receiver)
	}
	if cctx.Prefix != "var" {
		t.Fatalf("expected prefix 'var', got %q", cctx.Prefix)
	}

	items := completePostfix(cctx, content)

	// Should have "var" template
	found := false
	for _, item := range items {
		if item.FilterText == "var" {
			found = true
			if item.TextEdit == nil {
				t.Fatal("expected TextEdit to be set for postfix completion")
			}
			if item.InsertTextFormat != InsertTextFormatSnippet {
				t.Errorf("expected snippet format, got %d", item.InsertTextFormat)
			}
			if !strings.Contains(item.TextEdit.NewText, "var ${1:name} = result;") {
				t.Errorf("unexpected NewText: %q", item.TextEdit.NewText)
			}
			break
		}
	}
	if !found {
		t.Error("expected 'var' postfix template in results")
	}
}

func TestCompletePostfix_FiltersByPrefix(t *testing.T) {
	content := []byte("package test;\nclass Foo {\n  void m() {\n    result.so\n  }\n}")
	cctx := parseCompletionCtx(nil, content, 3, 13) // cursor after "result.so"

	items := completePostfix(cctx, content)

	for _, item := range items {
		if !strings.HasPrefix(item.FilterText, "so") {
			t.Errorf("item %q should start with 'so' prefix", item.FilterText)
		}
	}

	// Should contain "sout"
	foundSout := false
	for _, item := range items {
		if item.FilterText == "sout" {
			foundSout = true
			break
		}
	}
	if !foundSout {
		t.Error("expected 'sout' postfix template with prefix 'so'")
	}
}

func TestCompletePostfix_OnlyInBlockScope(t *testing.T) {
	// Class body scope — postfix should not appear
	content := []byte("package test;\nclass Foo {\n  result.\n}")
	cctx := parseCompletionCtx(nil, content, 2, 9) // cursor after "result."
	cctx.Scope = ScopeClass

	items := completePostfix(cctx, content)
	if len(items) != 0 {
		t.Errorf("expected no postfix items in ScopeClass, got %d", len(items))
	}
}

func TestCompletePostfix_NotTemplate(t *testing.T) {
	content := []byte("package test;\nclass Foo {\n  void m() {\n    flag.not\n  }\n}")
	cctx := parseCompletionCtx(nil, content, 3, 12) // cursor after "flag.not"

	items := completePostfix(cctx, content)

	found := false
	for _, item := range items {
		if item.FilterText == "not" {
			found = true
			if item.TextEdit == nil {
				t.Fatal("expected TextEdit")
			}
			if !strings.Contains(item.TextEdit.NewText, "!flag") {
				t.Errorf("unexpected NewText: %q", item.TextEdit.NewText)
			}
			break
		}
	}
	if !found {
		t.Error("expected 'not' postfix template")
	}
}

func TestCompletePostfix_IfTemplate(t *testing.T) {
	content := []byte("package test;\nclass Foo {\n  void m() {\n    condition.if\n  }\n}")
	cctx := parseCompletionCtx(nil, content, 3, 16) // cursor after "condition.if"

	items := completePostfix(cctx, content)

	found := false
	for _, item := range items {
		if item.FilterText == "if" {
			found = true
			if item.TextEdit == nil {
				t.Fatal("expected TextEdit")
			}
			if !strings.Contains(item.TextEdit.NewText, "if (condition)") {
				t.Errorf("unexpected NewText: %q", item.TextEdit.NewText)
			}
			break
		}
	}
	if !found {
		t.Error("expected 'if' postfix template")
	}
}

func TestCompletePostfix_TextEditRange(t *testing.T) {
	// "    result.var" — result starts at col 4, "var" ends at col 14
	content := []byte("package test;\nclass Foo {\n  void m() {\n    result.var\n  }\n}")
	cctx := parseCompletionCtx(nil, content, 3, 14) // cursor after "result.var"

	items := completePostfix(cctx, content)

	for _, item := range items {
		if item.FilterText == "var" {
			te := item.TextEdit
			if te == nil {
				t.Fatal("expected TextEdit")
			}
			// "result" starts at line 3, col 4
			if te.Range.Start.Line != 3 || te.Range.Start.Character != 4 {
				t.Errorf("expected start (3,4), got (%d,%d)", te.Range.Start.Line, te.Range.Start.Character)
			}
			// "result.var" ends at line 3, col 14
			if te.Range.End.Line != 3 || te.Range.End.Character != 14 {
				t.Errorf("expected end (3,14), got (%d,%d)", te.Range.End.Line, te.Range.End.Character)
			}
			break
		}
	}
}

func TestCompletePostfix_MethodCallReceiver(t *testing.T) {
	content := []byte("package test;\nclass Foo {\n  void m() {\n    list.get(0).var\n  }\n}")
	cctx := parseCompletionCtx(nil, content, 3, 19) // cursor after "list.get(0).var"

	items := completePostfix(cctx, content)

	found := false
	for _, item := range items {
		if item.FilterText == "var" {
			found = true
			if item.TextEdit == nil {
				t.Fatal("expected TextEdit")
			}
			if !strings.Contains(item.TextEdit.NewText, "list.get(0)") {
				t.Errorf("unexpected NewText: %q", item.TextEdit.NewText)
			}
			break
		}
	}
	if !found {
		t.Error("expected 'var' postfix template for method call receiver")
	}
}

func TestCompletePostfix_NoPrefixShowsAll(t *testing.T) {
	content := []byte("package test;\nclass Foo {\n  void m() {\n    result.\n  }\n}")
	cctx := parseCompletionCtx(nil, content, 3, 11) // cursor after "result."

	items := completePostfix(cctx, content)

	if len(items) != len(postfixTemplates) {
		t.Errorf("with no prefix, expected %d postfix templates, got %d", len(postfixTemplates), len(items))
	}
}

func TestNeedsParens(t *testing.T) {
	tests := []struct {
		expr string
		want bool
	}{
		{"flag", false},
		{"obj.isValid()", false},
		{"a && b", true},
		{"x > 0", true},
		{"a + b", true},
		{"list.isEmpty()", false},
	}
	for _, tt := range tests {
		if got := needsParens(tt.expr); got != tt.want {
			t.Errorf("needsParens(%q) = %v, want %v", tt.expr, got, tt.want)
		}
	}
}
