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
	ctx := parseCompletionCtx(nil, src, 12, 14)
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
	ctx := parseCompletionCtx(nil, src, 6, 16)
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

func TestParseCompletionCtx_LambdaSingleParam(t *testing.T) {
	src := []byte(`package com.example;

import java.util.List;

public class MyClass {
    public void doSomething(List<String> items) {
        items.stream().map(a -> a.le)
    }
}`)
	ctx := parseCompletionCtx(nil, src, 6, 36)
	if ctx.Kind != CompletionDot {
		t.Fatalf("expected CompletionDot, got %d", ctx.Kind)
	}
	if ctx.Receiver != "a" {
		t.Fatalf("expected receiver 'a', got %q", ctx.Receiver)
	}
	if len(ctx.LambdaParams) != 1 || ctx.LambdaParams[0].Name != "a" {
		t.Fatalf("expected lambda param 'a', got %+v", ctx.LambdaParams)
	}
}

func TestParseCompletionCtx_LambdaTypedParam(t *testing.T) {
	src := []byte(`package com.example;

public class MyClass {
    interface Mapper {
        int map(String value);
    }

    public void doSomething(Mapper mapper) {
        mapper.map(((String value) -> value.len))
    }
}`)
	ctx := parseCompletionCtx(nil, src, 8, 46)
	if len(ctx.LambdaParams) != 1 {
		t.Fatalf("expected 1 lambda param, got %+v", ctx.LambdaParams)
	}
	if ctx.LambdaParams[0].Name != "value" {
		t.Fatalf("expected lambda param 'value', got %+v", ctx.LambdaParams[0])
	}
	if ctx.LambdaParams[0].Type == nil || ctx.LambdaParams[0].Type.String() != "String" {
		t.Fatalf("expected lambda param type String, got %+v", ctx.LambdaParams[0].Type)
	}
}

func TestParseCompletionCtx_NestedLambdaKeepsOuterCapture(t *testing.T) {
	src := []byte(`package com.example;

import java.util.List;

public class MyClass {
    public void doSomething(List<String> items) {
        items.stream().map(outer -> items.stream().map(inner -> outer.len))
    }
}`)
	ctx := parseCompletionCtx(nil, src, 6, 66)
	if len(ctx.LambdaParams) != 2 {
		t.Fatalf("expected 2 lambda params, got %+v", ctx.LambdaParams)
	}
	if ctx.LambdaParams[0].Name != "outer" || ctx.LambdaParams[1].Name != "inner" {
		t.Fatalf("expected outer then inner lambda params, got %+v", ctx.LambdaParams)
	}
}

func TestParseCompletionCtx_ChainedCallNewline(t *testing.T) {
	// Chained call with dot on the next line:
	//   items.stream()
	//       .fil
	src := []byte(`package com.example;
import java.util.List;
public class MyClass {
    public void doSomething(List<String> items) {
        items.stream()
            .fil
    }
}`)
	// ".fil" on line 5, character 16 (after "fil")
	ctx := parseCompletionCtx(nil, src, 5, 16)
	if ctx.Kind != CompletionDot {
		t.Fatalf("expected CompletionDot, got %d", ctx.Kind)
	}
	if ctx.Prefix != "fil" {
		t.Fatalf("expected prefix 'fil', got %q", ctx.Prefix)
	}
	if ctx.Receiver != "items.stream" {
		t.Fatalf("expected receiver 'items.stream', got %q", ctx.Receiver)
	}
}

func TestParseCompletionCtx_ChainedCallNewlineNoPrefx(t *testing.T) {
	// Chained call with dot on the next line, cursor right after dot:
	//   items.stream()
	//       .
	src := []byte(`package com.example;
import java.util.List;
public class MyClass {
    public void doSomething(List<String> items) {
        items.stream()
            .
    }
}`)
	// "." on line 5, character 13 (after the dot)
	ctx := parseCompletionCtx(nil, src, 5, 13)
	if ctx.Kind != CompletionDot {
		t.Fatalf("expected CompletionDot, got %d", ctx.Kind)
	}
	if ctx.Prefix != "" {
		t.Fatalf("expected empty prefix, got %q", ctx.Prefix)
	}
}

