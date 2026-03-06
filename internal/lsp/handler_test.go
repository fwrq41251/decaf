package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"testing"

	"github.com/fwrq41251/decaf/internal/jsonrpc"
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

func intPtr(n int) *int { return &n }

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
