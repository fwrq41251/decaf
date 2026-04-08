package lsp

import (
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
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
	item := methodCompletionItem("doWork", CompletionKindMethod, sig, "", "", false)
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
	item2 := methodCompletionItem("getCount", CompletionKindMethod, sigNoParams, "", "", false)
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
	item3 := methodCompletionItem("value", CompletionKindField, nil, "", "", false)
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
	item := methodCompletionItem("doWork", CompletionKindMethod, sig, "", "", false)
	if item.InsertText != "doWork($1)$0" {
		t.Errorf("fallback snippet InsertText = %q, want %q", item.InsertText, "doWork($1)$0")
	}
}

func TestMethodCompletionItem_ParenFollows(t *testing.T) {
	// When parens already follow, InsertText should be just the name (no parens, no snippet).
	sig := &index.SignatureInfo{
		Label:     "void doWork(String name)",
		HasParams: true,
		Params: []index.ParamInfo{
			{Name: "name", Type: "String"},
		},
	}
	item := methodCompletionItem("doWork", CompletionKindMethod, sig, "", "", true)
	if item.InsertText != "doWork" {
		t.Errorf("parenFollows: InsertText = %q, want %q", item.InsertText, "doWork")
	}
	if item.InsertTextFormat != 0 {
		t.Errorf("parenFollows: InsertTextFormat = %d, want 0", item.InsertTextFormat)
	}
	// Label should still show the full signature.
	if item.Label != "doWork(String name)" {
		t.Errorf("parenFollows: Label = %q, want %q", item.Label, "doWork(String name)")
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

func TestReturnTypeFromMethodLabel(t *testing.T) {
	tests := []struct {
		label string
		want  string
	}{
		{"String getName()", "String"},
		{"Map<String, Integer> index()", "Map<String, Integer>"},
		{"void work(String name)", "void"},
		{"broken", ""},
	}
	for _, tt := range tests {
		got := returnTypeFromMethodLabel(tt.label)
		if got != tt.want {
			t.Errorf("returnTypeFromMethodLabel(%q) = %q, want %q", tt.label, got, tt.want)
		}
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

func TestTypeExprMatchesExpected(t *testing.T) {
	tests := []struct {
		name      string
		candidate *index.TypeExpr
		expected  *index.TypeExpr
		want      bool
	}{
		{"exact match", te("java/lang/String#"), te("java/lang/String#"), true},
		{"different types", te("java/lang/String#"), te("java/lang/Integer#"), false},
		{"simple name match", te("java/lang/String#"), te("com/example/String#"), true},
		{"nil candidate", nil, te("java/lang/String#"), false},
		{"nil expected", te("java/lang/String#"), nil, false},
		{"same generic", teArgs("java/util/List#", te("java/lang/String#")), teArgs("java/util/List#", te("java/lang/String#")), true},
		{"different generic args", teArgs("java/util/List#", te("java/lang/String#")), teArgs("java/util/List#", te("java/lang/Integer#")), true},
		{"base match no args", te("java/util/List#"), teArgs("java/util/List#", te("java/lang/String#")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := typeExprMatchesExpected(tt.candidate, tt.expected)
			if got != tt.want {
				t.Errorf("typeExprMatchesExpected(%v, %v) = %v, want %v", tt.candidate, tt.expected, got, tt.want)
			}
		})
	}
}

func te(sym string) *index.TypeExpr {
	return &index.TypeExpr{Sym: sym}
}

func teArgs(sym string, args ...*index.TypeExpr) *index.TypeExpr {
	return &index.TypeExpr{Sym: sym, Args: args}
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

	// Test case 3: Class-scope snippets (main) should not appear in ScopeBlock
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

func TestCompleteDot_InferredLambdaParam(t *testing.T) {
	h, idx, tmpDir := newTestHandler(t)
	defer idx.Close()

	loadSDB(t, tmpDir, idx, &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Types.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "pkg/Box#", DisplayName: "Box", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "pkg/Container#", DisplayName: "Container", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "pkg/Stream#", DisplayName: "Stream", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "pkg/Function#", DisplayName: "Function", Kind: sdb.SymbolInformation_INTERFACE},
			},
		}},
	})

	setIndexField(t, idx, "classTypeParams", map[string][]string{
		"pkg/Container#": {"pkg/Container#[T]"},
		"pkg/Stream#":    {"pkg/Stream#[T]"},
		"pkg/Function#":  {"pkg/Function#[T]", "pkg/Function#[R]"},
	})
	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"pkg/Box#": {
			{Name: "length", Symbol: "pkg/Box#length.", Kind: sdb.SymbolInformation_METHOD},
		},
		"pkg/Container#": {
			{Name: "stream", Symbol: "pkg/Container#stream().", Kind: sdb.SymbolInformation_METHOD},
		},
		"pkg/Stream#": {
			{
				Name:   "map",
				Symbol: "pkg/Stream#map().",
				Kind:   sdb.SymbolInformation_METHOD,
				Signature: &index.SignatureInfo{
					Label:     "Stream<R> map(Function<T, R> mapper)",
					HasParams: true,
					Params: []index.ParamInfo{
						{Name: "mapper", Type: "Function<T, R>", TypeSym: "pkg/Function#"},
					},
				},
			},
		},
	})
	setIndexField(t, idx, "symbolDeclType", map[string]*index.TypeExpr{
		"pkg/Container#stream().": {
			Sym:  "pkg/Stream#",
			Args: []*index.TypeExpr{{Sym: "pkg/Container#[T]"}},
		},
	})

	cctx := &CompletionCtx{
		Kind:         CompletionDot,
		Receiver:     "a",
		Prefix:       "le",
		Package:      "pkg",
		Locals:       []ValueDecl{{Name: "list", Type: &index.TypeExpr{Sym: "Container", Args: []*index.TypeExpr{{Sym: "Box"}}}}},
		LambdaParams: []ValueDecl{{Name: "a"}},
		Call:         &CallContext{Receiver: "list.stream", MethodName: "map", ParamIndex: 0},
	}

	items := h.completeDot(cctx, "file://"+tmpDir+"/src/Test.java")
	if len(items) != 1 {
		t.Fatalf("expected 1 completion item, got %d: %+v", len(items), items)
	}
	if items[0].Label != "length()" {
		t.Fatalf("expected length() completion, got %+v", items[0])
	}
}