func TestParseCompletionCtx_DotOnPrevLineWithPrefixOnNewLine(t *testing.T) {
	// Dot at the end of the previous line, prefix on the new line:
	//   items.stream().
	//       fil
	src := []byte(`package com.example;
import java.util.List;
public class MyClass {
    public void doSomething(List<String> items) {
        items.stream().
            fil
    }
}`)
	// "fil" on line 5, character 15 (after "fil")
	ctx := parseCompletionCtx(nil, src, 5, 15)
	if ctx.Kind != CompletionDot {
		t.Fatalf("expected CompletionDot, got %d", ctx.Kind)
	}
	if ctx.Prefix != "fil" {
		t.Fatalf("expected prefix 'fil', got %q", ctx.Prefix)
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
	ctx := parseCompletionCtx(nil, src, 4, 11)
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
	ctx := parseCompletionCtx(nil, src, 6, 8)
	if len(ctx.Locals) != 2 {
		t.Fatalf("expected 2 locals, got %d: %+v", len(ctx.Locals), ctx.Locals)
	}
	if ctx.Locals[0].Name != "local1" || ctx.Locals[0].Type.String() != "String" {
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
	ctx := parseCompletionCtx(nil, src, 6, 8)
	if len(ctx.Params) != 2 {
		t.Fatalf("expected 2 params, got %d: %+v", len(ctx.Params), ctx.Params)
	}
	if ctx.Params[0].Name != "items" || ctx.Params[0].Type.String() != "List<String>" {
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
	ctx := parseCompletionCtx(nil, src, 7, 8)
	if len(ctx.ClassFields) != 2 {
		t.Fatalf("expected 2 class fields, got %d: %+v", len(ctx.ClassFields), ctx.ClassFields)
	}
	if ctx.ClassFields[0].Name != "name" || ctx.ClassFields[0].Type.String() != "String" {
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
	ctx := parseCompletionCtx(nil, src, 4, 8)
	if len(ctx.ClassMethods) != 3 {
		t.Fatalf("expected 3 class methods, got %d: %v", len(ctx.ClassMethods), ctx.ClassMethods)
	}
	expected := map[string]bool{"doSomething": true, "otherMethod": true, "getName": true}
	for _, m := range ctx.ClassMethods {
		if !expected[m.Name] {
			t.Fatalf("unexpected method %q", m.Name)
		}
	}
}

func TestParseCompletionCtx_ClassMethodParams(t *testing.T) {
	src := []byte(`package com.example;

public class MyClass {
    public void doSomething(String name, int count) {
        
    }
}`)
	ctx := parseCompletionCtx(nil, src, 4, 8)
	if len(ctx.ClassMethods) != 1 {
		t.Fatalf("expected 1 class method, got %d: %v", len(ctx.ClassMethods), ctx.ClassMethods)
	}
	if ctx.ClassMethods[0].Name != "doSomething" {
		t.Fatalf("expected method doSomething, got %q", ctx.ClassMethods[0].Name)
	}
	if len(ctx.ClassMethods[0].Params) != 2 {
		t.Fatalf("expected 2 params, got %d: %v", len(ctx.ClassMethods[0].Params), ctx.ClassMethods[0].Params)
	}
	if ctx.ClassMethods[0].Params[0] != "name" || ctx.ClassMethods[0].Params[1] != "count" {
		t.Fatalf("unexpected params: %v", ctx.ClassMethods[0].Params)
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
	ctx := parseCompletionCtx(nil, src, 9, 8)
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
	ctx := parseCompletionCtx(nil, src, 4, 8)
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
	ctx := parseCompletionCtx(nil, src, 4, 8)
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
	ctx := parseCompletionCtx(nil, src, 6, 13)
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
	ctx := parseCompletionCtx(nil, src, 4, 16)
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

func TestParseCompletionCtx_EnhancedForVariable(t *testing.T) {
	src := []byte(`package com.example;
import java.util.List;
public class MyClass {
    public void doSomething(List<String> list) {
        for (String item : list) {
            item.
        }
    }
}`)
	// cursor inside the for body at "item." — line 5, character 17
	ctx := parseCompletionCtx(nil, src, 5, 17)
	if ctx.Kind != CompletionDot {
		t.Fatalf("expected CompletionDot, got %d", ctx.Kind)
	}
	if ctx.Receiver != "item" {
		t.Fatalf("expected receiver 'item', got %q", ctx.Receiver)
	}
	// "item" should appear in Locals with type String.
	found := false
	for _, l := range ctx.Locals {
		if l.Name == "item" {
			found = true
			if l.Type == nil || l.Type.Sym != "String" {
				t.Fatalf("expected type String, got %v", l.Type)
			}
		}
	}
	if !found {
		t.Fatalf("expected 'item' in locals, got %+v", ctx.Locals)
	}
}

func TestParseCompletionCtx_CatchVariable(t *testing.T) {
	src := []byte(`package com.example;
import java.io.IOException;
public class MyClass {
    public void doSomething() {
        try {
        } catch (IOException e) {
            e.
        }
    }
}`)
	// cursor at "e." — line 6, character 14
	ctx := parseCompletionCtx(nil, src, 6, 14)
	if ctx.Kind != CompletionDot {
		t.Fatalf("expected CompletionDot, got %d", ctx.Kind)
	}
	if ctx.Receiver != "e" {
		t.Fatalf("expected receiver 'e', got %q", ctx.Receiver)
	}
	found := false
	for _, l := range ctx.Locals {
		if l.Name == "e" {
			found = true
			if l.Type == nil || l.Type.Sym != "IOException" {
				t.Fatalf("expected type IOException, got %v", l.Type)
			}
		}
	}
	if !found {
		t.Fatalf("expected 'e' in locals, got %+v", ctx.Locals)
	}
}

func TestParseCompletionCtx_TryWithResources(t *testing.T) {
	src := []byte(`package com.example;
import java.io.InputStream;
public class MyClass {
    public void doSomething() {
        try (InputStream stream = null) {
            stream.
        }
    }
}`)
	// cursor at "stream." — line 5, character 19
	ctx := parseCompletionCtx(nil, src, 5, 19)
	if ctx.Kind != CompletionDot {
		t.Fatalf("expected CompletionDot, got %d", ctx.Kind)
	}
	if ctx.Receiver != "stream" {
		t.Fatalf("expected receiver 'stream', got %q", ctx.Receiver)
	}
	found := false
	for _, l := range ctx.Locals {
		if l.Name == "stream" {
			found = true
			if l.Type == nil || l.Type.Sym != "InputStream" {
				t.Fatalf("expected type InputStream, got %v", l.Type)
			}
		}
	}
	if !found {
		t.Fatalf("expected 'stream' in locals, got %+v", ctx.Locals)
	}
}

func TestParseCompletionCtx_VarNewExpression(t *testing.T) {
	src := []byte(`package com.example;
public class MyClass {
    public void doSomething() {
        var list = new ArrayList<String>();
        list.
    }
}`)
	// cursor at "list." — line 4, character 13
	ctx := parseCompletionCtx(nil, src, 4, 13)
	if ctx.Kind != CompletionDot {
		t.Fatalf("expected CompletionDot, got %d", ctx.Kind)
	}
	found := false
	for _, l := range ctx.Locals {
		if l.Name == "list" {
			found = true
			if l.Type == nil {
				t.Fatal("expected type for 'list', got nil")
			}
			if l.Type.Sym != "ArrayList" {
				t.Fatalf("expected type ArrayList, got %s", l.Type.Sym)
			}
			if len(l.Type.Args) != 1 || l.Type.Args[0].Sym != "String" {
				t.Fatalf("expected type args [String], got %v", l.Type.Args)
			}
		}
	}
	if !found {
		t.Fatalf("expected 'list' in locals, got %+v", ctx.Locals)
	}
}

func TestParseCompletionCtx_VarStringLiteral(t *testing.T) {
	src := []byte(`package com.example;
public class MyClass {
    public void doSomething() {
        var name = "hello";
        name.
    }
}`)
	ctx := parseCompletionCtx(nil, src, 4, 13)
	found := false
	for _, l := range ctx.Locals {
		if l.Name == "name" {
			found = true
			if l.Type == nil || l.Type.Sym != "String" {
				t.Fatalf("expected type String, got %v", l.Type)
			}
		}
	}
	if !found {
		t.Fatalf("expected 'name' in locals, got %+v", ctx.Locals)
	}
}

func TestParseCompletionCtx_VarCastExpression(t *testing.T) {
	src := []byte(`package com.example;
public class MyClass {
    public void doSomething() {
        var obj = (MyClass) something;
        obj.
    }
}`)
	ctx := parseCompletionCtx(nil, src, 4, 12)
	found := false
	for _, l := range ctx.Locals {
		if l.Name == "obj" {
			found = true
			if l.Type == nil || l.Type.Sym != "MyClass" {
				t.Fatalf("expected type MyClass, got %v", l.Type)
			}
		}
	}
	if !found {
		t.Fatalf("expected 'obj' in locals, got %+v", ctx.Locals)
	}
}

func TestParseCompletionCtx_VarMethodInvocation(t *testing.T) {
	src := []byte(`package com.example;
public class MyClass {
    public void doSomething() {
        var list = List.of("a", "b");
        list.
    }
}`)
	ctx := parseCompletionCtx(nil, src, 4, 13)
	found := false
	for _, l := range ctx.Locals {
		if l.Name == "list" {
			found = true
			// Type should be nil (deferred), Initializer should be set.
			if l.Type != nil {
				t.Fatalf("expected nil type for method invocation var, got %v", l.Type)
			}
			if l.Initializer == nil {
				t.Fatal("expected Initializer to be set for method invocation var")
			}
			if l.Initializer.Receiver != "List" {
				t.Fatalf("expected receiver 'List', got %q", l.Initializer.Receiver)
			}
			if l.Initializer.MethodName != "of" {
				t.Fatalf("expected method 'of', got %q", l.Initializer.MethodName)
			}
		}
	}
	if !found {
		t.Fatalf("expected 'list' in locals, got %+v", ctx.Locals)
	}
}

func TestParseCompletionCtx_VarIntLiteral(t *testing.T) {
	src := []byte(`package com.example;
public class MyClass {
    public void doSomething() {
        var num = 42;
        num.
    }
}`)
	ctx := parseCompletionCtx(nil, src, 4, 12)
	found := false
	for _, l := range ctx.Locals {
		if l.Name == "num" {
			found = true
			if l.Type == nil || l.Type.Sym != "int" {
				t.Fatalf("expected type 'int' for int literal var, got %v", l.Type)
			}
		}
	}
	if !found {
		t.Fatalf("expected 'num' in locals, got %+v", ctx.Locals)
	}
}

func TestParseCompletionCtx_VarLongLiteral(t *testing.T) {
	src := []byte(`package com.example;
public class MyClass {
    public void doSomething() {
        var num = 42L;
        num.
    }
}`)
	ctx := parseCompletionCtx(nil, src, 4, 12)
	for _, l := range ctx.Locals {
		if l.Name == "num" {
			if l.Type == nil || l.Type.Sym != "long" {
				t.Fatalf("expected type 'long', got %v", l.Type)
			}
			return
		}
	}
	t.Fatalf("expected 'num' in locals, got %+v", ctx.Locals)
}

func TestParseCompletionCtx_VarDoubleLiteral(t *testing.T) {
	src := []byte(`package com.example;
public class MyClass {
    public void doSomething() {
        var pi = 3.14;
        pi.
    }
}`)
	ctx := parseCompletionCtx(nil, src, 4, 11)
	for _, l := range ctx.Locals {
		if l.Name == "pi" {
			if l.Type == nil || l.Type.Sym != "double" {
				t.Fatalf("expected type 'double', got %v", l.Type)
			}
			return
		}
	}
	t.Fatalf("expected 'pi' in locals, got %+v", ctx.Locals)
}

func TestParseCompletionCtx_VarFloatLiteral(t *testing.T) {
	src := []byte(`package com.example;
public class MyClass {
    public void doSomething() {
        var f = 3.14f;
        f.
    }
}`)
	ctx := parseCompletionCtx(nil, src, 4, 10)
	for _, l := range ctx.Locals {
		if l.Name == "f" {
			if l.Type == nil || l.Type.Sym != "float" {
				t.Fatalf("expected type 'float', got %v", l.Type)
			}
			return
		}
	}
	t.Fatalf("expected 'f' in locals, got %+v", ctx.Locals)
}

func TestParseCompletionCtx_VarBooleanLiteral(t *testing.T) {
	src := []byte(`package com.example;
public class MyClass {
    public void doSomething() {
        var flag = true;
        flag.
    }
}`)
	ctx := parseCompletionCtx(nil, src, 4, 13)
	for _, l := range ctx.Locals {
		if l.Name == "flag" {
			if l.Type == nil || l.Type.Sym != "boolean" {
				t.Fatalf("expected type 'boolean', got %v", l.Type)
			}
			return
		}
	}
	t.Fatalf("expected 'flag' in locals, got %+v", ctx.Locals)
}

func TestParseCompletionCtx_VarCharLiteral(t *testing.T) {
	src := []byte(`package com.example;
public class MyClass {
    public void doSomething() {
        var ch = 'a';
        ch.
    }
}`)
	ctx := parseCompletionCtx(nil, src, 4, 11)
	for _, l := range ctx.Locals {
		if l.Name == "ch" {
			if l.Type == nil || l.Type.Sym != "char" {
				t.Fatalf("expected type 'char', got %v", l.Type)
			}
			return
		}
	}
	t.Fatalf("expected 'ch' in locals, got %+v", ctx.Locals)
}

func TestParseCompletionCtx_VarArrayCreation(t *testing.T) {
	src := []byte(`package com.example;
public class MyClass {
    public void doSomething() {
        var arr = new int[]{1, 2, 3};
        arr.
    }
}`)
	ctx := parseCompletionCtx(nil, src, 4, 12)
	for _, l := range ctx.Locals {
		if l.Name == "arr" {
			if l.Type == nil || l.Type.Sym != "int" {
				t.Fatalf("expected type 'int', got %v", l.Type)
			}
			return
		}
	}
	t.Fatalf("expected 'arr' in locals, got %+v", ctx.Locals)
}

func TestParseCompletionCtx_VarArrayCreationObject(t *testing.T) {
	src := []byte(`package com.example;
public class MyClass {
    public void doSomething() {
        var arr = new String[10];
        arr.
    }
}`)
	ctx := parseCompletionCtx(nil, src, 4, 12)
	for _, l := range ctx.Locals {
		if l.Name == "arr" {
			if l.Type == nil || l.Type.Sym != "String" {
				t.Fatalf("expected type 'String', got %v", l.Type)
			}
			return
		}
	}
	t.Fatalf("expected 'arr' in locals, got %+v", ctx.Locals)
}

func TestParseCompletionCtx_VarChainedMethodInvocation(t *testing.T) {
	src := []byte(`package com.example;
public class MyClass {
    public void doSomething() {
        var result = builder.name("a").build();
        result.
    }
}`)
	ctx := parseCompletionCtx(nil, src, 4, 15)
	for _, l := range ctx.Locals {
		if l.Name == "result" {
			if l.Type != nil {
				t.Fatalf("expected nil type for chained call var, got %v", l.Type)
			}
			if l.Initializer == nil {
				t.Fatal("expected Initializer to be set for chained call var")
			}
			if l.Initializer.Receiver != "builder.name" {
				t.Fatalf("expected receiver 'builder.name', got %q", l.Initializer.Receiver)
			}
			if l.Initializer.MethodName != "build" {
				t.Fatalf("expected method 'build', got %q", l.Initializer.MethodName)
			}
			return
		}
	}
	t.Fatalf("expected 'result' in locals, got %+v", ctx.Locals)
}

func TestParseCompletionCtx_VarInstanceMethodInvocation(t *testing.T) {
	src := []byte(`package com.example;
public class MyClass {
    public void doSomething() {
        var item = list.get(0);
        item.
    }
}`)
	ctx := parseCompletionCtx(nil, src, 4, 13)
	for _, l := range ctx.Locals {
		if l.Name == "item" {
			if l.Type != nil {
				t.Fatalf("expected nil type for instance call var, got %v", l.Type)
			}
			if l.Initializer == nil {
				t.Fatal("expected Initializer to be set for instance call var")
			}
			if l.Initializer.Receiver != "list" {
				t.Fatalf("expected receiver 'list', got %q", l.Initializer.Receiver)
			}
			if l.Initializer.MethodName != "get" {
				t.Fatalf("expected method 'get', got %q", l.Initializer.MethodName)
			}
			return
		}
	}
	t.Fatalf("expected 'item' in locals, got %+v", ctx.Locals)
}

func TestExtractReceiverFromAST(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		line     int
		char     int
		expected string
	}{
		{
			name: "simple method call",
			src: `class X { void f() {
        items.get(0).
    }}`,
			line: 1, char: 21,
			expected: "items.get",
		},
		{
			name: "chained method then field",
			src: `class X { void f() {
        foo.bar().baz.
    }}`,
			line: 1, char: 22,
			expected: "foo.bar.baz",
		},
		{
			name: "method call with string arg",
			src: `class X { void f() {
        map.get("key").
    }}`,
			line: 1, char: 23,
			expected: "map.get",
		},
		{
			name: "method call with multiple args",
			src: `class X { void f() {
        foo.bar(a, b).
    }}`,
			line: 1, char: 22,
			expected: "foo.bar",
		},
		{
			name: "nested calls",
			src: `class X { void f() {
        a.b(c.d()).
    }}`,
			line: 1, char: 19,
			expected: "a.b",
		},
		{
			name: "simple identifier",
			src: `class X { void f() {
        items.
    }}`,
			line: 1, char: 14,
			expected: "items",
		},
		{
			name: "chained fields",
			src: `class X { void f() {
        foo.bar.baz.
    }}`,
			line: 1, char: 20,
			expected: "foo.bar.baz",
		},
		{
			name: "string with dots and parens",
			src: `class X { void f() {
        "a.b(c)".length().
    }}`,
			line: 1, char: 26, // right after the trailing dot at char 25
			expected: "\"a.b(c)\".length",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := []byte(tt.src)
			ctx := parseCompletionCtx(nil, content, tt.line, tt.char)
			if ctx.Kind != CompletionDot {
				t.Fatalf("expected CompletionDot, got %d", ctx.Kind)
			}
			if ctx.Receiver != tt.expected {
				t.Errorf("receiver = %q, want %q", ctx.Receiver, tt.expected)
			}
		})
	}
}

func TestParseCompletionCtx_CallContext_MethodInvocation(t *testing.T) {
	src := []byte(`package com.example;

public class MyClass {
    public void test() {
        String name = "hello";
        int count = 42;
        obj.doWork(na)
    }
}`)
	// Cursor at "na" inside doWork() - line 6, character 20
	ctx := parseCompletionCtx(nil, src, 6, 20)
	if ctx.Call == nil {
		t.Fatal("expected CallContext to be non-nil")
	}
	if ctx.Call.Receiver != "obj" {
		t.Errorf("CallContext.Receiver = %q, want %q", ctx.Call.Receiver, "obj")
	}
	if ctx.Call.MethodName != "doWork" {
		t.Errorf("CallContext.MethodName = %q, want %q", ctx.Call.MethodName, "doWork")
	}
	if ctx.Call.ParamIndex != 0 {
		t.Errorf("CallContext.ParamIndex = %d, want 0", ctx.Call.ParamIndex)
	}
	if ctx.Call.IsNewExpr {
		t.Error("expected IsNewExpr to be false")
	}
}

func TestParseCompletionCtx_CallContext_SecondParam(t *testing.T) {
	src := []byte(`package com.example;

public class MyClass {
    public void test() {
        String name = "hello";
        foo("first", na)
    }
}`)
	// Cursor at "na" as second param - line 5, character 24
	ctx := parseCompletionCtx(nil, src, 5, 24)
	if ctx.Call == nil {
		t.Fatal("expected CallContext to be non-nil")
	}
	if ctx.Call.MethodName != "foo" {
		t.Errorf("CallContext.MethodName = %q, want %q", ctx.Call.MethodName, "foo")
	}
	if ctx.Call.ParamIndex != 1 {
		t.Errorf("CallContext.ParamIndex = %d, want 1", ctx.Call.ParamIndex)
	}
}

func TestParseCompletionCtx_CallContext_NewExpression(t *testing.T) {
	src := []byte(`package com.example;

public class MyClass {
    public void test() {
        String name = "hello";
        new ArrayList(na)
    }
}`)
	// Cursor at "na" inside new ArrayList() - line 5, character 24
	ctx := parseCompletionCtx(nil, src, 5, 24)
	if ctx.Call == nil {
		t.Fatal("expected CallContext to be non-nil")
	}
	if !ctx.Call.IsNewExpr {
		t.Error("expected IsNewExpr to be true")
	}
	if ctx.Call.Constructor != "ArrayList" {
		t.Errorf("CallContext.Constructor = %q, want %q", ctx.Call.Constructor, "ArrayList")
	}
}

func TestParseCompletionCtx_CallContext_NoCallContext(t *testing.T) {
	src := []byte(`package com.example;

public class MyClass {
    public void test() {
        String na
    }
}`)
	// Cursor at "na" outside any call - line 4, character 17
	ctx := parseCompletionCtx(nil, src, 4, 17)
	if ctx.Call != nil {
		t.Errorf("expected CallContext to be nil, got %+v", ctx.Call)
	}
}
