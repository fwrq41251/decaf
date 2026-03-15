package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (h *Handler) handleCodeAction(_ context.Context, params json.RawMessage) (any, error) {
	var p CodeActionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return []CodeAction{}, nil
	}

	var actions []CodeAction

	// Check which code action kinds are requested.
	wantOrganize := len(p.Context.Only) == 0
	wantQuickFix := len(p.Context.Only) == 0
	for _, kind := range p.Context.Only {
		switch kind {
		case CodeActionSourceOrganizeImports, "source":
			wantOrganize = true
		case CodeActionQuickFix:
			wantQuickFix = true
		}
	}

	// Organize imports.
	if wantOrganize {
		overlay, _ := h.docs.Get(p.TextDocument.URI)
		edit := organizeImports(p.TextDocument.URI, h.idx, overlay)
		if edit != nil {
			actions = append(actions, CodeAction{
				Title: "Organize Imports",
				Kind:  CodeActionSourceOrganizeImports,
				Edit:  edit,
			})
		}
	}

	// Quick fix: add missing import.
	if wantQuickFix {
		overlay, _ := h.docs.Get(p.TextDocument.URI)
		for _, diag := range p.Context.Diagnostics {
			name := extractMissingSymbolName(diag.Message)
			if name == "" {
				continue
			}
			candidates := h.idx.SearchSymbols(name)
			for _, sym := range candidates {
				if sym.Name != name {
					continue
				}
				fqn := fqnFromSymbol(sym.Symbol)
				if fqn == "" {
					continue
				}
				edit := addImportEdit(p.TextDocument.URI, overlay, fqn)
				if edit == nil {
					continue
				}
				actions = append(actions, CodeAction{
					Title:       fmt.Sprintf("Add import '%s'", fqn),
					Kind:        CodeActionQuickFix,
					Diagnostics: []Diagnostic{diag},
					Edit:        edit,
				})
			}
		}
	}

	if len(actions) == 0 {
		return []CodeAction{}, nil
	}
	return actions, nil
}

func (h *Handler) handleRename(_ context.Context, params json.RawMessage) (any, error) {
	var p RenameParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return nil, nil
	}

	_, occs := h.idx.RenameOccurrences(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	if len(occs) == 0 {
		return nil, nil
	}

	changes := make(map[string][]TextEdit)
	for _, occ := range occs {
		if occ.Range == nil {
			continue
		}
		uri := h.toFileURI(occ.URI)
		changes[uri] = append(changes[uri], TextEdit{
			Range:   sdbRangeToLSP(occ.Range),
			NewText: p.NewName,
		})
	}

	h.logger.Printf("rename at %s:%d:%d -> %d files affected",
		p.TextDocument.URI, p.Position.Line, p.Position.Character, len(changes))
	return WorkspaceEdit{Changes: changes}, nil
}

func (h *Handler) handlePrepareRename(_ context.Context, params json.RawMessage) (any, error) {
	var p PrepareRenameParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return nil, nil
	}

	sym := h.idx.Hover(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	if sym == nil || sym.Range == nil {
		return nil, nil
	}

	return map[string]any{
		"range":       sdbRangeToLSP(sym.Range),
		"placeholder": sym.Name,
	}, nil
}

// extractMissingSymbolName extracts the class/type name from a "cannot find symbol"
// diagnostic message. Typical format:
//
//	"cannot find symbol\n  symbol:   class Foo"
//	"cannot find symbol: class Foo"
func extractMissingSymbolName(msg string) string {
	if !strings.Contains(msg, "cannot find symbol") {
		return ""
	}
	idx := strings.Index(msg, "symbol:")
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(msg[idx+len("symbol:"):])
	// Skip the kind keyword (class, interface, variable, etc.)
	parts := strings.Fields(rest)
	if len(parts) >= 2 {
		return parts[1]
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return ""
}