func TestCompleteLexical_IncludesLambdaParams(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()
	items := h.completeLexical(&CompletionCtx{
		Prefix:       "us",
		LambdaParams: []ValueDecl{{Name: "user"}},
	}, "", nil)

	if len(items) != 1 {
		t.Fatalf("expected 1 completion item, got %d: %+v", len(items), items)
	}
	if items[0].Label != "user" {
		t.Fatalf("expected lambda param completion 'user', got %+v", items[0])
	}
}

func TestCompleteLexical_LambdaParamsShadowLocalsAndParams(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()
	items := h.completeLexical(&CompletionCtx{
		Prefix:       "va",
		LambdaParams: []ValueDecl{{Name: "value", Type: &index.TypeExpr{Sym: "String"}}},
		Locals:       []ValueDecl{{Name: "value", Type: &index.TypeExpr{Sym: "int"}}},
		Params:       []ValueDecl{{Name: "value", Type: &index.TypeExpr{Sym: "long"}}},
	}, "", nil)

	if len(items) != 1 {
		t.Fatalf("expected 1 completion item after dedup, got %d: %+v", len(items), items)
	}
	if items[0].Label != "value" {
		t.Fatalf("expected completion 'value', got %+v", items[0])
	}
	if items[0].Detail != "String" {
		t.Fatalf("expected lambda param detail to win shadowing, got %+v", items[0])
	}
}

func TestCompleteLexical_NearestLambdaParamWins(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()
	items := h.completeLexical(&CompletionCtx{
		Prefix:       "it",
		LambdaParams: []ValueDecl{{Name: "item", Type: &index.TypeExpr{Sym: "Outer"}}, {Name: "item", Type: &index.TypeExpr{Sym: "Inner"}}},
	}, "", nil)

	if len(items) != 1 {
		t.Fatalf("expected 1 completion item after dedup, got %d: %+v", len(items), items)
	}
	if items[0].Detail != "Inner" {
		t.Fatalf("expected nearest lambda param to win, got %+v", items[0])
	}
}

func TestCompleteLexical_SemanticCandidatesBeatSnippets(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()

	items := h.completeLexical(&CompletionCtx{
		Scope:       ScopeBlock,
		Prefix:      "so",
		Locals:      []ValueDecl{{Name: "source", Type: &index.TypeExpr{Sym: "String"}}},
		ClassFields: []ValueDecl{{Name: "socket", Type: &index.TypeExpr{Sym: "Socket"}}},
		ClassMethods: []MethodDecl{
			{Name: "solve", Params: []string{}},
		},
	}, "", nil)
	sortCompletionItems(items)

	if len(items) < 4 {
		t.Fatalf("expected semantic items plus snippet, got %d: %+v", len(items), items)
	}
	if items[0].Label != "source" {
		t.Fatalf("expected local variable to rank first, got %+v", items[0])
	}

	snippetIndex := -1
	for i, item := range items {
		if item.Label == "sout" {
			snippetIndex = i
			break
		}
	}
	if snippetIndex < 0 {
		t.Fatalf("expected sout snippet in completion list, got %+v", items)
	}
	if snippetIndex < 3 {
		t.Fatalf("expected snippet to rank below semantic candidates, index=%d items=%+v", snippetIndex, items)
	}
}

func TestCompleteLexical_ExpectedTypeBoostsClassMethods(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()

	setIndexField(t, idx, "typeBySimpleName", map[string][]*index.Symbol{
		"test": {
			{Name: "Test", Symbol: "pkg/Test#", Kind: sdb.SymbolInformation_CLASS},
		},
	})
	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"pkg/Test#": {
			{
				Name:   "setValue",
				Symbol: "pkg/Test#setValue().",
				Kind:   sdb.SymbolInformation_METHOD,
				Signature: &index.SignatureInfo{
					Label:     "void setValue(String value)",
					HasParams: true,
					Params: []index.ParamInfo{
						{Name: "value", Type: "String"},
					},
				},
			},
		},
	})

	items := h.completeLexical(&CompletionCtx{
		Scope:          ScopeBlock,
		Prefix:         "ge",
		Package:        "pkg",
		EnclosingClass: "Test",
		Call:           &CallContext{MethodName: "setValue", ParamIndex: 0},
		ClassMethods: []MethodDecl{
			{Name: "getInt", Params: nil, ReturnType: &index.TypeExpr{Sym: "int"}},
			{Name: "getString", Params: nil, ReturnType: &index.TypeExpr{Sym: "String"}},
		},
	}, "", nil)
	sortCompletionItems(items)

	if len(items) < 2 {
		t.Fatalf("expected class method completions, got %d: %+v", len(items), items)
	}
	if items[0].Label != "getString()" {
		t.Fatalf("expected String-returning method to rank first, got %+v", items[0])
	}
}

