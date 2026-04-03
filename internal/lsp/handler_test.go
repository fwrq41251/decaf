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

	"github.com/fwrq41251/decaf/internal/bsp"
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
	defer idx.Close()
	close(h.indexReady) // signal index is ready for test
	
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
	defer idx.Close()
	close(h.indexReady) // signal index is ready for test
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

func TestRenameTopLevelClass(t *testing.T) {
	tmpDir := t.TempDir()
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	h := NewHandler(logger, jsonrpc.NewTransport(&bytes.Buffer{}, &bytes.Buffer{}))
	h.rootURI = "file://" + tmpDir

	idx := index.NewIndex(logger, tmpDir)
	defer idx.Close()
	close(h.indexReady)
	h.idx = idx

	// Foo.java defines Foo and references it; Bar.java references Foo.
	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{
			{
				Uri: "src/Foo.java",
				Symbols: []*sdb.SymbolInformation{
					{Symbol: "com/example/Foo#", DisplayName: "Foo", Kind: sdb.SymbolInformation_CLASS},
				},
				Occurrences: []*sdb.SymbolOccurrence{
					{Symbol: "com/example/Foo#", Role: sdb.SymbolOccurrence_DEFINITION, Range: &sdb.Range{StartLine: 2, StartCharacter: 13, EndLine: 2, EndCharacter: 16}},
					{Symbol: "com/example/Foo#", Role: sdb.SymbolOccurrence_REFERENCE, Range: &sdb.Range{StartLine: 5, StartCharacter: 8, EndLine: 5, EndCharacter: 11}},
				},
			},
			{
				Uri: "src/Bar.java",
				Occurrences: []*sdb.SymbolOccurrence{
					{Symbol: "com/example/Foo#", Role: sdb.SymbolOccurrence_REFERENCE, Range: &sdb.Range{StartLine: 3, StartCharacter: 4, EndLine: 3, EndCharacter: 7}},
				},
			},
		},
	}
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "Foo.java.semanticdb"), data, 0644)
	idx.Load()

	renameParams := RenameParams{
		TextDocument: TextDocumentIdentifier{URI: "file://" + tmpDir + "/src/Foo.java"},
		Position:     Position{Line: 2, Character: 14},
		NewName:      "Baz",
	}
	rawParams, _ := json.Marshal(renameParams)

	// Case 1: client does NOT support rename resource operations — fallback to Changes only.
	h.clientCaps = ClientCapabilities{}
	got, err := h.handleRename(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleRename failed: %v", err)
	}
	edit := got.(WorkspaceEdit)
	if len(edit.DocumentChanges) != 0 {
		t.Errorf("expected no DocumentChanges without client support, got %d", len(edit.DocumentChanges))
	}
	if len(edit.Changes) == 0 {
		t.Fatal("expected Changes to be populated")
	}
	totalEdits := 0
	for _, edits := range edit.Changes {
		totalEdits += len(edits)
	}
	if totalEdits != 3 {
		t.Errorf("expected 3 text edits, got %d", totalEdits)
	}

	// Case 2: client supports rename — should produce DocumentChanges with RenameFile.
	h.clientCaps = ClientCapabilities{
		Workspace: &WorkspaceClientCapabilities{
			WorkspaceEdit: &WorkspaceEditClientCapabilities{
				ResourceOperations: []string{"create", "rename", "delete"},
			},
		},
	}
	got, err = h.handleRename(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleRename failed: %v", err)
	}
	edit = got.(WorkspaceEdit)
	if edit.Changes != nil {
		t.Error("expected Changes to be nil when DocumentChanges is used")
	}
	if len(edit.DocumentChanges) == 0 {
		t.Fatal("expected DocumentChanges to be populated")
	}

	// Verify there is exactly one RenameFile operation with correct URIs.
	var renameOps []RenameFile
	for _, dc := range edit.DocumentChanges {
		raw, _ := dc.MarshalJSON()
		var probe struct {
			Kind string `json:"kind"`
		}
		json.Unmarshal(raw, &probe)
		if probe.Kind == "rename" {
			var rf RenameFile
			json.Unmarshal(raw, &rf)
			renameOps = append(renameOps, rf)
		}
	}
	if len(renameOps) != 1 {
		t.Fatalf("expected 1 RenameFile operation, got %d", len(renameOps))
	}
	if !strings.HasSuffix(renameOps[0].OldURI, "/src/Foo.java") {
		t.Errorf("expected OldURI to end with /src/Foo.java, got %s", renameOps[0].OldURI)
	}
	if !strings.HasSuffix(renameOps[0].NewURI, "/src/Baz.java") {
		t.Errorf("expected NewURI to end with /src/Baz.java, got %s", renameOps[0].NewURI)
	}
}

