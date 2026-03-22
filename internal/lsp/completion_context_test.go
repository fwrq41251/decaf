package lsp

import (
	"testing"
)

func TestParseCompletionCtx_DotCompletion(t *testing.T) {
	src := []byte(`package com.example;

import java.util.List;
import java.util.Map;

public class MyClass {
    private String name;
    private int count;

    public void doSomething(List<String> items, int limit) {
        String local1 = "hello";
        int local2 = 42;
        items.
    }

    public void otherMethod() {}
}`)
	// "items." is on line 12 (0-indexed), character 14 (after the dot)
	ctx := parseCompletionCtx(src, 12, 14)
	if ctx.Kind != CompletionDot {
		t.Fatalf("expected CompletionDot, got %d", ctx.Kind)
	}
	if ctx.Receiver != "items" {
		t.Fatalf("expected receiver 'items', got %q", ctx.Receiver)
	}
	if ctx.Prefix != "" {
		t.Fatalf("expected empty prefix, got %q", ctx.Prefix)
	}
}

func TestParseCompletionCtx_DotCompletionWithPrefix(t *testing.T) {
	src := []byte(`package com.example;

import java.util.List;

public class MyClass {
    public void doSomething(List<String> items) {
        items.ge
    }
}`)
	// "items.ge" on line 6, character 16 (after "ge")
	ctx := parseCompletionCtx(src, 6, 16)
	if ctx.Kind != CompletionDot {
		t.Fatalf("expected CompletionDot, got %d", ctx.Kind)
	}
	if ctx.Receiver != "items" {
		t.Fatalf("expected receiver 'items', got %q", ctx.Receiver)
	}
	if ctx.Prefix != "ge" {
		t.Fatalf("expected prefix 'ge', got %q", ctx.Prefix)
	}
}

func TestParseCompletionCtx_LexicalCompletion(t *testing.T) {
	src := []byte(`package com.example;

public class MyClass {
    public void doSomething() {
        pri
    }
}`)
	// "pri" on line 4, character 11 (after "pri")
	ctx := parseCompletionCtx(src, 4, 11)
	if ctx.Kind != CompletionLexical {
		t.Fatalf("expected CompletionLexical, got %d", ctx.Kind)
	}
	if ctx.Prefix != "pri" {
		t.Fatalf("expected prefix 'pri', got %q", ctx.Prefix)
	}
}

func TestParseCompletionCtx_LocalVariables(t *testing.T) {
	src := []byte(`package com.example;

public class MyClass {
    public void doSomething() {
        String local1 = "hello";
        int local2 = 42;
        
    }
}`)
	// cursor at blank line 6, character 8
	ctx := parseCompletionCtx(src, 6, 8)
	if len(ctx.Locals) != 2 {
		t.Fatalf("expected 2 locals, got %d: %+v", len(ctx.Locals), ctx.Locals)
	}
	if ctx.Locals[0].Name != "local1" || ctx.Locals[0].Type != "String" {
		t.Fatalf("expected local1:String, got %s:%s", ctx.Locals[0].Name, ctx.Locals[0].Type)
	}
	if ctx.Locals[1].Name != "local2" {
		t.Fatalf("expected local2, got %s", ctx.Locals[1].Name)
	}
}

func TestParseCompletionCtx_Parameters(t *testing.T) {
	src := []byte(`package com.example;

import java.util.List;

public class MyClass {
    public void doSomething(List<String> items, int limit) {
        
    }
}`)
	// cursor at line 6, character 8
	ctx := parseCompletionCtx(src, 6, 8)
	if len(ctx.Params) != 2 {
		t.Fatalf("expected 2 params, got %d: %+v", len(ctx.Params), ctx.Params)
	}
	if ctx.Params[0].Name != "items" || ctx.Params[0].Type != "List<String>" {
		t.Fatalf("expected items:List<String>, got %s:%s", ctx.Params[0].Name, ctx.Params[0].Type)
	}
	if ctx.Params[1].Name != "limit" {
		t.Fatalf("expected limit, got %s", ctx.Params[1].Name)
	}
}

func TestParseCompletionCtx_ClassFields(t *testing.T) {
	src := []byte(`package com.example;

public class MyClass {
    private String name;
    private int count;

    public void doSomething() {
        
    }
}`)
	// cursor at line 7, character 8
	ctx := parseCompletionCtx(src, 7, 8)
	if len(ctx.ClassFields) != 2 {
		t.Fatalf("expected 2 class fields, got %d: %+v", len(ctx.ClassFields), ctx.ClassFields)
	}
	if ctx.ClassFields[0].Name != "name" || ctx.ClassFields[0].Type != "String" {
		t.Fatalf("expected name:String, got %s:%s", ctx.ClassFields[0].Name, ctx.ClassFields[0].Type)
	}
	if ctx.ClassFields[1].Name != "count" {
		t.Fatalf("expected count, got %s", ctx.ClassFields[1].Name)
	}
}

