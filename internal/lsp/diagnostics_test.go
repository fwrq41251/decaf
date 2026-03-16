package lsp

import "testing"

func TestComputeDiagnostics_ValidCode(t *testing.T) {
	code := `package com.example;

public class Hello {
    public static void main(String[] args) {
        System.out.println("hello");
    }
}
`
	diags := computeDiagnostics(code)
	if len(diags) != 0 {
		t.Errorf("expected no diagnostics for valid code, got %d: %v", len(diags), diags)
	}
}

func TestComputeDiagnostics_MissingSemicolon(t *testing.T) {
	code := `package com.example;

public class Hello {
    int x = 1
}
`
	diags := computeDiagnostics(code)
	if len(diags) == 0 {
		t.Fatal("expected diagnostics for missing semicolon, got none")
	}
	for _, d := range diags {
		if d.Source != "decaf" {
			t.Errorf("expected source 'decaf', got %q", d.Source)
		}
		if d.Severity != SeverityError {
			t.Errorf("expected severity %d, got %d", SeverityError, d.Severity)
		}
	}
}

func TestComputeDiagnostics_UnclosedBrace(t *testing.T) {
	code := `package com.example;

public class Hello {
    public void foo() {
        int x = 1;

}
`
	diags := computeDiagnostics(code)
	if len(diags) == 0 {
		t.Fatal("expected diagnostics for unclosed brace, got none")
	}
}

func TestComputeDiagnostics_EmptyContent(t *testing.T) {
	diags := computeDiagnostics("")
	if len(diags) != 0 {
		t.Errorf("expected no diagnostics for empty content, got %d", len(diags))
	}
}
