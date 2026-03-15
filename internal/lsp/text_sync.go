package lsp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/fwrq41251/decaf/internal/uri"
)

func (h *Handler) handleDidOpen(_ context.Context, params json.RawMessage) (any, error) {
	var p DidOpenTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	h.docs.Open(p.TextDocument.URI, p.TextDocument.Text)
	h.logger.Printf("didOpen: %s (version %d, %d bytes)", p.TextDocument.URI, p.TextDocument.Version, len(p.TextDocument.Text))
	return nil, nil
}

func (h *Handler) handleDidChange(_ context.Context, params json.RawMessage) (any, error) {
	var p DidChangeTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	h.docs.ApplyChanges(p.TextDocument.URI, p.ContentChanges)
	h.logger.Printf("didChange: %s (version %d, %d changes)", p.TextDocument.URI, p.TextDocument.Version, len(p.ContentChanges))
	h.scheduleCompile()
	return nil, nil
}

func (h *Handler) handleDidSave(ctx context.Context, params json.RawMessage) (any, error) {
	var p DidSaveTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	h.logger.Printf("didSave: %s — triggering compile", p.TextDocument.URI)

	go func() {
		prog := h.beginProgress("decaf", "compiling…")
		if err := h.bspClient.Compile(ctx); err != nil {
			h.logger.Printf("compile on save failed: %v", err)
			prog.end("compilation failed")
			return
		}
		prog.report("indexing…", nil)
		h.reindex()
		prog.end("done")
	}()

	return nil, nil
}

func (h *Handler) handleDidClose(_ context.Context, params json.RawMessage) (any, error) {
	var p DidCloseTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	h.docs.Close(p.TextDocument.URI)
	h.logger.Printf("didClose: %s", p.TextDocument.URI)
	return nil, nil
}

func (h *Handler) handleDidChangeWatchedFiles(_ context.Context, params json.RawMessage) (any, error) {
	var p DidChangeWatchedFilesParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	javaChanged := 0
	for _, e := range p.Changes {
		if strings.HasSuffix(e.URI, ".java") {
			javaChanged++
		}
	}

	if javaChanged == 0 {
		return nil, nil
	}

	h.logger.Printf("watched files changed: %d java file(s)", javaChanged)
	h.scheduleCompile()
	return nil, nil
}

// scheduleCompile debounces compilation — waits 500ms after the last call
// before triggering a compile + reindex cycle.
func (h *Handler) scheduleCompile() {
	h.debounceMu.Lock()
	defer h.debounceMu.Unlock()

	if h.debounceTimer != nil {
		h.debounceTimer.Stop()
	}

	h.debounceTimer = time.AfterFunc(500*time.Millisecond, func() {
		ctx := h.backgroundCtx
		if ctx == nil {
			return
		}
		h.flushOverlays()
		prog := h.beginProgress("decaf", "compiling…")
		if err := h.bspClient.Compile(ctx); err != nil {
			h.logger.Printf("compile on file change failed: %v", err)
			prog.end("compilation failed")
			return
		}
		prog.report("indexing…", nil)
		h.reindex()
		prog.end("done")
	})
}

// flushOverlays writes all open document overlays to their file paths on disk
// so that Bloop (which compiles from the filesystem) sees the latest buffer content.
func (h *Handler) flushOverlays() {
	for docURI, content := range h.docs.Snapshot() {
		filePath := uri.ToPath(docURI)
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			h.logger.Printf("failed to flush overlay for %s: %v", docURI, err)
		}
	}
}