func TestCompleteDot_FallbackUsesClassContextAndObjectMethods(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()

	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"java/lang/Object#": {
			{Name: "hashCode", Symbol: "java/lang/Object#hashCode().", Kind: sdb.SymbolInformation_METHOD},
			{Name: "toString", Symbol: "java/lang/Object#toString().", Kind: sdb.SymbolInformation_METHOD},
			{Name: "wait", Symbol: "java/lang/Object#wait().", Kind: sdb.SymbolInformation_METHOD},
		},
	})

	items := h.completeDot(&CompletionCtx{
		Kind:        CompletionDot,
		Receiver:    "unknown",
		Prefix:      "ha",
		ClassFields: []ValueDecl{{Name: "handler", Type: &index.TypeExpr{Sym: "Handler"}}},
		ClassMethods: []MethodDecl{
			{Name: "handle", Params: []string{"value"}},
		},
	}, "")

	if len(items) < 2 {
		t.Fatalf("expected fallback completion items, got %d: %+v", len(items), items)
	}
	if items[0].Label != "handler" {
		t.Fatalf("expected class field fallback first, got %+v", items[0])
	}

	foundHandle := false
	foundHashCode := false
	for _, item := range items {
		if item.Label == "handle(value)" {
			foundHandle = true
		}
		if item.Label == "hashCode()" {
			foundHashCode = true
		}
	}
	if !foundHandle {
		t.Fatalf("expected class method fallback item, got %+v", items)
	}
	if !foundHashCode {
		t.Fatalf("expected Object method fallback item, got %+v", items)
	}
}

func TestCompleteDot_FallbackStaysEmptyWhenNothingMatches(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()

	items := h.completeDot(&CompletionCtx{
		Kind:     CompletionDot,
		Receiver: "unknown",
		Prefix:   "zzz",
	}, "")

	if len(items) != 0 {
		t.Fatalf("expected no fallback items for unmatched prefix, got %+v", items)
	}
}

func TestCompleteDot_CustomSAMLambdaParam(t *testing.T) {
	h, idx, tmpDir := newTestHandler(t)
	defer idx.Close()

	loadSDB(t, tmpDir, idx, &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Types.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "pkg/Box#", DisplayName: "Box", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "pkg/Processor#", DisplayName: "Processor", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "pkg/Mapper#", DisplayName: "Mapper", Kind: sdb.SymbolInformation_INTERFACE},
			},
		}},
	})

	setIndexField(t, idx, "classTypeParams", map[string][]string{
		"pkg/Processor#": {"pkg/Processor#[T]"},
		"pkg/Mapper#":    {"pkg/Mapper#[T]", "pkg/Mapper#[R]"},
	})
	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"pkg/Box#": {
			{Name: "length", Symbol: "pkg/Box#length().", Kind: sdb.SymbolInformation_METHOD},
		},
		"pkg/Processor#": {
			{
				Name:   "map",
				Symbol: "pkg/Processor#map().",
				Kind:   sdb.SymbolInformation_METHOD,
				Signature: &index.SignatureInfo{
					Label:     "String map(Mapper<T, String> mapper)",
					HasParams: true,
					Params: []index.ParamInfo{
						{Name: "mapper", Type: "Mapper<T, String>", TypeSym: "pkg/Mapper#"},
					},
				},
			},
		},
		"pkg/Mapper#": {
			{
				Name:   "apply",
				Symbol: "pkg/Mapper#apply().",
				Kind:   sdb.SymbolInformation_METHOD,
				Signature: &index.SignatureInfo{
					Label:     "R apply(T value)",
					HasParams: true,
					Params: []index.ParamInfo{
						{Name: "value", Type: "T"},
					},
				},
			},
		},
	})

	cctx := &CompletionCtx{
		Kind:         CompletionDot,
		Receiver:     "item",
		Prefix:       "le",
		Package:      "pkg",
		Locals:       []ValueDecl{{Name: "processor", Type: &index.TypeExpr{Sym: "Processor", Args: []*index.TypeExpr{{Sym: "Box"}}}}},
		LambdaParams: []ValueDecl{{Name: "item"}},
		Call:         &CallContext{Receiver: "processor", MethodName: "map", ParamIndex: 0},
	}

	items := h.completeDot(cctx, "file://"+tmpDir+"/src/Test.java")
	if len(items) != 1 {
		t.Fatalf("expected 1 completion item, got %d: %+v", len(items), items)
	}
	if items[0].Label != "length()" {
		t.Fatalf("expected custom SAM lambda completion, got %+v", items[0])
	}
}

func TestCompleteDot_ListOfStringLambdaParam(t *testing.T) {
	h, idx, tmpDir := newTestHandler(t)
	defer idx.Close()

	loadSDB(t, tmpDir, idx, &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Types.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "java/lang/String#", DisplayName: "String", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "java/util/List#", DisplayName: "List", Kind: sdb.SymbolInformation_INTERFACE},
				{Symbol: "java/util/stream/Stream#", DisplayName: "Stream", Kind: sdb.SymbolInformation_INTERFACE},
				{Symbol: "java/util/function/Function#", DisplayName: "Function", Kind: sdb.SymbolInformation_INTERFACE},
			},
		}},
	})

	setIndexField(t, idx, "classTypeParams", map[string][]string{
		"java/util/List#":              {"java/util/List#[E]"},
		"java/util/stream/Stream#":     {"java/util/stream/Stream#[T]"},
		"java/util/function/Function#": {"java/util/function/Function#[T]", "java/util/function/Function#[R]"},
	})
	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"java/lang/String#": {
			{Name: "toLowerCase", Symbol: "java/lang/String#toLowerCase().", Kind: sdb.SymbolInformation_METHOD},
		},
		"java/util/List#": {
			{Name: "of", Symbol: "java/util/List#of().", Kind: sdb.SymbolInformation_METHOD, IsStatic: true},
			{Name: "stream", Symbol: "java/util/List#stream().", Kind: sdb.SymbolInformation_METHOD},
		},
		"java/util/stream/Stream#": {
			{
				Name:   "map",
				Symbol: "java/util/stream/Stream#map().",
				Kind:   sdb.SymbolInformation_METHOD,
				Signature: &index.SignatureInfo{
					Label:     "Stream<R> map(Function<T, R> mapper)",
					HasParams: true,
					Params: []index.ParamInfo{
						{Name: "mapper", Type: "Function<T, R>", TypeSym: "java/util/function/Function#"},
					},
				},
			},
		},
	})
	setIndexField(t, idx, "symbolDeclType", map[string]*index.TypeExpr{
		"java/util/List#of().": {
			Sym:  "java/util/List#",
			Args: []*index.TypeExpr{{Sym: "java/util/List#[E]"}},
		},
		"java/util/List#stream().": {
			Sym:  "java/util/stream/Stream#",
			Args: []*index.TypeExpr{{Sym: "java/util/List#[E]"}},
		},
	})
	setIndexField(t, idx, "symbolDeclParamTypes", map[string][]*index.TypeExpr{
		"java/util/List#of().": {{Sym: "java/util/List#[E]"}},
	})
	setIndexField(t, idx, "typeBySimpleName", map[string][]*index.Symbol{
		"string": {{Name: "String", Symbol: "java/lang/String#", Kind: sdb.SymbolInformation_CLASS}},
		"list":   {{Name: "List", Symbol: "java/util/List#", Kind: sdb.SymbolInformation_INTERFACE}},
	})

	cctx := &CompletionCtx{
		Kind:     CompletionDot,
		Receiver: "a",
		Prefix:   "tol",
		Locals: []ValueDecl{{
			Name: "list",
			Initializer: &VarInitializer{
				Receiver:   "List",
				MethodName: "of",
				ArgTypes:   []*index.TypeExpr{{Sym: "String"}},
			},
		}},
		LambdaParams: []ValueDecl{{Name: "a"}},
		Call:         &CallContext{Receiver: "list.stream", MethodName: "map", ParamIndex: 0},
	}

	items := h.completeDot(cctx, "file://"+tmpDir+"/src/Test.java")
	if len(items) != 1 {
		t.Fatalf("expected 1 completion item, got %d: %+v", len(items), items)
	}
	if items[0].Label != "toLowerCase()" {
		t.Fatalf("expected String lambda completion from List.of inference, got %+v", items[0])
	}
}

