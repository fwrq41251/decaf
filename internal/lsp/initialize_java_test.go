package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"testing"

	"github.com/fwrq41251/decaf/internal/index"
	"github.com/fwrq41251/decaf/internal/jsonrpc"
)

func TestCodeAction_InitializeJavaTypeForEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	h := NewHandler(logger, jsonrpc.NewTransport(&bytes.Buffer{}, &bytes.Buffer{}))

	idx := index.NewIndex(logger, tmpDir)
	defer idx.Close()
	h.markIndexReadyForTest()
	h.setIndexForTest(idx)
	h.rootURI = "file://" + tmpDir

	fileURI := "file://" + tmpDir + "/src/main/java/com/example/Foo.java"
	h.docs.Open(fileURI, "   \n")

	params := CodeActionParams{
		TextDocument: TextDocumentIdentifier{URI: fileURI},
		Range:        Range{},
		Context: CodeActionContext{
			Only: []string{"source"},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleCodeAction(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleCodeAction failed: %v", err)
	}

	actions := got.([]CodeAction)
	for _, action := range actions {
		if action.Title != "Initialize Java Type" {
			continue
		}
		if action.Command == nil {
			t.Fatal("expected initialize action to use a command")
		}
		if action.Command.Command != "decaf.initializeJavaType" {
			t.Fatalf("unexpected command %q", action.Command.Command)
		}
		return
	}

	t.Fatalf("expected initialize action, got %+v", actions)
}

func TestCodeAction_InitializeJavaTypeSkippedForNonEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	h := NewHandler(logger, jsonrpc.NewTransport(&bytes.Buffer{}, &bytes.Buffer{}))

	idx := index.NewIndex(logger, tmpDir)
	defer idx.Close()
	h.markIndexReadyForTest()
	h.setIndexForTest(idx)
	h.rootURI = "file://" + tmpDir

	fileURI := "file://" + tmpDir + "/src/main/java/com/example/Foo.java"
	h.docs.Open(fileURI, "package com.example;\n\npublic class Foo {}\n")

	params := CodeActionParams{
		TextDocument: TextDocumentIdentifier{URI: fileURI},
		Range:        Range{},
		Context: CodeActionContext{
			Only: []string{"source"},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleCodeAction(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleCodeAction failed: %v", err)
	}

	actions := got.([]CodeAction)
	for _, action := range actions {
		if action.Title == "Initialize Java Type" {
			t.Fatalf("did not expect initialize action for non-empty file: %+v", action)
		}
	}
}

func TestInitializeJavaTypeTemplate(t *testing.T) {
	rootURI := "file:///workspace"
	fileURI := "file:///workspace/src/main/java/com/example/Foo.java"

	got := initializeJavaTypeTemplate(rootURI, fileURI, "class")
	want := "package com.example;\n\npublic class Foo {\n}\n"
	if got != want {
		t.Fatalf("template mismatch:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestInitializeJavaTypeTemplateWithoutPackage(t *testing.T) {
	rootURI := "file:///workspace"
	fileURI := "file:///workspace/Foo.java"

	got := initializeJavaTypeTemplate(rootURI, fileURI, "record")
	want := "public record Foo() {\n}\n"
	if got != want {
		t.Fatalf("template mismatch:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestInitializeJavaTypeTemplateForMultiModuleProject(t *testing.T) {
	rootURI := "file:///workspace"
	fileURI := "file:///workspace/module-a/src/main/java/com/example/Foo.java"

	got := initializeJavaTypeTemplate(rootURI, fileURI, "class")
	want := "package com.example;\n\npublic class Foo {\n}\n"
	if got != want {
		t.Fatalf("template mismatch:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestTrimJavaSourceRootPrefixFindsNearestSourceRoot(t *testing.T) {
	segments := []string{"workspace", "module-a", "src", "main", "java", "com", "example"}
	got := trimJavaSourceRootPrefix(segments)
	want := []string{"com", "example"}
	if strings.Join(got, "/") != strings.Join(want, "/") {
		t.Fatalf("trimJavaSourceRootPrefix() = %v, want %v", got, want)
	}
}

func TestCanInitializeJavaTypeRejectsInvalidCases(t *testing.T) {
	tests := []struct {
		name    string
		fileURI string
		overlay string
		want    bool
	}{
		{name: "empty java file", fileURI: "file:///workspace/Foo.java", overlay: " \n", want: true},
		{name: "non java file", fileURI: "file:///workspace/Foo.txt", overlay: "", want: false},
		{name: "non empty java file", fileURI: "file:///workspace/Foo.java", overlay: "class Foo {}", want: false},
		{name: "invalid class name", fileURI: "file:///workspace/123Foo.java", overlay: "", want: false},
	}

	for _, tc := range tests {
		if got := canInitializeJavaType(tc.fileURI, tc.overlay); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