func TestRenameInnerClassNoFileRename(t *testing.T) {
	tmpDir := t.TempDir()
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	h := NewHandler(logger, jsonrpc.NewTransport(&bytes.Buffer{}, &bytes.Buffer{}))
	h.rootURI = "file://" + tmpDir
	h.clientCaps = ClientCapabilities{
		Workspace: &WorkspaceClientCapabilities{
			WorkspaceEdit: &WorkspaceEditClientCapabilities{
				ResourceOperations: []string{"rename"},
			},
		},
	}

	idx := index.NewIndex(logger, tmpDir)
	defer idx.Close()
	close(h.indexReady)
	h.idx = idx

	// Inner class: Outer#Inner# has two '#' — should NOT trigger file rename.
	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{
			{
				Uri: "src/Outer.java",
				Symbols: []*sdb.SymbolInformation{
					{Symbol: "com/example/Outer#Inner#", DisplayName: "Inner", Kind: sdb.SymbolInformation_CLASS},
				},
				Occurrences: []*sdb.SymbolOccurrence{
					{Symbol: "com/example/Outer#Inner#", Role: sdb.SymbolOccurrence_DEFINITION, Range: &sdb.Range{StartLine: 5, StartCharacter: 17, EndLine: 5, EndCharacter: 22}},
				},
			},
		},
	}
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "Outer.java.semanticdb"), data, 0644)
	idx.Load()

	renameParams := RenameParams{
		TextDocument: TextDocumentIdentifier{URI: "file://" + tmpDir + "/src/Outer.java"},
		Position:     Position{Line: 5, Character: 18},
		NewName:      "InnerRenamed",
	}
	rawParams, _ := json.Marshal(renameParams)

	got, err := h.handleRename(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleRename failed: %v", err)
	}
	edit := got.(WorkspaceEdit)
	if len(edit.DocumentChanges) != 0 {
		t.Errorf("inner class rename should not produce DocumentChanges, got %d", len(edit.DocumentChanges))
	}
	if len(edit.Changes) == 0 {
		t.Error("expected text edits in Changes")
	}
}