func TestResolveVarInitializer_UsesOwnerMethodForInference(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()

	setIndexField(t, idx, "typeBySimpleName", map[string][]*index.Symbol{
		"collections": {{Name: "Collections", Symbol: "java/util/Collections#", Kind: sdb.SymbolInformation_CLASS}},
		"list":        {{Name: "List", Symbol: "java/util/List#", Kind: sdb.SymbolInformation_INTERFACE}},
		"string":      {{Name: "String", Symbol: "java/lang/String#", Kind: sdb.SymbolInformation_CLASS}},
	})
	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"java/util/Collections#": {
			{
				Name:     "unmodifiableList",
				Symbol:   "java/util/Collections#unmodifiableList().",
				Kind:     sdb.SymbolInformation_METHOD,
				IsStatic: true,
				Signature: &index.SignatureInfo{
					Label:         "List<T> unmodifiableList(List<? extends T> list)",
					ReturnTypeSym: "java/util/List#",
					HasParams:     true,
				},
			},
		},
	})
	setIndexField(t, idx, "symbolDeclType", map[string]*index.TypeExpr{
		"java/util/Collections#unmodifiableList().": {
			Sym:  "java/util/List#",
			Args: []*index.TypeExpr{{Sym: "T"}},
		},
	})
	setIndexField(t, idx, "symbolDeclParamTypes", map[string][]*index.TypeExpr{
		"java/util/Collections#unmodifiableList().": {{
			Sym:  "java/util/List#",
			Args: []*index.TypeExpr{{Sym: "T"}},
		}},
	})

	resolver := &typeResolver{idx: idx}
	got := h.resolveVarInitializer(&VarInitializer{
		Receiver:   "Collections",
		MethodName: "unmodifiableList",
		ArgTypes: []*index.TypeExpr{{
			Sym:  "java/util/List#",
			Args: []*index.TypeExpr{{Sym: "java/lang/String#"}},
		}},
	}, &CompletionCtx{}, resolver)

	if got == nil {
		t.Fatal("expected inferred type, got nil")
	}
	if got.Sym != "java/util/List#" || len(got.Args) != 1 || got.Args[0].Sym != "java/lang/String#" {
		t.Fatalf("expected List<String>, got %+v", got)
	}
}

func TestResolveIdentifierTypeExpr_UnqualifiedVarInitializerMethod(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()

	setIndexField(t, idx, "typeBySimpleName", map[string][]*index.Symbol{
		"test":   {{Name: "Test", Symbol: "com/example/Test#", Kind: sdb.SymbolInformation_CLASS}},
		"list":   {{Name: "List", Symbol: "java/util/List#", Kind: sdb.SymbolInformation_INTERFACE}},
		"string": {{Name: "String", Symbol: "java/lang/String#", Kind: sdb.SymbolInformation_CLASS}},
	})
	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"com/example/Test#": {
			{
				Name:   "createList",
				Symbol: "com/example/Test#createList().",
				Kind:   sdb.SymbolInformation_METHOD,
				Signature: &index.SignatureInfo{
					Label:         "List<String> createList(String name)",
					ReturnTypeSym: "java/util/List#",
					HasParams:     true,
					Params:        []index.ParamInfo{{Name: "name", Type: "String", TypeSym: "java/lang/String#"}},
				},
			},
		},
	})
	setIndexField(t, idx, "symbolDeclType", map[string]*index.TypeExpr{
		"com/example/Test#createList().": {
			Sym:  "java/util/List#",
			Args: []*index.TypeExpr{{Sym: "java/lang/String#"}},
		},
	})

	resolver := &typeResolver{idx: idx}
	typeExpr, staticAccess := h.resolveIdentifierTypeExpr("list", &CompletionCtx{
		EnclosingClass: "Test",
		Params:         []ValueDecl{{Name: "name", Type: &index.TypeExpr{Sym: "String"}}},
		Locals: []ValueDecl{{
			Name: "list",
			Initializer: &VarInitializer{
				MethodName: "createList",
				ArgTypes:   []*index.TypeExpr{{Sym: "String"}},
			},
		}},
	}, resolver)

	if staticAccess {
		t.Fatal("expected local var binding, got static access")
	}
	if typeExpr == nil || typeExpr.Sym != "java/util/List#" || len(typeExpr.Args) != 1 || typeExpr.Args[0].Sym != "java/lang/String#" {
		t.Fatalf("expected List<String> from unqualified method var inference, got %+v", typeExpr)
	}
}

