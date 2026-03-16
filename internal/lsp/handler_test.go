package lsp

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fwrq41251/decaf/internal/index"
	"github.com/fwrq41251/decaf/internal/jsonrpc"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"google.golang.org/protobuf/proto"
)

// buildMessage creates a Content-Length framed JSON-RPC message.
func buildMessage(t *testing.T, id *int, method string, params any) string {
	t.Helper()

	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if id != nil {
		msg["id"] = *id
	}
	if params != nil {
		msg["params"] = params
	}

	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
}

func TestLifecycle(t *testing.T) {
	// Simulate: initialize -> initialized -> shutdown -> exit
	var input bytes.Buffer
	input.WriteString(buildMessage(t, intPtr(1), "initialize", InitializeParams{
		RootURI: "file:///tmp/project",
	}))
	input.WriteString(buildMessage(t, nil, "initialized", struct{}{}))
	input.WriteString(buildMessage(t, intPtr(2), "shutdown", nil))
	input.WriteString(buildMessage(t, nil, "exit", nil))

	var output bytes.Buffer
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)

	transport := jsonrpc.NewTransport(&input, &output)
	dispatcher := jsonrpc.NewDispatcher(transport, logger)

	handler := NewHandler(logger, transport)
	handler.RegisterAll(dispatcher)

	ctx := context.Background()
	err := dispatcher.Run(ctx)
	if err != nil {
		t.Fatalf("dispatcher exited with error: %v", err)
	}

	// Parse responses from output.
	outTransport := jsonrpc.NewTransport(&output, nil)

	// Response 1: initialize result.
	resp1, err := outTransport.ReadResponse()
	if err != nil {
		t.Fatalf("reading initialize response: %v", err)
	}
	if resp1.Error != nil {
		t.Fatalf("initialize returned error: %s", resp1.Error.Message)
	}
	var initResult InitializeResult
	if err := json.Unmarshal(resp1.Result, &initResult); err != nil {
		t.Fatalf("decoding initialize result: %v", err)
	}
	if initResult.ServerInfo == nil || initResult.ServerInfo.Name != "decaf" {
		t.Fatalf("unexpected server info: %+v", initResult.ServerInfo)
	}
	if initResult.Capabilities.TextDocumentSync == nil {
		t.Fatal("expected TextDocumentSync capability")
	}

	// Response 2: shutdown result (null).
	resp2, err := outTransport.ReadResponse()
	if err != nil {
		t.Fatalf("reading shutdown response: %v", err)
	}
	if resp2.Error != nil {
		t.Fatalf("shutdown returned error: %s", resp2.Error.Message)
	}
}

func TestDefinition(t *testing.T) {
	tmpDir := t.TempDir()
	
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	transport := jsonrpc.NewTransport(&bytes.Buffer{}, &bytes.Buffer{})
	h := NewHandler(logger, transport)
	
	// Mock Index with one workspace definition and one external symbol.
	idx := index.NewIndex(logger, tmpDir)
	
	// 1. Workspace symbol
	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{
			{
				Uri: "src/Main.java",
				Symbols: []*sdb.SymbolInformation{
					{
						Symbol:      "com/example/Main#",
						DisplayName: "Main",
						Kind:        sdb.SymbolInformation_CLASS,
					},
				},
				Occurrences: []*sdb.SymbolOccurrence{
					{
						Symbol: "com/example/Main#",
						Role:   sdb.SymbolOccurrence_DEFINITION,
						Range:  &sdb.Range{StartLine: 2, StartCharacter: 13, EndLine: 2, EndCharacter: 17},
					},
					{
						Symbol: "com/example/Main#",
						Role:   sdb.SymbolOccurrence_REFERENCE,
						Range:  &sdb.Range{StartLine: 10, StartCharacter: 8, EndLine: 10, EndCharacter: 12},
					},
				},
			},
		},
	}
	// We need to use internal methods or just call indexDocument directly if exported.
	// Since indexDocument is private, we simulate a load or use Load() on a file.
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "Main.java.semanticdb"), data, 0644)
	idx.Load()
	
	// 2. External symbol setup (mock JAR)
	jarPath := filepath.Join(tmpDir, "lib.jar")
	f, _ := os.Create(jarPath)
	w := zip.NewWriter(f)
	zf, _ := w.Create("com/example/Lib.java")
	io.WriteString(zf, "package com.example;\npublic class Lib {}")
	w.Close()
	f.Close()
	idx.AddDependencySource(jarPath)

	h.idx = idx

	// Test case: Workspace definition
	params := TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: "file://" + tmpDir + "/src/Main.java"},
		Position:     Position{Line: 10, Character: 9},
	}
	rawParams, _ := json.Marshal(params)
	
	got, err := h.handleDefinition(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleDefinition failed: %v", err)
	}
	locs := got.([]LSPLocation)
	if len(locs) != 1 {
		t.Fatalf("expected 1 location, got %d", len(locs))
	}
	if locs[0].Range.Start.Line != 2 {
		t.Errorf("got line %d, want 2", locs[0].Range.Start.Line)
	}

	// Test case: External definition (using a reference to com/example/Lib# in Main.java)
	// Add an occurrence for the external symbol in the index.
	docsExt := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{
			{
				Uri: "src/Main.java",
				Occurrences: []*sdb.SymbolOccurrence{
					{
						Symbol: "com/example/Lib#",
						Role:   sdb.SymbolOccurrence_REFERENCE,
						Range:  &sdb.Range{StartLine: 15, StartCharacter: 5, EndLine: 15, EndCharacter: 8},
					},
				},
			},
		},
	}
	dataExt, _ := proto.Marshal(docsExt)
	os.WriteFile(filepath.Join(sdbDir, "External.java.semanticdb"), dataExt, 0644)
	time.Sleep(100 * time.Millisecond) // let watcher pick up the new file
	idx.Load()                         // Re-load to get the new occurrence.

	paramsExt := TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: "file://" + tmpDir + "/src/Main.java"},
		Position:     Position{Line: 15, Character: 6},
	}
	rawParamsExt, _ := json.Marshal(paramsExt)
	
	gotExt, err := h.handleDefinition(context.Background(), rawParamsExt)
	if err != nil {
		t.Fatalf("handleDefinition failed for external symbol: %v", err)
	}
	locsExt := gotExt.([]LSPLocation)
	if len(locsExt) != 1 {
		t.Fatalf("expected 1 external location, got %d", len(locsExt))
	}
	if !strings.Contains(locsExt[0].URI, "Lib.java") {
		t.Errorf("got URI %q, want it to contain Lib.java", locsExt[0].URI)
	}
}

