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

func TestExtractUnimplementedInfo(t *testing.T) {
	tests := []struct {
		msg                             string
		wantClass, wantMethod, wantPart string
	}{
		{
			msg:        "MyService is not abstract and does not override abstract method handle(String) in Handler",
			wantClass:  "MyService",
			wantMethod: "handle",
			wantPart:   "Handler",
		},
		{
			msg:        "Foo is not abstract and does not override abstract method run() in Runnable",
			wantClass:  "Foo",
			wantMethod: "run",
			wantPart:   "Runnable",
		},
		{
			msg:        "cannot find symbol",
			wantClass:  "",
			wantMethod: "",
			wantPart:   "",
		},
	}

	for _, tt := range tests {
		className, methodName, parentName := extractUnimplementedInfo(tt.msg)
		if className != tt.wantClass {
			t.Errorf("extractUnimplementedInfo(%q): className = %q, want %q", tt.msg, className, tt.wantClass)
		}
		if methodName != tt.wantMethod {
			t.Errorf("extractUnimplementedInfo(%q): methodName = %q, want %q", tt.msg, methodName, tt.wantMethod)
		}
		if parentName != tt.wantPart {
			t.Errorf("extractUnimplementedInfo(%q): parentName = %q, want %q", tt.msg, parentName, tt.wantPart)
		}
	}
}