func TestResolveCurrentArgumentTypeExpr_PrefersMatchingOverload(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()

	setIndexField(t, idx, "typeBySimpleName", map[string][]*index.Symbol{
		"foo":     {{Name: "Foo", Symbol: "com/example/Foo#", Kind: sdb.SymbolInformation_CLASS}},
		"integer": {{Name: "Integer", Symbol: "java/lang/Integer#", Kind: sdb.SymbolInformation_CLASS}},
		"string":  {{Name: "String", Symbol: "java/lang/String#", Kind: sdb.SymbolInformation_CLASS}},
	})
	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"com/example/Foo#": {
			{
				Name:   "call",
				Symbol: "com/example/Foo#call(+1).",
				Kind:   sdb.SymbolInformation_METHOD,
				Signature: &index.SignatureInfo{
					Label:         "void call(Integer value)",
					HasParams:     true,
					Params:        []index.ParamInfo{{Name: "value", Type: "Integer", TypeSym: "java/lang/Integer#"}},
					ReturnTypeSym: "void",
				},
			},
			{
				Name:   "call",
				Symbol: "com/example/Foo#call(+2).",
				Kind:   sdb.SymbolInformation_METHOD,
				Signature: &index.SignatureInfo{
					Label:         "void call(String value, int n)",
					HasParams:     true,
					Params:        []index.ParamInfo{{Name: "value", Type: "String", TypeSym: "java/lang/String#"}, {Name: "n", Type: "int"}},
					ReturnTypeSym: "void",
				},
			},
		},
	})

	resolver := &typeResolver{idx: idx}
	got := h.resolveCurrentArgumentTypeExpr(&CompletionCtx{
		Receiver: "foo",
		Locals:   []ValueDecl{{Name: "foo", Type: &index.TypeExpr{Sym: "com/example/Foo#"}}},
		Call:     &CallContext{Receiver: "foo", MethodName: "call", ParamIndex: 0},
	}, resolver)

	if got == nil || got.Sym != "java/lang/Integer#" {
		t.Fatalf("expected Integer overload to win for single-arg call, got %+v", got)
	}
}

func TestResolveCurrentArgumentTypeExpr_FallbackToParsedLabel(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()

	setIndexField(t, idx, "typeBySimpleName", map[string][]*index.Symbol{
		"foo":    {{Name: "Foo", Symbol: "com/example/Foo#", Kind: sdb.SymbolInformation_CLASS}},
		"string": {{Name: "String", Symbol: "java/lang/String#", Kind: sdb.SymbolInformation_CLASS}},
	})
	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"com/example/Foo#": {
			{
				Name:   "setValue",
				Symbol: "com/example/Foo#setValue().",
				Kind:   sdb.SymbolInformation_METHOD,
				Signature: &index.SignatureInfo{
					Label:     "void setValue(String value)",
					HasParams: true,
				},
			},
		},
	})

	resolver := &typeResolver{idx: idx}
	got := h.resolveCurrentArgumentTypeExpr(&CompletionCtx{
		Receiver: "foo",
		Locals:   []ValueDecl{{Name: "foo", Type: &index.TypeExpr{Sym: "com/example/Foo#"}}},
		Call:     &CallContext{Receiver: "foo", MethodName: "setValue", ParamIndex: 0},
	}, resolver)

	if got == nil || got.Sym != "java/lang/String#" {
		t.Fatalf("expected fallback ParseParams type resolution, got %+v", got)
	}
}

func TestResolveIdentifierTypeExpr_LambdaShadowBeatsLocal(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()

	setIndexField(t, idx, "typeBySimpleName", map[string][]*index.Symbol{
		"string":  {{Name: "String", Symbol: "java/lang/String#", Kind: sdb.SymbolInformation_CLASS}},
		"integer": {{Name: "Integer", Symbol: "java/lang/Integer#", Kind: sdb.SymbolInformation_CLASS}},
	})

	resolver := &typeResolver{idx: idx}
	typeExpr, staticAccess := h.resolveIdentifierTypeExpr("value", &CompletionCtx{
		LambdaParams: []ValueDecl{{Name: "value", Type: &index.TypeExpr{Sym: "String"}}},
		Locals:       []ValueDecl{{Name: "value", Type: &index.TypeExpr{Sym: "Integer"}}},
	}, resolver)

	if staticAccess {
		t.Fatalf("expected instance binding, got static")
	}
	if typeExpr == nil || typeExpr.Sym != "java/lang/String#" {
		t.Fatalf("expected lambda param type to win, got %+v", typeExpr)
	}
}

func TestCompleteLexical_OuterLambdaCaptureVisible(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()

	items := h.completeLexical(&CompletionCtx{
		Prefix:       "ou",
		LambdaParams: []ValueDecl{{Name: "outer"}, {Name: "inner"}},
	}, "", nil)

	if len(items) != 1 {
		t.Fatalf("expected outer lambda capture completion, got %d: %+v", len(items), items)
	}
	if items[0].Label != "outer" {
		t.Fatalf("expected outer lambda capture candidate, got %+v", items[0])
	}
}

