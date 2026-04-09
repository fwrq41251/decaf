package lsp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fwrq41251/decaf/internal/index"
)

func (h *Handler) handleDefinition(ctx context.Context, params json.RawMessage) (any, error) {
	var p TextDocumentPositionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	select {
	case <-h.indexReady:
		// Index is ready.
	default:
		h.logger.Printf("Definition request: waiting for initial index load...")
		if !h.waitIndexReady(ctx) {
			h.logger.Printf("Definition request: index not ready, cancelled.")
			return []LSPLocation{}, nil
		}
	}

	defs := h.idx.Definition(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	locations := make([]LSPLocation, 0, len(defs))
	for _, d := range defs {
		if d.Range.IsEmpty() {
			continue
		}
		locations = append(locations, LSPLocation{
			URI: h.toFileURI(d.URI),
			Range: Range{
				Start: Position{Line: int(d.Range.StartLine), Character: int(d.Range.StartCharacter)},
				End:   Position{Line: int(d.Range.EndLine), Character: int(d.Range.EndCharacter)},
			},
		})
	}

	h.logger.Printf("definition at %s:%d:%d -> %d results",
		p.TextDocument.URI, p.Position.Line, p.Position.Character, len(locations))
	return locations, nil
}

func (h *Handler) handleReferences(ctx context.Context, params json.RawMessage) (any, error) {
	var p ReferenceParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return []LSPLocation{}, nil
	}

	refs := h.idx.References(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	
	// If includeDeclaration is true, add the definition to the results.
	if p.Context.IncludeDeclaration {
		defs := h.idx.Definition(p.TextDocument.URI, p.Position.Line, p.Position.Character)
		for _, d := range defs {
			if d.Range.IsEmpty() {
				continue
			}
			// Convert Symbol to Occurrence-like structure for the loop below.
			refs = append(refs, index.Occurrence{
				URI:   d.URI,
				Range: d.Range,
			})
		}
		// Re-deduplicate since definition might already be in references or multiple files.
		// Note: We'd need to expose deduplicateOccurrences if we wanted to call it here, 
		// but the loop below already converts to LSPLocation, so we can just let the client 
		// handle it or deduplicate the final locations array.
	}

	locations := make([]LSPLocation, 0, len(refs))
	seen := make(map[string]bool)
	for _, r := range refs {
		if r.Range.IsEmpty() {
			continue
		}
		loc := LSPLocation{
			URI: h.toFileURI(r.URI),
			Range: Range{
				Start: Position{Line: int(r.Range.StartLine), Character: int(r.Range.StartCharacter)},
				End:   Position{Line: int(r.Range.EndLine), Character: int(r.Range.EndCharacter)},
			},
		}
		// Final deduplication of LSP locations
		key := fmt.Sprintf("%s:%d:%d-%d:%d", loc.URI, loc.Range.Start.Line, loc.Range.Start.Character, loc.Range.End.Line, loc.Range.End.Character)
		if !seen[key] {
			seen[key] = true
			locations = append(locations, loc)
		}
	}

	h.logger.Printf("references at %s:%d:%d -> %d results",
		p.TextDocument.URI, p.Position.Line, p.Position.Character, len(locations))
	return locations, nil
}

func (h *Handler) handleHover(ctx context.Context, params json.RawMessage) (any, error) {
	var p TextDocumentPositionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return nil, nil
	}

	sym := h.idx.Hover(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	if sym == nil {
		return nil, nil
	}

	content := formatHover(sym, h.idx)
	result := HoverResult{
		Contents: MarkupContent{
			Kind:  "markdown",
			Value: content,
		},
	}

	return result, nil
}

func (h *Handler) handleDocumentSymbol(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		TextDocument TextDocumentIdentifier `json:"textDocument"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return []DocumentSymbol{}, nil
	}

	symbols := h.idx.FileSymbols(p.TextDocument.URI)
	result := buildDocumentSymbols(symbols)
	return result, nil
}

func (h *Handler) handleDocumentHighlight(ctx context.Context, params json.RawMessage) (any, error) {
	var p TextDocumentPositionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return []DocumentHighlight{}, nil
	}

	occs := h.idx.FileOccurrencesOf(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	highlights := make([]DocumentHighlight, 0, len(occs))
	for _, occ := range occs {
		if occ.Range.IsEmpty() {
			continue
		}
		highlights = append(highlights, DocumentHighlight{
			Range: sdbRangeToLSP(occ.Range),
			Kind:  HighlightText,
		})
	}

	return highlights, nil
}

func (h *Handler) handleImplementation(ctx context.Context, params json.RawMessage) (any, error) {
	var p TextDocumentPositionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return []LSPLocation{}, nil
	}

	impls := h.idx.Implementations(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	locations := make([]LSPLocation, 0, len(impls))
	for _, d := range impls {
		if d.Range.IsEmpty() {
			continue
		}
		locations = append(locations, LSPLocation{
			URI:   h.toFileURI(d.URI),
			Range: sdbRangeToLSP(d.Range),
		})
	}

	return locations, nil
}

func (h *Handler) handleWorkspaceSymbol(ctx context.Context, params json.RawMessage) (any, error) {
	var p WorkspaceSymbolParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if p.Query == "" || !h.waitIndexReady(ctx) {
		return []SymbolInformation{}, nil
	}

	symbols := h.idx.SearchSymbols(p.Query)
	result := make([]SymbolInformation, 0, len(symbols))
	for _, s := range symbols {
		if s.Range.IsEmpty() {
			continue
		}
		result = append(result, SymbolInformation{
			Name: s.Name,
			Kind: sdbKindToLSP(s.Kind),
			Location: LSPLocation{
				URI:   h.toFileURI(s.URI),
				Range: sdbRangeToLSP(s.Range),
			},
		})
	}

	return result, nil
}