func TestSignatureHelp_UncompiledCode(t *testing.T) {
	tmpDir := t.TempDir()
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	h := NewHandler(logger, jsonrpc.NewTransport(&bytes.Buffer{}, &bytes.Buffer{}))

	idx := index.NewIndex(logger, tmpDir)
	defer idx.Close()
	close(h.indexReady)
	h.idx = idx
	h.rootURI = "file://" + tmpDir

	// Index a class Foo with an overloaded method "bar":
	//   bar(int x) and bar(int x, String y)
	// No occurrences for the call site — simulating uncompiled code.
	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Foo.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "com/example/Foo#", DisplayName: "Foo", Kind: sdb.SymbolInformation_CLASS},
				{
					Symbol: "com/example/Foo#bar().", DisplayName: "bar", Kind: sdb.SymbolInformation_METHOD,
					Signature: &sdb.Signature{
						SealedValue: &sdb.Signature_MethodSignature{
							MethodSignature: &sdb.MethodSignature{
								ReturnType: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "scala/Unit#"}}},
								ParameterLists: []*sdb.Scope{{
									Hardlinks: []*sdb.SymbolInformation{
										{DisplayName: "x", Signature: &sdb.Signature{SealedValue: &sdb.Signature_ValueSignature{ValueSignature: &sdb.ValueSignature{Tpe: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "scala/Int#"}}}}}}},
									},
								}},
							},
						},
					},
				},
				{
					Symbol: "com/example/Foo#bar().", DisplayName: "bar", Kind: sdb.SymbolInformation_METHOD,
					Signature: &sdb.Signature{
						SealedValue: &sdb.Signature_MethodSignature{
							MethodSignature: &sdb.MethodSignature{
								ReturnType: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "scala/Unit#"}}},
								ParameterLists: []*sdb.Scope{{
									Hardlinks: []*sdb.SymbolInformation{
										{DisplayName: "x", Signature: &sdb.Signature{SealedValue: &sdb.Signature_ValueSignature{ValueSignature: &sdb.ValueSignature{Tpe: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "scala/Int#"}}}}}}},
										{DisplayName: "y", Signature: &sdb.Signature{SealedValue: &sdb.Signature_ValueSignature{ValueSignature: &sdb.ValueSignature{Tpe: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "java/lang/String#"}}}}}}},
									},
								}},
							},
						},
					},
				},
			},
		}},
	}
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "Foo.java.semanticdb"), data, 0644)
	idx.Load()

	// Open a file with uncompiled code that calls foo.bar(|).
	// No SemanticDB occurrence exists for this call site.
	callerURI := "file://" + tmpDir + "/src/Caller.java"
	callerContent := `package com.example;
public class Caller {
    void test() {
        Foo foo = new Foo();
        foo.bar();
    }
}`
	h.docs.Open(callerURI, callerContent)

	// Cursor inside bar() at line 4, character 17 (between the parens).
	params := SignatureHelpParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: callerURI},
			Position:     Position{Line: 4, Character: 17},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleSignatureHelp(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleSignatureHelp failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil SignatureHelp result")
	}

	sh := got.(SignatureHelp)
	if len(sh.Signatures) != 2 {
		t.Fatalf("expected 2 signatures (overloads), got %d", len(sh.Signatures))
	}

	// First overload: bar(int x)
	if len(sh.Signatures[0].Parameters) != 1 {
		t.Errorf("first overload: expected 1 param, got %d", len(sh.Signatures[0].Parameters))
	}
	// Second overload: bar(int x, String y)
	if len(sh.Signatures[1].Parameters) != 2 {
		t.Errorf("second overload: expected 2 params, got %d", len(sh.Signatures[1].Parameters))
	}
}

func TestCompletion_AllowsMultipleTypesWithSameSimpleName(t *testing.T) {
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
				{Symbol: "java/time/LocalDate#", DisplayName: "LocalDate", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "org/joda/time/LocalDate#", DisplayName: "LocalDate", Kind: sdb.SymbolInformation_CLASS},
			},
		}},
	}
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "Types.java.semanticdb"), data, 0644)
	idx.Load()

	fileURI := "file://" + tmpDir + "/src/Caller.java"
	content := `package com.example;
public class Caller {
    void test() {
        Loca
    }
}`
	h.docs.Open(fileURI, content)

	params := CompletionParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: fileURI},
			Position:     Position{Line: 3, Character: 12},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleCompletion(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleCompletion failed: %v", err)
	}

	list := got.(CompletionList)
	var localDates []CompletionItem
	for _, item := range list.Items {
		if item.Label == "LocalDate" {
			localDates = append(localDates, item)
		}
	}
	if len(localDates) != 2 {
		t.Fatalf("expected 2 LocalDate completion items, got %d: %+v", len(localDates), localDates)
	}

	details := map[string]bool{}
	for _, item := range localDates {
		details[item.Detail] = true
	}
	if !details["java.time.LocalDate"] {
		t.Fatalf("missing java.time.LocalDate completion item: %+v", localDates)
	}
	if !details["org.joda.time.LocalDate"] {
		t.Fatalf("missing org.joda.time.LocalDate completion item: %+v", localDates)
	}
}