func TestParseCompletionCtx_ClassMethods(t *testing.T) {
	src := []byte(`package com.example;

public class MyClass {
    public void doSomething() {
        
    }

    public void otherMethod() {}
    public String getName() { return ""; }
}`)
	// cursor at line 4, character 8
	ctx := parseCompletionCtx(src, 4, 8)
	if len(ctx.ClassMethods) != 3 {
		t.Fatalf("expected 3 class methods, got %d: %v", len(ctx.ClassMethods), ctx.ClassMethods)
	}
	expected := map[string]bool{"doSomething": true, "otherMethod": true, "getName": true}
	for _, m := range ctx.ClassMethods {
		if !expected[m] {
			t.Fatalf("unexpected method %q", m)
		}
	}
}

func TestParseCompletionCtx_Imports(t *testing.T) {
	src := []byte(`package com.example;

import java.util.List;
import java.util.Map;
import static java.util.Collections.emptyList;
import java.io.*;

public class MyClass {
    public void doSomething() {
        
    }
}`)
	// cursor at line 9, character 8
	ctx := parseCompletionCtx(src, 9, 8)
	if len(ctx.Imports) != 4 {
		t.Fatalf("expected 4 imports, got %d: %+v", len(ctx.Imports), ctx.Imports)
	}
	// First import: java.util.List
	if ctx.Imports[0].Path != "java.util.List" || ctx.Imports[0].Static || ctx.Imports[0].Wildcard {
		t.Fatalf("import 0 unexpected: %+v", ctx.Imports[0])
	}
	// Second import: java.util.Map
	if ctx.Imports[1].Path != "java.util.Map" || ctx.Imports[1].Static || ctx.Imports[1].Wildcard {
		t.Fatalf("import 1 unexpected: %+v", ctx.Imports[1])
	}
	// Third: static import
	if !ctx.Imports[2].Static {
		t.Fatalf("import 2 should be static: %+v", ctx.Imports[2])
	}
	// Fourth: wildcard import
	if !ctx.Imports[3].Wildcard {
		t.Fatalf("import 3 should be wildcard: %+v", ctx.Imports[3])
	}
}

func TestParseCompletionCtx_Package(t *testing.T) {
	src := []byte(`package com.example;

public class MyClass {
    public void doSomething() {
        
    }
}`)
	ctx := parseCompletionCtx(src, 4, 8)
	if ctx.Package != "com.example" {
		t.Fatalf("expected package 'com.example', got %q", ctx.Package)
	}
}

func TestParseCompletionCtx_EnclosingClass(t *testing.T) {
	src := []byte(`package com.example;

public class MyClass {
    public void doSomething() {
        
    }
}`)
	ctx := parseCompletionCtx(src, 4, 8)
	if ctx.EnclosingClass != "MyClass" {
		t.Fatalf("expected enclosing class 'MyClass', got %q", ctx.EnclosingClass)
	}
}

func TestParseCompletionCtx_ThisDot(t *testing.T) {
	src := []byte(`package com.example;

public class MyClass {
    private String name;

    public void doSomething() {
        this.
    }
}`)
	// "this." on line 6, character 13 (after the dot)
	ctx := parseCompletionCtx(src, 6, 13)
	if ctx.Kind != CompletionDot {
		t.Fatalf("expected CompletionDot, got %d", ctx.Kind)
	}
	if ctx.Receiver != "this" {
		t.Fatalf("expected receiver 'this', got %q", ctx.Receiver)
	}
}

func TestParseCompletionCtx_ChainedDot(t *testing.T) {
	src := []byte(`package com.example;

public class MyClass {
    public void doSomething() {
        foo.bar.
    }
}`)
	// "foo.bar." on line 4, character 16 (after the last dot)
	ctx := parseCompletionCtx(src, 4, 16)
	if ctx.Kind != CompletionDot {
		t.Fatalf("expected CompletionDot, got %d", ctx.Kind)
	}
	if ctx.Receiver != "foo.bar" {
		t.Fatalf("expected receiver 'foo.bar', got %q", ctx.Receiver)
	}
	if ctx.Prefix != "" {
		t.Fatalf("expected empty prefix, got %q", ctx.Prefix)
	}
}

func TestExtractReceiverBefore_MethodInvocation(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple method call",
			input:    "items.get(0).",
			expected: "items.get",
		},
		{
			name:     "chained method then field",
			input:    "foo.bar().baz.",
			expected: "foo.bar.baz",
		},
		{
			name:     "method call with string arg",
			input:    `map.get("key").`,
			expected: "map.get",
		},
		{
			name:     "method call with multiple args",
			input:    "foo.bar(a, b).",
			expected: "foo.bar",
		},
		{
			name:     "nested calls",
			input:    "a.b(c.d()).",
			expected: "a.b",
		},
		{
			name:     "simple identifier",
			input:    "items.",
			expected: "items",
		},
		{
			name:     "chained fields",
			input:    "foo.bar.baz.",
			expected: "foo.bar.baz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := []byte(tt.input)
			// pos is at the trailing dot, so len-1; extractReceiverBefore works on content before pos
			pos := len(content) - 1
			got := extractReceiverBefore(content, pos)
			if got != tt.expected {
				t.Errorf("extractReceiverBefore(%q, %d) = %q, want %q", tt.input, pos, got, tt.expected)
			}
		})
	}
}