func TestResolveStaticMemberType_Structured(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()

	// Mock data: a class with a static method that returns another class.
	// We deliberately use a misleading Label "WrongType assertThat(Object actual)"
	// but set ReturnTypeSym to the correct "org/assertj/core/api/AbstractAssert#".
	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"org/assertj/core/api/Assertions#": {
			{
				Name:     "assertThat",
				Symbol:   "org/assertj/core/api/Assertions#assertThat().",
				Kind:     sdb.SymbolInformation_METHOD,
				IsStatic: true,
				Signature: &index.SignatureInfo{
					Label:         "WrongType assertThat(Object actual)",
					ReturnTypeSym: "org/assertj/core/api/AbstractAssert#",
					HasParams:     true,
				},
			},
		},
		"org/assertj/core/api/AbstractAssert#": {
			{
				Name:   "isNotNull",
				Symbol: "org/assertj/core/api/AbstractAssert#isNotNull().",
				Kind:   sdb.SymbolInformation_METHOD,
			},
		},
	})

	resolver := &typeResolver{idx: h.idx}
	te := h.resolveStaticMemberType("org/assertj/core/api/Assertions#", "assertThat", resolver)

	if te == nil {
		t.Fatal("failed to resolve static member type")
	}
	if te.Sym != "org/assertj/core/api/AbstractAssert#" {
		t.Errorf("resolved type = %q, want %q (should use ReturnTypeSym, not Label)", te.Sym, "org/assertj/core/api/AbstractAssert#")
	}
}

func TestCompleteLexical_StaticImport(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()

	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"org/assertj/core/api/Assertions#": {
			{
				Name:     "assertThat",
				Symbol:   "org/assertj/core/api/Assertions#assertThat().",
				Kind:     sdb.SymbolInformation_METHOD,
				IsStatic: true,
				Signature: &index.SignatureInfo{
					Label:     "AbstractAssert assertThat(Object actual)",
					HasParams: true,
					Params:    []index.ParamInfo{{Name: "actual", Type: "Object"}},
				},
			},
			{
				Name:     "assertThatCode",
				Symbol:   "org/assertj/core/api/Assertions#assertThatCode().",
				Kind:     sdb.SymbolInformation_METHOD,
				IsStatic: true,
			},
			{
				Name:   "instanceMethod",
				Symbol: "org/assertj/core/api/Assertions#instanceMethod().",
				Kind:   sdb.SymbolInformation_METHOD,
			},
		},
	})

	items := h.completeLexical(&CompletionCtx{
		Prefix: "assert",
		Imports: []ImportSpec{
			{Path: "org.assertj.core.api.Assertions.assertThat", Static: true},
		},
	}, "", nil)

	found := false
	for _, item := range items {
		if item.FilterText == "assertThat" {
			found = true
			break
		}
		// Instance methods should not appear.
		if item.FilterText == "instanceMethod" {
			t.Fatal("instance method should not appear from static import")
		}
	}
	if !found {
		t.Fatalf("expected assertThat from static import, got %+v", items)
	}
}

func TestCompleteLexical_WildcardStaticImport(t *testing.T) {
	h, idx, _ := newTestHandler(t)
	defer idx.Close()

	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"org/junit/jupiter/api/Assertions#": {
			{
				Name:     "assertEquals",
				Symbol:   "org/junit/jupiter/api/Assertions#assertEquals().",
				Kind:     sdb.SymbolInformation_METHOD,
				IsStatic: true,
				Signature: &index.SignatureInfo{
					Label:     "void assertEquals(Object expected, Object actual)",
					HasParams: true,
					Params: []index.ParamInfo{
						{Name: "expected", Type: "Object"},
						{Name: "actual", Type: "Object"},
					},
				},
			},
			{
				Name:     "assertTrue",
				Symbol:   "org/junit/jupiter/api/Assertions#assertTrue().",
				Kind:     sdb.SymbolInformation_METHOD,
				IsStatic: true,
			},
			{
				Name:   "notStatic",
				Symbol: "org/junit/jupiter/api/Assertions#notStatic().",
				Kind:   sdb.SymbolInformation_METHOD,
			},
		},
	})

	items := h.completeLexical(&CompletionCtx{
		Prefix: "assert",
		Imports: []ImportSpec{
			{Path: "org.junit.jupiter.api.Assertions.*", Static: true, Wildcard: true},
		},
	}, "", nil)

	foundEquals := false
	foundTrue := false
	for _, item := range items {
		if item.FilterText == "assertEquals" {
			foundEquals = true
		}
		if item.FilterText == "assertTrue" {
			foundTrue = true
		}
		if item.FilterText == "notStatic" {
			t.Fatal("instance method should not appear from wildcard static import")
		}
	}
	if !foundEquals {
		t.Fatalf("expected assertEquals from wildcard static import, got %+v", items)
	}
	if !foundTrue {
		t.Fatalf("expected assertTrue from wildcard static import, got %+v", items)
	}
}

func TestCompleteDot_StaticImportChainedCall(t *testing.T) {
	h, idx, tmpDir := newTestHandler(t)
	defer idx.Close()

	loadSDB(t, tmpDir, idx, &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Types.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "org/assertj/core/api/Assertions#", DisplayName: "Assertions", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "org/assertj/core/api/AbstractAssert#", DisplayName: "AbstractAssert", Kind: sdb.SymbolInformation_CLASS},
			},
		}},
	})

	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"org/assertj/core/api/Assertions#": {
			{
				Name:     "assertThat",
				Symbol:   "org/assertj/core/api/Assertions#assertThat().",
				Kind:     sdb.SymbolInformation_METHOD,
				IsStatic: true,
				Signature: &index.SignatureInfo{
					Label:     "AbstractAssert assertThat(Object actual)",
					HasParams: true,
					Params:    []index.ParamInfo{{Name: "actual", Type: "Object"}},
				},
			},
		},
		"org/assertj/core/api/AbstractAssert#": {
			{
				Name:   "isEqualTo",
				Symbol: "org/assertj/core/api/AbstractAssert#isEqualTo().",
				Kind:   sdb.SymbolInformation_METHOD,
				Signature: &index.SignatureInfo{
					Label:     "AbstractAssert isEqualTo(Object expected)",
					HasParams: true,
					Params:    []index.ParamInfo{{Name: "expected", Type: "Object"}},
				},
			},
			{
				Name:   "isNotNull",
				Symbol: "org/assertj/core/api/AbstractAssert#isNotNull().",
				Kind:   sdb.SymbolInformation_METHOD,
			},
		},
	})
	setIndexField(t, idx, "symbolDeclType", map[string]*index.TypeExpr{
		"org/assertj/core/api/Assertions#assertThat().": {Sym: "org/assertj/core/api/AbstractAssert#"},
	})

	// Simulate: assertThat(firstAdjustment).is|
	cctx := &CompletionCtx{
		Kind:     CompletionDot,
		Receiver: "assertThat",
		Prefix:   "is",
		Imports: []ImportSpec{
			{Path: "org.assertj.core.api.Assertions.assertThat", Static: true},
		},
	}

	items := h.completeDot(cctx, "file://"+tmpDir+"/src/Test.java")

	foundIsEqualTo := false
	foundIsNotNull := false
	for _, item := range items {
		if item.FilterText == "isEqualTo" {
			foundIsEqualTo = true
		}
		if item.FilterText == "isNotNull" {
			foundIsNotNull = true
		}
	}
	if !foundIsEqualTo {
		t.Fatalf("expected isEqualTo from assertThat() return type, got %+v", items)
	}
	if !foundIsNotNull {
		t.Fatalf("expected isNotNull from assertThat() return type, got %+v", items)
	}
}