func TestCompletion_PrefersJDKTypeOverThirdPartyForSameSimpleName(t *testing.T) {
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
				{Symbol: "java/time/LocalDate#", DisplayName: "LocalDate", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "org/joda/time/LocalDate#", DisplayName: "LocalDate", Kind: sdb.SymbolInformation_CLASS},
			},
		}},
	}
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "Types.java.semanticdb"), data, 0644)
	idx.Load()

	fileURI := "file://" + tmpDir + "/src/Caller.java"
	content := `package com.example;
public class Caller {
    void test() {
        Loca
    }
}`
	h.docs.Open(fileURI, content)

	params := CompletionParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: fileURI},
			Position:     Position{Line: 3, Character: 12},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleCompletion(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleCompletion failed: %v", err)
	}

	list := got.(CompletionList)
	var localDates []CompletionItem
	for _, item := range list.Items {
		if item.Label == "LocalDate" {
			localDates = append(localDates, item)
		}
	}
	if len(localDates) < 2 {
		t.Fatalf("expected at least 2 LocalDate completion items, got %d: %+v", len(localDates), localDates)
	}
	if localDates[0].Detail != "java.time.LocalDate" {
		t.Fatalf("first LocalDate detail = %q, want %q", localDates[0].Detail, "java.time.LocalDate")
	}
}

func TestCompletion_PrefersExplicitImportForSameSimpleName(t *testing.T) {
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
				{Symbol: "java/time/LocalDate#", DisplayName: "LocalDate", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "org/joda/time/LocalDate#", DisplayName: "LocalDate", Kind: sdb.SymbolInformation_CLASS},
			},
		}},
	}
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "Types.java.semanticdb"), data, 0644)
	idx.Load()

	fileURI := "file://" + tmpDir + "/src/Caller.java"
	content := `package com.example;
import org.joda.time.LocalDate;
public class Caller {
    void test() {
        Loca
    }
}`
	h.docs.Open(fileURI, content)

	params := CompletionParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: fileURI},
			Position:     Position{Line: 4, Character: 12},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleCompletion(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleCompletion failed: %v", err)
	}

	list := got.(CompletionList)
	var localDates []CompletionItem
	for _, item := range list.Items {
		if item.Label == "LocalDate" {
			localDates = append(localDates, item)
		}
	}
	if len(localDates) < 2 {
		t.Fatalf("expected at least 2 LocalDate completion items, got %d: %+v", len(localDates), localDates)
	}
	if localDates[0].AdditionalTextEdits != nil {
		t.Fatalf("expected imported LocalDate to need no additional import edit, got %+v", localDates[0].AdditionalTextEdits)
	}
}

