package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fwrq41251/decaf/internal/index"
	"github.com/fwrq41251/decaf/internal/jsonrpc"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"google.golang.org/protobuf/proto"
)

func TestCodeAction_AddImportForNestedTypeInSamePackage(t *testing.T) {
	tmpDir := t.TempDir()
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	h := NewHandler(logger, jsonrpc.NewTransport(&bytes.Buffer{}, &bytes.Buffer{}))

	idx := index.NewIndex(logger, tmpDir)
	defer idx.Close()
	close(h.indexReady)
	h.idx = idx
	h.rootURI = "file://" + tmpDir

	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Types.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "com/example/Outer#", DisplayName: "Outer", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "com/example/Outer#Inner#", DisplayName: "Inner", Kind: sdb.SymbolInformation_CLASS},
			},
		}},
	}
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	if err := os.MkdirAll(sdbDir, 0755); err != nil {
		t.Fatalf("mkdir semanticdb dir: %v", err)
	}
	data, _ := proto.Marshal(docs)
	if err := os.WriteFile(filepath.Join(sdbDir, "Types.java.semanticdb"), data, 0644); err != nil {
		t.Fatalf("write semanticdb: %v", err)
	}
	writeSourcePlaceholdersForDocs(t, tmpDir, docs)
	idx.Load()

	fileURI := "file://" + tmpDir + "/src/Use.java"
	content := `package com.example;
public class Use {
    Inner value;
}`
	h.docs.Open(fileURI, content)

	params := CodeActionParams{
		TextDocument: TextDocumentIdentifier{URI: fileURI},
		Range:        Range{},
		Context: CodeActionContext{
			Only: []string{CodeActionQuickFix},
			Diagnostics: []Diagnostic{{
				Message: "cannot find symbol\n  symbol:   class Inner",
			}},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleCodeAction(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleCodeAction failed: %v", err)
	}

	actions := got.([]CodeAction)
	if len(actions) == 0 {
		t.Fatal("expected quick fix actions, got none")
	}

	for _, action := range actions {
		if action.Title != "Add import 'com.example.Outer.Inner'" {
			continue
		}
		if action.Edit == nil || len(action.Edit.Changes[fileURI]) != 1 {
			t.Fatalf("expected import edit for nested type, got %+v", action.Edit)
		}
		if !strings.Contains(action.Edit.Changes[fileURI][0].NewText, "import com.example.Outer.Inner;") {
			t.Fatalf("expected nested type import edit, got %+v", action.Edit.Changes[fileURI][0])
		}
		return
	}

	t.Fatalf("expected nested type import action, got %+v", actions)
}

func TestCodeAction_DoesNotAddImportForTopLevelTypeInSamePackage(t *testing.T) {
	tmpDir := t.TempDir()
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	h := NewHandler(logger, jsonrpc.NewTransport(&bytes.Buffer{}, &bytes.Buffer{}))

	idx := index.NewIndex(logger, tmpDir)
	defer idx.Close()
	close(h.indexReady)
	h.idx = idx
	h.rootURI = "file://" + tmpDir

	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Helper.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "com/example/Helper#", DisplayName: "Helper", Kind: sdb.SymbolInformation_CLASS},
			},
		}},
	}
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	if err := os.MkdirAll(sdbDir, 0755); err != nil {
		t.Fatalf("mkdir semanticdb dir: %v", err)
	}
	data, _ := proto.Marshal(docs)
	if err := os.WriteFile(filepath.Join(sdbDir, "Helper.java.semanticdb"), data, 0644); err != nil {
		t.Fatalf("write semanticdb: %v", err)
	}
	writeSourcePlaceholdersForDocs(t, tmpDir, docs)
	idx.Load()

	fileURI := "file://" + tmpDir + "/src/Use.java"
	content := `package com.example;
public class Use {
    Helper value;
}`
	h.docs.Open(fileURI, content)

	params := CodeActionParams{
		TextDocument: TextDocumentIdentifier{URI: fileURI},
		Range:        Range{},
		Context: CodeActionContext{
			Only: []string{CodeActionQuickFix},
			Diagnostics: []Diagnostic{{
				Message: "cannot find symbol\n  symbol:   class Helper",
			}},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleCodeAction(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleCodeAction failed: %v", err)
	}

	actions := got.([]CodeAction)
	for _, action := range actions {
		if strings.HasPrefix(action.Title, "Add import '") {
			t.Fatalf("unexpected import quick fix for same-package top-level type: %+v", action)
		}
	}
}

func TestCodeAction_DeduplicatesImportFixesWithSameFQN(t *testing.T) {
	tmpDir := t.TempDir()
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	h := NewHandler(logger, jsonrpc.NewTransport(&bytes.Buffer{}, &bytes.Buffer{}))

	idx := index.NewIndex(logger, tmpDir)
	defer idx.Close()
	close(h.indexReady)
	h.idx = idx
	h.rootURI = "file://" + tmpDir

	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Request.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "org/winry/model/Request#", DisplayName: "Request", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "org/winry/model/Request#", DisplayName: "Request", Kind: sdb.SymbolInformation_CLASS},
			},
		}},
	}
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	if err := os.MkdirAll(sdbDir, 0755); err != nil {
		t.Fatalf("mkdir semanticdb dir: %v", err)
	}
	data, _ := proto.Marshal(docs)
	if err := os.WriteFile(filepath.Join(sdbDir, "Request.java.semanticdb"), data, 0644); err != nil {
		t.Fatalf("write semanticdb: %v", err)
	}
	writeSourcePlaceholdersForDocs(t, tmpDir, docs)
	idx.Load()

	fileURI := "file://" + tmpDir + "/src/Use.java"
	content := `package com.example;
public class Use {
    Request request;
}`
	h.docs.Open(fileURI, content)

	params := CodeActionParams{
		TextDocument: TextDocumentIdentifier{URI: fileURI},
		Range:        Range{},
		Context: CodeActionContext{
			Only: []string{CodeActionQuickFix},
			Diagnostics: []Diagnostic{{
				Message: "cannot find symbol\n  symbol:   class Request",
			}},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleCodeAction(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleCodeAction failed: %v", err)
	}

	actions := got.([]CodeAction)
	var count int
	for _, action := range actions {
		if action.Title == "Add import 'org.winry.model.Request'" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 Request import action, got %d: %+v", count, actions)
	}
}
