package lsp

import (
	"bytes"
	"context"
	"log"
	"testing"

	"github.com/fwrq41251/decaf/internal/jsonrpc"
)

func TestRunCompileCycleSkipsWhenBSPNotReady(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	h := NewHandler(logger, jsonrpc.NewTransport(&bytes.Buffer{}, &bytes.Buffer{}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h.workspace.bgMu.Lock()
	h.workspace.backgroundCtx = ctx
	h.workspace.bgMu.Unlock()

	h.workspace.debounceMu.Lock()
	h.workspace.pendingURIs = []string{"file:///tmp/Test.java"}
	h.workspace.debounceMu.Unlock()

	h.workspace.runCompileCycle()

	h.workspace.debounceMu.Lock()
	defer h.workspace.debounceMu.Unlock()
	if len(h.workspace.pendingURIs) != 0 {
		t.Fatalf("pendingURIs = %v, want empty after skipped compile cycle", h.workspace.pendingURIs)
	}
}