func TestPrepareRename(t *testing.T) {
	tmpDir := t.TempDir()
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	h := NewHandler(logger, jsonrpc.NewTransport(&bytes.Buffer{}, &bytes.Buffer{}))
	
	idx := index.NewIndex(logger, tmpDir)
	h.idx = idx

	// Define a class and a reference to it in different locations.
	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{
			{
				Uri: "src/Main.java",
				Occurrences: []*sdb.SymbolOccurrence{
					{
						Symbol: "com/example/Main#",
						Role:   sdb.SymbolOccurrence_DEFINITION,
						Range:  &sdb.Range{StartLine: 2, StartCharacter: 13, EndLine: 2, EndCharacter: 17},
					},
					{
						Symbol: "com/example/Main#",
						Role:   sdb.SymbolOccurrence_REFERENCE,
						Range:  &sdb.Range{StartLine: 10, StartCharacter: 8, EndLine: 10, EndCharacter: 12},
					},
				},
			},
		},
	}
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "Main.java.semanticdb"), data, 0644)
	idx.Load()

	// 1. Prepare rename on the reference (line 10).
	paramsRef := PrepareRenameParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: "file://" + tmpDir + "/src/Main.java"},
			Position:     Position{Line: 10, Character: 9},
		},
	}
	rawParamsRef, _ := json.Marshal(paramsRef)
	
	gotRef, err := h.handlePrepareRename(context.Background(), rawParamsRef)
	if err != nil {
		t.Fatalf("handlePrepareRename failed: %v", err)
	}
	
	resRef := gotRef.(map[string]any)
	rngRef := resRef["range"].(Range)
	
	// Should return the range of the reference (line 10), not the definition (line 2).
	if rngRef.Start.Line != 10 {
		t.Errorf("expected range at line 10, got %d", rngRef.Start.Line)
	}
	if resRef["placeholder"] != "Main" {
		t.Errorf("expected placeholder 'Main', got %v", resRef["placeholder"])
	}

	// 2. Prepare rename on the definition (line 2).
	paramsDef := PrepareRenameParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: "file://" + tmpDir + "/src/Main.java"},
			Position:     Position{Line: 2, Character: 15},
		},
	}
	rawParamsDef, _ := json.Marshal(paramsDef)
	
	gotDef, err := h.handlePrepareRename(context.Background(), rawParamsDef)
	if err != nil {
		t.Fatalf("handlePrepareRename failed: %v", err)
	}
	
	resDef := gotDef.(map[string]any)
	rngDef := resDef["range"].(Range)
	
	if rngDef.Start.Line != 2 {
		t.Errorf("expected range at line 2, got %d", rngDef.Start.Line)
	}
}