func TestImplementMethodsEdit(t *testing.T) {
	javaSource := `package com.example;

public class MyService implements Handler {
}
`

	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src", "main", "java", "com", "example")
	os.MkdirAll(srcDir, 0755)

	javaPath := filepath.Join(srcDir, "MyService.java")
	os.WriteFile(javaPath, []byte(javaSource), 0644)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := index.NewIndex(logger, tmpDir)

	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)

	relURI := "src/main/java/com/example/MyService.java"
	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{
			{
				Uri: relURI,
				Symbols: []*sdb.SymbolInformation{
					{
						Symbol:      "com/example/MyService#",
						DisplayName: "MyService",
						Kind:        sdb.SymbolInformation_CLASS,
						Signature: &sdb.Signature{
							SealedValue: &sdb.Signature_ClassSignature{
								ClassSignature: &sdb.ClassSignature{
									Parents: []*sdb.Type{
										{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "java/lang/Object#"}}},
										{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "com/example/Handler#"}}},
									},
								},
							},
						},
					},
				},
				Occurrences: []*sdb.SymbolOccurrence{
					{
						Symbol: "com/example/MyService#",
						Range:  &sdb.Range{StartLine: 2, StartCharacter: 13, EndLine: 2, EndCharacter: 22},
						Role:   sdb.SymbolOccurrence_DEFINITION,
					},
				},
			},
		},
	}
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "MyService.java.semanticdb"), data, 0644)

	// Set up the Handler interface with an abstract method in a separate document.
	handlerDocs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{
			{
				Uri: "src/main/java/com/example/Handler.java",
				Symbols: []*sdb.SymbolInformation{
					{
						Symbol:      "com/example/Handler#",
						DisplayName: "Handler",
						Kind:        sdb.SymbolInformation_INTERFACE,
					},
					{
						Symbol:      "com/example/Handler#handle().",
						DisplayName: "handle",
						Kind:        sdb.SymbolInformation_METHOD,
						Properties:  int32(sdb.SymbolInformation_ABSTRACT),
						Signature: &sdb.Signature{
							SealedValue: &sdb.Signature_MethodSignature{
								MethodSignature: &sdb.MethodSignature{
									ReturnType: nil, // void
									ParameterLists: []*sdb.Scope{
										{
											Hardlinks: []*sdb.SymbolInformation{
												{
													Symbol:      "com/example/Handler#handle().(request)",
													DisplayName: "request",
													Kind:        sdb.SymbolInformation_PARAMETER,
													Signature: &sdb.Signature{
														SealedValue: &sdb.Signature_ValueSignature{
															ValueSignature: &sdb.ValueSignature{
																Tpe: &sdb.Type{
																	SealedValue: &sdb.Type_TypeRef{
																		TypeRef: &sdb.TypeRef{Symbol: "java/lang/String#"},
																	},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
				Occurrences: []*sdb.SymbolOccurrence{
					{
						Symbol: "com/example/Handler#",
						Range:  &sdb.Range{StartLine: 0, StartCharacter: 17, EndLine: 0, EndCharacter: 24},
						Role:   sdb.SymbolOccurrence_DEFINITION,
					},
				},
			},
		},
	}
	handlerData, _ := proto.Marshal(handlerDocs)
	os.WriteFile(filepath.Join(sdbDir, "Handler.java.semanticdb"), handlerData, 0644)

	idx.Load()

	fileURI := uri.FromPath(javaPath)

	diag := Diagnostic{
		Range: Range{
			Start: Position{Line: 2, Character: 13},
			End:   Position{Line: 2, Character: 22},
		},
		Message: "MyService is not abstract and does not override abstract method handle(String) in Handler",
	}

	edit := implementMethodsEdit(fileURI, idx, javaSource, diag)
	if edit == nil {
		t.Fatal("implementMethodsEdit returned nil")
	}

	edits, ok := edit.Changes[fileURI]
	if !ok || len(edits) == 0 {
		t.Fatal("no edits for file")
	}

	newText := edits[0].NewText
	if !strings.Contains(newText, "@Override") {
		t.Errorf("expected @Override in stub, got: %s", newText)
	}
	if !strings.Contains(newText, "handle") {
		t.Errorf("expected method name 'handle' in stub, got: %s", newText)
	}
	if !strings.Contains(newText, "String request") {
		t.Errorf("expected parameter 'String request' in stub, got: %s", newText)
	}
	if !strings.Contains(newText, "UnsupportedOperationException") {
		t.Errorf("expected UnsupportedOperationException in stub, got: %s", newText)
	}
}

func TestGenerateMethodStub(t *testing.T) {
	sym := index.Symbol{
		Name: "process",
		Kind: sdb.SymbolInformation_METHOD,
		Signature: &index.SignatureInfo{
			Label:     "int process(String input, int count)",
			HasParams: true,
			Params: []index.ParamInfo{
				{Name: "input", Type: "String"},
				{Name: "count", Type: "int"},
			},
		},
	}

	stub := generateMethodStub(sym)
	if !strings.Contains(stub, "@Override") {
		t.Errorf("missing @Override: %s", stub)
	}
	if !strings.Contains(stub, "public int process(String input, int count)") {
		t.Errorf("wrong signature: %s", stub)
	}
	if !strings.Contains(stub, "throw new UnsupportedOperationException") {
		t.Errorf("missing throw: %s", stub)
	}
}

func TestGenerateMethodStub_Void(t *testing.T) {
	sym := index.Symbol{
		Name: "run",
		Kind: sdb.SymbolInformation_METHOD,
		Signature: &index.SignatureInfo{
			Label: "void run()",
		},
	}

	stub := generateMethodStub(sym)
	if !strings.Contains(stub, "public void run()") {
		t.Errorf("wrong void signature: %s", stub)
	}
}

func TestGenerateMethodStub_GenericOwnerSubstitution(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := index.NewIndex(logger, t.TempDir())
	defer idx.Close()

	setIndexField(t, idx, "classTypeParams", map[string][]string{
		"java/util/AbstractList#": {"java/util/AbstractList#[E]"},
	})
	setIndexField(t, idx, "symbolDeclType", map[string]*index.TypeExpr{
		"java/util/AbstractList#get().": {Sym: "java/util/AbstractList#[E]"},
	})
	setIndexField(t, idx, "symbolDeclParamTypes", map[string][]*index.TypeExpr{
		"java/util/AbstractList#get().": {{Sym: "scala/Int#"}},
	})

	sym := index.Symbol{
		Name:   "get",
		Kind:   sdb.SymbolInformation_METHOD,
		Symbol: "java/util/AbstractList#get().",
		Signature: &index.SignatureInfo{
			Label:         "E get(int index)",
			ReturnTypeSym: "java/util/AbstractList#[E]",
			HasParams:     true,
			Params:        []index.ParamInfo{{Name: "index", Type: "int", TypeSym: "scala/Int#"}},
		},
	}

	stub := generateMethodStubForOwner(sym, &index.TypeExpr{
		Sym:  "java/util/AbstractList#",
		Args: []*index.TypeExpr{{Sym: "java/lang/String#"}},
	}, idx)

	if !strings.Contains(stub, "public String get(int index)") {
		t.Fatalf("expected substituted generic return type in stub, got: %s", stub)
	}
}

func TestOverrideMethodActions(t *testing.T) {
	javaSource := `package com.example;

public class MyList extends AbstractList {
}
`

	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src", "main", "java", "com", "example")
	os.MkdirAll(srcDir, 0755)

	javaPath := filepath.Join(srcDir, "MyList.java")
	os.WriteFile(javaPath, []byte(javaSource), 0644)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := index.NewIndex(logger, tmpDir)

	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)

	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{
			{
				Uri: "src/main/java/com/example/MyList.java",
				Symbols: []*sdb.SymbolInformation{
					{
						Symbol:      "com/example/MyList#",
						DisplayName: "MyList",
						Kind:        sdb.SymbolInformation_CLASS,
						Signature: &sdb.Signature{
							SealedValue: &sdb.Signature_ClassSignature{
								ClassSignature: &sdb.ClassSignature{
									Parents: []*sdb.Type{
										{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "com/example/AbstractList#"}}},
									},
								},
							},
						},
					},
				},
				Occurrences: []*sdb.SymbolOccurrence{
					{
						Symbol: "com/example/MyList#",
						Range:  &sdb.Range{StartLine: 2, StartCharacter: 13, EndLine: 2, EndCharacter: 19},
						Role:   sdb.SymbolOccurrence_DEFINITION,
					},
				},
			},
			{
				Uri: "src/main/java/com/example/AbstractList.java",
				Symbols: []*sdb.SymbolInformation{
					{
						Symbol:      "com/example/AbstractList#",
						DisplayName: "AbstractList",
						Kind:        sdb.SymbolInformation_CLASS,
					},
					{
						Symbol:      "com/example/AbstractList#add().",
						DisplayName: "add",
						Kind:        sdb.SymbolInformation_METHOD,
						Signature: &sdb.Signature{
							SealedValue: &sdb.Signature_MethodSignature{
								MethodSignature: &sdb.MethodSignature{
									ReturnType: &sdb.Type{
										SealedValue: &sdb.Type_TypeRef{
											TypeRef: &sdb.TypeRef{Symbol: "scala/Boolean#"},
										},
									},
									ParameterLists: []*sdb.Scope{
										{
											Hardlinks: []*sdb.SymbolInformation{
												{
													Symbol:      "com/example/AbstractList#add().(item)",
													DisplayName: "item",
													Kind:        sdb.SymbolInformation_PARAMETER,
													Signature: &sdb.Signature{
														SealedValue: &sdb.Signature_ValueSignature{
															ValueSignature: &sdb.ValueSignature{
																Tpe: &sdb.Type{
																	SealedValue: &sdb.Type_TypeRef{
																		TypeRef: &sdb.TypeRef{Symbol: "java/lang/Object#"},
																	},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
					// Abstract method — should be skipped by override
					{
						Symbol:      "com/example/AbstractList#size().",
						DisplayName: "size",
						Kind:        sdb.SymbolInformation_METHOD,
						Properties:  int32(sdb.SymbolInformation_ABSTRACT),
						Signature: &sdb.Signature{
							SealedValue: &sdb.Signature_MethodSignature{
								MethodSignature: &sdb.MethodSignature{
									ReturnType: &sdb.Type{
										SealedValue: &sdb.Type_TypeRef{
											TypeRef: &sdb.TypeRef{Symbol: "scala/Int#"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "MyList.java.semanticdb"), data, 0644)

	idx.Load()
	fileURI := uri.FromPath(javaPath)

	methods, insertLine := collectOverridableMethods(fileURI, idx, javaSource, 2)

	if insertLine < 0 {
		t.Fatal("expected valid insert line")
	}

	// Should have "add" but NOT "size" (abstract is handled by implement)
	foundAdd := false
	for _, m := range methods {
		if m.method.Name == "add" {
			foundAdd = true
		}
		if m.method.Name == "size" {
			t.Error("should not offer override for abstract method 'size'")
		}
	}
	if !foundAdd {
		t.Errorf("expected overridable method 'add', got %d methods: %v", len(methods), methods)
	}
}