func TestCompletion_ShowsOverloadedMethodsSeparately(t *testing.T) {
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
			Uri: "src/Foo.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "com/example/Foo#", DisplayName: "Foo", Kind: sdb.SymbolInformation_CLASS},
				{
					Symbol: "com/example/Foo#get().", DisplayName: "get", Kind: sdb.SymbolInformation_METHOD,
					Signature: &sdb.Signature{
						SealedValue: &sdb.Signature_MethodSignature{
							MethodSignature: &sdb.MethodSignature{
								ReturnType: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "java/lang/String#"}}},
							},
						},
					},
				},
				{
					Symbol: "com/example/Foo#get(+1).", DisplayName: "get", Kind: sdb.SymbolInformation_METHOD,
					Signature: &sdb.Signature{
						SealedValue: &sdb.Signature_MethodSignature{
							MethodSignature: &sdb.MethodSignature{
								ReturnType: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "java/lang/String#"}}},
								ParameterLists: []*sdb.Scope{{
									Hardlinks: []*sdb.SymbolInformation{
										{DisplayName: "name", Signature: &sdb.Signature{SealedValue: &sdb.Signature_ValueSignature{ValueSignature: &sdb.ValueSignature{Tpe: &sdb.Type{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "java/lang/String#"}}}}}}},
									},
								}},
							},
						},
					},
				},
			},
		}},
	}
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)
	data, _ := proto.Marshal(docs)
	os.WriteFile(filepath.Join(sdbDir, "Foo.java.semanticdb"), data, 0644)
	idx.Load()

	fileURI := "file://" + tmpDir + "/src/Caller.java"
	content := `package com.example;
public class Caller {
    void test() {
        Foo foo = new Foo();
        foo.get
    }
}`
	h.docs.Open(fileURI, content)

	params := CompletionParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: fileURI},
			Position:     Position{Line: 4, Character: 15},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleCompletion(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleCompletion failed: %v", err)
	}

	list := got.(CompletionList)
	var gets []CompletionItem
	for _, item := range list.Items {
		if strings.HasPrefix(item.Label, "get(") {
			gets = append(gets, item)
		}
	}
	if len(gets) != 2 {
		t.Fatalf("expected 2 get completion items, got %d: %+v", len(gets), gets)
	}

	insertTexts := map[string]bool{}
	for _, item := range gets {
		insertTexts[item.InsertText] = true
	}
	if !insertTexts["get()$0"] {
		t.Fatalf("missing zero-arg get completion: %+v", gets)
	}
	if !insertTexts["get(${1:name})$0"] {
		t.Fatalf("missing one-arg get completion: %+v", gets)
	}
}

func TestCompletion_ShowsLocalOverloadedMethodsSeparately(t *testing.T) {
	tmpDir := t.TempDir()
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	h := NewHandler(logger, jsonrpc.NewTransport(&bytes.Buffer{}, &bytes.Buffer{}))

	idx := index.NewIndex(logger, tmpDir)
	defer idx.Close()
	close(h.indexReady)
	h.idx = idx
	h.rootURI = "file://" + tmpDir

	fileURI := "file://" + tmpDir + "/src/Caller.java"
	content := `package com.example;
public class Caller {
    void run() {
        wor
    }
    void work() {}
    void work(String name) {}
}`
	h.docs.Open(fileURI, content)

	params := CompletionParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: fileURI},
			Position:     Position{Line: 3, Character: 11},
		},
	}
	rawParams, _ := json.Marshal(params)

	got, err := h.handleCompletion(context.Background(), rawParams)
	if err != nil {
		t.Fatalf("handleCompletion failed: %v", err)
	}

	list := got.(CompletionList)
	var works []CompletionItem
	for _, item := range list.Items {
		if strings.HasPrefix(item.Label, "work(") {
			works = append(works, item)
		}
	}
	if len(works) != 2 {
		t.Fatalf("expected 2 work completion items, got %d: %+v", len(works), works)
	}

	insertTexts := map[string]bool{}
	for _, item := range works {
		insertTexts[item.InsertText] = true
	}
	if !insertTexts["work()$0"] {
		t.Fatalf("missing zero-arg work completion: %+v", works)
	}
	if !insertTexts["work(${1:name})$0"] {
		t.Fatalf("missing one-arg work completion: %+v", works)
	}
}

