package lsp

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/fwrq41251/decaf/internal/bsp"
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
	h.scheduleCompile(p.TextDocument.URI)
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

	var uris []string
	for _, e := range p.Changes {
		if strings.HasSuffix(e.URI, ".java") {
			uris = append(uris, e.URI)
		}
	}
	h.logger.Printf("watched files changed: %d java file(s)", javaChanged)
	h.scheduleCompile(uris...)
	return nil, nil
}

// scheduleCompile debounces compilation — waits 500ms after the last call
// before triggering a compile + reindex cycle.
// If file URIs are provided, only the build targets owning those files are compiled.
func (h *Handler) scheduleCompile(uris ...string) {
	h.debounceMu.Lock()
	defer h.debounceMu.Unlock()

	// Merge URIs across debounced calls.
	h.pendingURIs = append(h.pendingURIs, uris...)

	if h.debounceTimer != nil {
		h.debounceTimer.Stop()
	}

	h.debounceTimer = time.AfterFunc(500*time.Millisecond, func() {
		totalStart := time.Now()

		h.compileMu.Lock()
		defer h.compileMu.Unlock()
		h.logger.Printf("[timing] acquired compileMu after %v", time.Since(totalStart))

		h.bgMu.Lock()
		ctx := h.backgroundCtx
		h.bgMu.Unlock()
		if ctx == nil {
			return
		}

		// Collect and clear pending URIs.
		h.debounceMu.Lock()
		changedURIs := h.pendingURIs
		h.pendingURIs = nil
		h.debounceMu.Unlock()

		prog := h.beginProgress("decaf", "compiling…")

		compiled := false
		if len(changedURIs) > 0 {
			t0 := time.Now()
			targets := h.resolveTargets(ctx, changedURIs)
			h.logger.Printf("[timing] resolveTargets (%d URIs -> %d targets) took %v", len(changedURIs), len(targets), time.Since(t0))
			if len(targets) > 0 {
				t1 := time.Now()
				err := h.bspClient.CompileTargets(ctx, targets)
				h.logger.Printf("[timing] CompileTargets took %v", time.Since(t1))
				if err != nil {
					h.logger.Printf("compile on file change failed: %v", err)
				}
				compiled = true
			}
		}

		// Fall back to full compile if we couldn't resolve targets.
		if !compiled {
			t1 := time.Now()
			if err := h.bspClient.Compile(ctx); err != nil {
				h.logger.Printf("compile on file change failed: %v", err)
			}
			h.logger.Printf("[timing] full Compile took %v", time.Since(t1))
		}

		// Always reindex — even partial compilation produces updated semanticdb.
		prog.report("indexing…", nil)
		t2 := time.Now()
		h.reindex()
		h.logger.Printf("[timing] reindex took %v", time.Since(t2))

		prog.end("done")
		h.logger.Printf("[timing] total compile+reindex cycle took %v", time.Since(totalStart))
	})
}

// resolveTargets uses inverseSources to find build targets for the given file URIs,
// deduplicating the results.
func (h *Handler) resolveTargets(ctx context.Context, uris []string) []bsp.BuildTargetIdentifier {
	seen := make(map[string]struct{})
	var targets []bsp.BuildTargetIdentifier
	for _, u := range uris {
		ts, err := h.bspClient.InverseSources(ctx, u)
		if err != nil {
			h.logger.Printf("inverseSources failed for %s: %v", u, err)
			return nil // fall back to full compile
		}
		for _, t := range ts {
			if _, ok := seen[t.URI]; !ok {
				seen[t.URI] = struct{}{}
				targets = append(targets, t)
			}
		}
	}
	return targets
}

