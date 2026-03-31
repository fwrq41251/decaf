package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

func (h *Handler) handleCodeAction(ctx context.Context, params json.RawMessage) (any, error) {
	var p CodeActionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
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

func (h *Handler) handleRename(ctx context.Context, params json.RawMessage) (any, error) {
	var p RenameParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return nil, nil
	}

	sym, occs := h.idx.RenameOccurrences(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	if len(occs) == 0 {
		return nil, nil
	}

	changes := make(map[string][]TextEdit)
	for _, occ := range occs {
		if occ.Range.IsEmpty() {
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

	// Check if this is a top-level public class rename that requires a file rename.
	if isTopLevelClass(sym) {
		oldName := index.ExtractShortName(sym)
		// Find the definition file URI.
		var defURI string
		for _, occ := range occs {
			if occ.Role == sdb.SymbolOccurrence_DEFINITION {
				defURI = h.toFileURI(occ.URI)
				break
			}
		}
		// Check if the file basename matches the old class name.
		if defURI != "" {
			lastSlash := strings.LastIndex(defURI, "/")
			baseName := defURI[lastSlash+1:]
			if baseName == oldName+".java" && clientSupportsRename(h.clientCaps) {
				newURI := defURI[:lastSlash+1] + p.NewName + ".java"

				var docChanges []DocumentChange
				for fileURI, edits := range changes {
					docChanges = append(docChanges, NewTextDocumentEditChange(TextDocumentEdit{
						TextDocument: OptionalVersionedTextDocumentIdentifier{URI: fileURI},
						Edits:        edits,
					}))
				}
				docChanges = append(docChanges, NewRenameFileChange(RenameFile{
					Kind:   "rename",
					OldURI: defURI,
					NewURI: newURI,
				}))

				return WorkspaceEdit{DocumentChanges: docChanges}, nil
			}
		}
	}

	return WorkspaceEdit{Changes: changes}, nil
}

func (h *Handler) handlePrepareRename(ctx context.Context, params json.RawMessage) (any, error) {
	var p PrepareRenameParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return nil, nil
	}

	occ := h.idx.OccurrenceAt(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	if occ == nil || occ.Range.IsEmpty() {
		return nil, nil
	}

	return map[string]any{
		"range":       sdbRangeToLSP(occ.Range),
		"placeholder": index.ExtractShortName(occ.Symbol),
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

// isTopLevelClass returns true if sym is a SemanticDB top-level class symbol
// (e.g. "com/example/Foo#" — exactly one '#' at the end, no nested '#').
func isTopLevelClass(sym string) bool {
	return strings.HasSuffix(sym, "#") && strings.Count(sym, "#") == 1
}

// clientSupportsRename checks whether the client advertises the "rename"
// resource operation in its workspace edit capabilities.
func clientSupportsRename(caps ClientCapabilities) bool {
	if caps.Workspace == nil || caps.Workspace.WorkspaceEdit == nil {
		return false
	}
	for _, op := range caps.Workspace.WorkspaceEdit.ResourceOperations {
		if op == "rename" {
			return true
		}
	}
	return false
}