func TestDiagnosticsClearing(t *testing.T) {
	var output bytes.Buffer
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	transport := jsonrpc.NewTransport(&bytes.Buffer{}, &output)
	h := NewHandler(logger, transport)

	docURI := "file:///project/Main.java"

	// 1. Send some diagnostics
	h.handleBSPDiagnostics(bsp.PublishDiagnosticsParams{
		TextDocument: bsp.TextDocumentIdentifier{URI: docURI},
		Reset:        true,
		Diagnostics: []bsp.Diagnostic{
			{
				Range:    bsp.Range{Start: bsp.Position{Line: 1, Character: 1}, End: bsp.Position{Line: 1, Character: 5}},
				Severity: 1,
				Message:  "Error 1",
			},
		},
	})

	// 2. Clear diagnostics
	h.handleBSPDiagnostics(bsp.PublishDiagnosticsParams{
		TextDocument: bsp.TextDocumentIdentifier{URI: docURI},
		Reset:        true,
		Diagnostics:  []bsp.Diagnostic{},
	})

	// Check output
	outTransport := jsonrpc.NewTransport(&output, nil)
	
	// Skip first notification
	_, err := outTransport.Read()
	if err != nil {
		t.Fatalf("reading first notification: %v", err)
	}

	// Read second notification
	msg, err := outTransport.Read()
	if err != nil {
		t.Fatalf("reading second notification: %v", err)
	}

	if msg.Method != "textDocument/publishDiagnostics" {
		t.Fatalf("expected textDocument/publishDiagnostics, got %s", msg.Method)
	}

	var params PublishDiagnosticsParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("decoding params: %v", err)
	}

	if len(params.Diagnostics) != 0 {
		t.Errorf("expected 0 diagnostics, got %d", len(params.Diagnostics))
	}

	// Verify if it's "null" or "[]" in raw JSON
	if strings.Contains(string(msg.Params), ":null") {
		t.Errorf("JSON contains ':null', expected '[]' for diagnostics: %s", string(msg.Params))
	}
}

func TestDiagnosticsMerging(t *testing.T) {
	var output bytes.Buffer
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	transport := jsonrpc.NewTransport(&bytes.Buffer{}, &output)
	h := NewHandler(logger, transport)

	docURI := "file:///project/Main.java"

	// 1. Target A reports an error
	h.handleBSPDiagnostics(bsp.PublishDiagnosticsParams{
		TextDocument: bsp.TextDocumentIdentifier{URI: docURI},
		BuildTarget:  bsp.BuildTargetIdentifier{URI: "target-A"},
		Reset:        true,
		Diagnostics: []bsp.Diagnostic{
			{
				Range:    bsp.Range{Start: bsp.Position{Line: 1, Character: 1}, End: bsp.Position{Line: 1, Character: 5}},
				Severity: 1,
				Message:  "Error from A",
			},
		},
	})

	// 2. Target B reports another error
	h.handleBSPDiagnostics(bsp.PublishDiagnosticsParams{
		TextDocument: bsp.TextDocumentIdentifier{URI: docURI},
		BuildTarget:  bsp.BuildTargetIdentifier{URI: "target-B"},
		Reset:        true,
		Diagnostics: []bsp.Diagnostic{
			{
				Range:    bsp.Range{Start: bsp.Position{Line: 2, Character: 1}, End: bsp.Position{Line: 2, Character: 5}},
				Severity: 1,
				Message:  "Error from B",
			},
		},
	})

	// Check output
	outTransport := jsonrpc.NewTransport(&output, nil)
	
	// Skip first notification (only A)
	_, _ = outTransport.Read()

	// Read second notification (A + B)
	msg, err := outTransport.Read()
	if err != nil {
		t.Fatalf("reading second notification: %v", err)
	}

	var params PublishDiagnosticsParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("decoding params: %v", err)
	}

	if len(params.Diagnostics) != 2 {
		t.Errorf("expected 2 diagnostics (merged from A and B), got %d", len(params.Diagnostics))
	}

	// 3. Clear Target A
	h.handleBSPDiagnostics(bsp.PublishDiagnosticsParams{
		TextDocument: bsp.TextDocumentIdentifier{URI: docURI},
		BuildTarget:  bsp.BuildTargetIdentifier{URI: "target-A"},
		Reset:        true,
		Diagnostics:  []bsp.Diagnostic{},
	})

	// Read third notification (only B remains)
	msg, err = outTransport.Read()
	if err != nil {
		t.Fatalf("reading third notification: %v", err)
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("decoding params: %v", err)
	}
	if len(params.Diagnostics) != 1 {
		t.Errorf("expected 1 diagnostic (only B remains), got %d", len(params.Diagnostics))
	}
}