func TestCompleteDot_StaticImportChainedCall_SkipsEmptyOverload(t *testing.T) {
	// Simulates real assertThat() scenario: first overload returns AssertDelegateTarget (no members),
	// second overload's DeclTypeOf returns a type parameter, but its Signature.Label has the useful
	// return type. The code should skip overloads with no members and find the right one.
	h, idx, tmpDir := newTestHandler(t)
	defer idx.Close()

	loadSDB(t, tmpDir, idx, &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Types.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "org/assertj/core/api/Assertions#", DisplayName: "Assertions", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "org/assertj/core/api/AssertDelegateTarget#", DisplayName: "AssertDelegateTarget", Kind: sdb.SymbolInformation_INTERFACE},
				{Symbol: "org/assertj/core/api/AbstractAssert#", DisplayName: "AbstractAssert", Kind: sdb.SymbolInformation_CLASS},
			},
		}},
	})

	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"org/assertj/core/api/Assertions#": {
			// Single assertThat entry: TypeOfSymbol returns AssertDelegateTarget (no members),
			// but Signature.Label has AbstractAssert (has members).
			{
				Name:     "assertThat",
				Symbol:   "org/assertj/core/api/Assertions#assertThat().",
				Kind:     sdb.SymbolInformation_METHOD,
				IsStatic: true,
				Signature: &index.SignatureInfo{
					Label:     "AbstractAssert assertThat(Object actual)",
					HasParams: true,
					Params:    []index.ParamInfo{{Name: "actual", Type: "Object"}},
				},
			},
		},
		// AssertDelegateTarget has no members.
		"org/assertj/core/api/AbstractAssert#": {
			{
				Name:   "isEqualTo",
				Symbol: "org/assertj/core/api/AbstractAssert#isEqualTo().",
				Kind:   sdb.SymbolInformation_METHOD,
			},
		},
	})
	setIndexField(t, idx, "symbolType", map[string]string{
		"org/assertj/core/api/Assertions#assertThat().": "org/assertj/core/api/AssertDelegateTarget#",
	})
	setIndexField(t, idx, "typeBySimpleName", map[string][]*index.Symbol{
		"abstractassert": {{Name: "AbstractAssert", Symbol: "org/assertj/core/api/AbstractAssert#", Kind: sdb.SymbolInformation_CLASS}},
	})

	cctx := &CompletionCtx{
		Kind:     CompletionDot,
		Receiver: "assertThat",
		Prefix:   "is",
		Imports: []ImportSpec{
			{Path: "org.assertj.core.api.Assertions.assertThat", Static: true},
		},
	}

	items := h.completeDot(cctx, "file://"+tmpDir+"/src/Test.java")

	found := false
	for _, item := range items {
		if item.FilterText == "isEqualTo" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected isEqualTo after skipping overload with no members, got %+v", items)
	}
}

func TestCompleteDot_WildcardStaticImportChainedCall(t *testing.T) {
	h, idx, tmpDir := newTestHandler(t)
	defer idx.Close()

	loadSDB(t, tmpDir, idx, &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Types.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "org/mockito/Mockito#", DisplayName: "Mockito", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "org/mockito/stubbing/OngoingStubbing#", DisplayName: "OngoingStubbing", Kind: sdb.SymbolInformation_INTERFACE},
			},
		}},
	})

	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"org/mockito/Mockito#": {
			{
				Name:     "when",
				Symbol:   "org/mockito/Mockito#when().",
				Kind:     sdb.SymbolInformation_METHOD,
				IsStatic: true,
			},
		},
		"org/mockito/stubbing/OngoingStubbing#": {
			{
				Name:   "thenReturn",
				Symbol: "org/mockito/stubbing/OngoingStubbing#thenReturn().",
				Kind:   sdb.SymbolInformation_METHOD,
			},
		},
	})
	setIndexField(t, idx, "symbolDeclType", map[string]*index.TypeExpr{
		"org/mockito/Mockito#when().": {Sym: "org/mockito/stubbing/OngoingStubbing#"},
	})

	// Simulate: when(mock.call()).then|
	cctx := &CompletionCtx{
		Kind:     CompletionDot,
		Receiver: "when",
		Prefix:   "then",
		Imports: []ImportSpec{
			{Path: "org.mockito.Mockito.*", Static: true, Wildcard: true},
		},
	}

	items := h.completeDot(cctx, "file://"+tmpDir+"/src/Test.java")

	found := false
	for _, item := range items {
		if item.FilterText == "thenReturn" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected thenReturn from when() return type via wildcard static import, got %+v", items)
	}
}

