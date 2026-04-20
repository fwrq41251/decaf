package lsp

import (
	"context"
	"encoding/json"
	"strings"
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
	return nil, nil
}

func (h *Handler) handleDidSave(ctx context.Context, params json.RawMessage) (any, error) {
	var p DidSaveTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	h.logger.Printf("didSave: %s — triggering compile", p.TextDocument.URI)
	h.workspace.scheduleCompile(p.TextDocument.URI)
	return nil, nil
}

func (h *Handler) handleDidClose(_ context.Context, params json.RawMessage) (any, error) {
	var p DidCloseTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	h.docs.Close(p.TextDocument.URI)
	h.clearDiagnostics(p.TextDocument.URI)
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

	var uris []string
	for _, e := range p.Changes {
		if !strings.HasSuffix(e.URI, ".java") {
			continue
		}
		uris = append(uris, e.URI)
		if e.Type == FileChangeDeleted {
			h.clearDiagnostics(e.URI)
		}
	}
	h.logger.Printf("watched files changed: %d java file(s)", javaChanged)
	h.workspace.scheduleCompile(uris...)
	return nil, nil
}
