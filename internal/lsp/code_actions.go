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
	wantSource := len(p.Context.Only) == 0
	for _, kind := range p.Context.Only {
		switch kind {
		case CodeActionSourceOrganizeImports:
			wantOrganize = true
		case "source":
			wantOrganize = true
			wantSource = true
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

	// Quick fix: add missing import / implement methods.
	if wantQuickFix {
		overlay, _ := h.docs.Get(p.TextDocument.URI)
		for _, diag := range p.Context.Diagnostics {
			// Add missing import.
			name := extractMissingSymbolName(diag.Message)
			if name != "" {
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

			// Implement abstract methods.
			if edit := implementMethodsEdit(p.TextDocument.URI, h.idx, overlay, diag); edit != nil {
				actions = append(actions, CodeAction{
					Title:       "Implement abstract methods",
					Kind:        CodeActionQuickFix,
					Diagnostics: []Diagnostic{diag},
					Edit:        edit,
				})
			}
		}
	}

	// Source actions: generate constructor, override method.
	if wantSource {
		overlay, _ := h.docs.Get(p.TextDocument.URI)
		cursorLine := p.Range.Start.Line

		if action := initializeJavaTypeAction(p.TextDocument.URI, overlay); action != nil {
			actions = append(actions, *action)
		}

		if edit := implementMethodsSourceEdit(p.TextDocument.URI, h.idx, overlay, cursorLine); edit != nil {
			actions = append(actions, CodeAction{
				Title: "Implement abstract methods",
				Kind:  "source",
				Edit:  edit,
			})
		}

		if edit := generateConstructorEdit(p.TextDocument.URI, h.idx, overlay, cursorLine); edit != nil {
			actions = append(actions, CodeAction{
				Title: "Generate constructor",
				Kind:  "source",
				Edit:  edit,
			})
		}

		if action := overrideMethodAction(p.TextDocument.URI, h.idx, overlay, cursorLine); action != nil {
			actions = append(actions, *action)
		}

		candidates := collectFieldCandidates(p.TextDocument.URI, h.idx, overlay, cursorLine)

		if action := getterAction(p.TextDocument.URI, cursorLine, candidates); action != nil {
			actions = append(actions, *action)
		}

		if action := setterAction(p.TextDocument.URI, cursorLine, candidates); action != nil {
			actions = append(actions, *action)
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

// extractMissingSymbolName extracts a type/class name from a compiler diagnostic
// message that can be fixed by adding an import statement.
//
// Supported formats:
//
//	"cannot find symbol\n  symbol:   class Foo"
//	"cannot find symbol: class Foo"
//	"Foo cannot be resolved to a type"            (Eclipse ecj)
//	"Foo cannot be resolved"                      (Eclipse ecj)
func extractMissingSymbolName(msg string) string {
	// javac: "cannot find symbol" with "symbol: class/variable Foo"
	if strings.Contains(msg, "cannot find symbol") {
		idx := strings.Index(msg, "symbol:")
		if idx < 0 {
			return ""
		}
		rest := strings.TrimSpace(msg[idx+len("symbol:"):])
		parts := strings.Fields(rest)
		if len(parts) >= 2 {
			return parts[1]
		}
		if len(parts) == 1 {
			return parts[0]
		}
		return ""
	}

	// Eclipse ecj: "Foo cannot be resolved to a type" or "Foo cannot be resolved"
	if idx := strings.Index(msg, " cannot be resolved"); idx > 0 {
		name := strings.TrimSpace(msg[:idx])
		if len(name) > 0 && isJavaIdentifier(name) {
			return name
		}
	}

	return ""
}

// isJavaIdentifier returns true if s is a valid Java simple identifier (no dots/spaces).
func isJavaIdentifier(s string) bool {
	for i, c := range s {
		if i == 0 {
			if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_' || c == '$') {
				return false
			}
		} else {
			if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '$') {
				return false
			}
		}
	}
	return len(s) > 0
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