func setIndexField(t *testing.T, idx *index.Index, field string, value any) {
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

func TestCompleteDot_ExcludesConstructors(t *testing.T) {
	h, idx, tmpDir := newTestHandler(t)
	defer idx.Close()

	loadSDB(t, tmpDir, idx, &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Types.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "pkg/MyClass#", DisplayName: "MyClass", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "pkg/MyClass#<init>.", DisplayName: "MyClass", Kind: sdb.SymbolInformation_CONSTRUCTOR},
			},
		}},
	})

	setIndexField(t, idx, "typeBySimpleName", map[string][]*index.Symbol{
		"myclass": {
			{Name: "MyClass", Symbol: "pkg/MyClass#", Kind: sdb.SymbolInformation_CLASS},
		},
	})

	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"pkg/MyClass#": {
			{Name: "MyClass", Symbol: "pkg/MyClass#<init>.", Kind: sdb.SymbolInformation_CONSTRUCTOR, Signature: &index.SignatureInfo{Label: "MyClass()", HasParams: false}},
			{Name: "doWork", Symbol: "pkg/MyClass#doWork().", Kind: sdb.SymbolInformation_METHOD, Signature: &index.SignatureInfo{Label: "void doWork()", HasParams: false}},
			{Name: "value", Symbol: "pkg/MyClass#value.", Kind: sdb.SymbolInformation_FIELD},
		},
	})

	// Simulate instance access: obj.| where obj is of type MyClass.
	cctx := &CompletionCtx{
		Kind:     CompletionDot,
		Receiver: "obj",
		Prefix:   "",
		Package:  "pkg",
		Locals:   []ValueDecl{{Name: "obj", Type: &index.TypeExpr{Sym: "MyClass"}}},
	}

	items := h.completeDot(cctx, "file://"+tmpDir+"/src/Test.java")

	// Verify no constructors appear in the results
	for _, item := range items {
		if strings.Contains(item.Label, "MyClass(") {
			t.Fatalf("constructor should not appear in member completion: got %q", item.Label)
		}
	}

	// Verify methods and fields still appear
	var hasMethod, hasField bool
	for _, item := range items {
		if item.Label == "doWork()" {
			hasMethod = true
		}
		if item.Label == "value" {
			hasField = true
		}
	}
	if !hasMethod {
		t.Error("expected doWork() to appear in completion items")
	}
	if !hasField {
		t.Error("expected value to appear in completion items")
	}
}

func TestCompleteDot_CaseInsensitivePrefixRanking(t *testing.T) {
	h, idx, tmpDir := newTestHandler(t)
	defer idx.Close()

	loadSDB(t, tmpDir, idx, &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Types.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "pkg/MyClass#", DisplayName: "MyClass", Kind: sdb.SymbolInformation_CLASS},
			},
		}},
	})

	setIndexField(t, idx, "typeBySimpleName", map[string][]*index.Symbol{
		"myclass": {
			{Name: "MyClass", Symbol: "pkg/MyClass#", Kind: sdb.SymbolInformation_CLASS},
		},
	})

	setIndexField(t, idx, "ownerMembers", map[string][]*index.Symbol{
		"pkg/MyClass#": {
			{Name: "getUserName", Symbol: "pkg/MyClass#getUserName().", Kind: sdb.SymbolInformation_METHOD, Signature: &index.SignatureInfo{Label: "String getUserName()", HasParams: false}},
			{Name: "doClean", Symbol: "pkg/MyClass#doClean().", Kind: sdb.SymbolInformation_METHOD, Signature: &index.SignatureInfo{Label: "void doClean()", HasParams: false}},
			{Name: "doCreate", Symbol: "pkg/MyClass#doCreate().", Kind: sdb.SymbolInformation_METHOD, Signature: &index.SignatureInfo{Label: "void doCreate()", HasParams: false}},
			{Name: "buildConfig", Symbol: "pkg/MyClass#buildConfig().", Kind: sdb.SymbolInformation_METHOD, Signature: &index.SignatureInfo{Label: "void buildConfig()", HasParams: false}},
		},
	})

	// Type lowercase prefix "dc" should match "doClean" and "doCreate" via case-insensitive prefix,
	// and "buildConfig" via fuzzy match (d...c in buil**d****c**onfig). Prefix matches should rank higher.
	cctx := &CompletionCtx{
		Kind:     CompletionDot,
		Receiver: "obj",
		Prefix:   "dc",
		Package:  "pkg",
		Locals:   []ValueDecl{{Name: "obj", Type: &index.TypeExpr{Sym: "MyClass"}}},
	}

	items := h.completeDot(cctx, "file://"+tmpDir+"/src/Test.java")

	if len(items) < 2 {
		t.Fatalf("expected at least 2 items for prefix 'dc', got %d: %+v", len(items), items)
	}

	// "doClean"/"doCreate" should rank before "buildConfig" because they match the prefix "dc"
	// case-insensitively, while "buildConfig" only fuzzy-matches.
	doCleanIdx := -1
	doCreateIdx := -1
	buildConfigIdx := -1
	for i, item := range items {
		switch item.FilterText {
		case "doClean":
			doCleanIdx = i
		case "doCreate":
			doCreateIdx = i
		case "buildConfig":
			buildConfigIdx = i
		}
	}

	if doCleanIdx == -1 {
		t.Fatalf("expected doClean() in results: %+v", items)
	}
	if doCreateIdx == -1 {
		t.Fatalf("expected doCreate() in results: %+v", items)
	}
	// buildConfig fuzzy-matches "dc" (d...c) but should rank after prefix matches
	if buildConfigIdx == -1 {
		t.Fatalf("expected buildConfig() in results (fuzzy match): %+v", items)
	}

	if buildConfigIdx <= doCleanIdx {
		t.Errorf("expected buildConfig (fuzzy, idx %d) to rank after doClean (prefix match, idx %d)", buildConfigIdx, doCleanIdx)
	}
	if buildConfigIdx <= doCreateIdx {
		t.Errorf("expected buildConfig (fuzzy, idx %d) to rank after doCreate (prefix match, idx %d)", buildConfigIdx, doCreateIdx)
	}
}
