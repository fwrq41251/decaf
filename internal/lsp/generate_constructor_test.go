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

func TestGenerateConstructorEdit(t *testing.T) {
	javaSource := `package com.example;

public class User {
    private String name;
    private int age;
}
`

	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src", "main", "java", "com", "example")
	os.MkdirAll(srcDir, 0755)

	javaPath := filepath.Join(srcDir, "User.java")
	os.WriteFile(javaPath, []byte(javaSource), 0644)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := index.NewIndex(logger, tmpDir)

	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)

	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{
			{
				Uri: "src/main/java/com/example/User.java",
				Symbols: []*sdb.SymbolInformation{
					{
						Symbol:      "com/example/User#",
						DisplayName: "User",
						Kind:        sdb.SymbolInformation_CLASS,
					},
					{
						Symbol:      "com/example/User#name.",
						DisplayName: "name",
						Kind:        sdb.SymbolInformation_FIELD,
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
					{
						Symbol:      "com/example/User#age.",
						DisplayName: "age",
						Kind:        sdb.SymbolInformation_FIELD,
						Signature: &sdb.Signature{
							SealedValue: &sdb.Signature_ValueSignature{
								ValueSignature: &sdb.ValueSignature{
									Tpe: &sdb.Type{
										SealedValue: &sdb.Type_TypeRef{
											TypeRef: &sdb.TypeRef{Symbol: "scala/Int#"},
										},
									},
								},
							},
						},
					},
				},
				Occurrences: []*sdb.SymbolOccurrence{
					{
						Symbol: "com/example/User#",
						Range:  &sdb.Range{StartLine: 2, StartCharacter: 13, EndLine: 2, EndCharacter: 17},
						Role:   sdb.SymbolOccurrence_DEFINITION,
					},
				},
			},
		},
	}
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "User.java.semanticdb"), data, 0644)

	idx.Load()
	fileURI := uri.FromPath(javaPath)

	edit := generateConstructorEdit(fileURI, idx, javaSource, 3) // cursor on a field line
	if edit == nil {
		t.Fatal("generateConstructorEdit returned nil")
	}

	edits, ok := edit.Changes[fileURI]
	if !ok || len(edits) == 0 {
		t.Fatal("no edits for file")
	}

	newText := edits[0].NewText
	if !strings.Contains(newText, "public User(") {
		t.Errorf("expected constructor name 'User', got: %s", newText)
	}
	if !strings.Contains(newText, "String name") {
		t.Errorf("expected parameter 'String name', got: %s", newText)
	}
	if !strings.Contains(newText, "this.name = name") {
		t.Errorf("expected field assignment 'this.name = name', got: %s", newText)
	}
	if !strings.Contains(newText, "this.age = age") {
		t.Errorf("expected field assignment 'this.age = age', got: %s", newText)
	}
}

func TestGenerateConstructorEdit_AlreadyHasConstructor(t *testing.T) {
	javaSource := `package com.example;

public class Foo {
    private String name;
}
`

	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src", "main", "java", "com", "example")
	os.MkdirAll(srcDir, 0755)

	javaPath := filepath.Join(srcDir, "Foo.java")
	os.WriteFile(javaPath, []byte(javaSource), 0644)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := index.NewIndex(logger, tmpDir)

	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)

	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{
			{
				Uri: "src/main/java/com/example/Foo.java",
				Symbols: []*sdb.SymbolInformation{
					{
						Symbol:      "com/example/Foo#",
						DisplayName: "Foo",
						Kind:        sdb.SymbolInformation_CLASS,
					},
					{
						Symbol:      "com/example/Foo#name.",
						DisplayName: "name",
						Kind:        sdb.SymbolInformation_FIELD,
					},
					{
						Symbol:      "com/example/Foo#`<init>`().",
						DisplayName: "Foo",
						Kind:        sdb.SymbolInformation_CONSTRUCTOR,
					},
				},
				Occurrences: []*sdb.SymbolOccurrence{
					{
						Symbol: "com/example/Foo#",
						Range:  &sdb.Range{StartLine: 2, StartCharacter: 13, EndLine: 2, EndCharacter: 16},
						Role:   sdb.SymbolOccurrence_DEFINITION,
					},
				},
			},
		},
	}
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "Foo.java.semanticdb"), data, 0644)

	idx.Load()
	fileURI := uri.FromPath(javaPath)

	edit := generateConstructorEdit(fileURI, idx, javaSource, 3)
	if edit != nil {
		t.Error("expected nil when constructor already exists")
	}
}

func TestFormatConstructor(t *testing.T) {
	fields := []index.Symbol{
		{Name: "name", Signature: &index.SignatureInfo{Label: "name: String"}},
		{Name: "age", Signature: &index.SignatureInfo{Label: "age: int"}},
	}

	result := formatConstructor("User", fields)
	if !strings.Contains(result, "public User(String name, int age)") {
		t.Errorf("wrong constructor signature: %s", result)
	}
	if !strings.Contains(result, "this.name = name;") {
		t.Errorf("missing assignment: %s", result)
	}
	if !strings.Contains(result, "this.age = age;") {
		t.Errorf("missing assignment: %s", result)
	}
}

func TestFormatConstructor_NoFields(t *testing.T) {
	result := formatConstructor("Empty", nil)
	if !strings.Contains(result, "public Empty()") {
		t.Errorf("wrong no-arg constructor: %s", result)
	}
}
