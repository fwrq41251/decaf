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

	h.bgMu.Lock()
	h.backgroundCtx = ctx
	h.bgMu.Unlock()

	h.debounceMu.Lock()
	h.pendingURIs = []string{"file:///tmp/Test.java"}
	h.debounceMu.Unlock()

	h.runCompileCycle()

	h.debounceMu.Lock()
	defer h.debounceMu.Unlock()
	if len(h.pendingURIs) != 0 {
		t.Fatalf("pendingURIs = %v, want empty after skipped compile cycle", h.pendingURIs)
	}
}
