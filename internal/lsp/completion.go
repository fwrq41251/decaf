package lsp

import (
	"context"
	"encoding/json"
	"strings"
)

func (h *Handler) handleCompletion(_ context.Context, params json.RawMessage) (any, error) {
	var p CompletionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return CompletionList{}, nil
	}

	// Extract the word prefix at the cursor position from the overlay buffer.
	prefix := ""
	if p.Context == nil || p.Context.TriggerCharacter != "." {
		prefix = h.wordPrefixAt(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	}

	symbols := h.idx.CompletionSymbols(p.TextDocument.URI, prefix)
	items := make([]CompletionItem, 0, len(symbols))
	for _, s := range symbols {
		item := CompletionItem{
			Label:      s.Name,
			Kind:       sdbKindToCompletionKind(s.Kind),
			InsertText: s.Name,
		}
		if s.Signature != nil {
			item.Detail = s.Signature.Label
		}
		items = append(items, item)
	}

	h.logger.Printf("completion at %s:%d:%d prefix=%q -> %d items",
		p.TextDocument.URI, p.Position.Line, p.Position.Character, prefix, len(items))
	return CompletionList{IsIncomplete: true, Items: items}, nil
}

func (h *Handler) handleSignatureHelp(_ context.Context, params json.RawMessage) (any, error) {
	var p SignatureHelpParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return nil, nil
	}

	sym := h.idx.SymbolSignature(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	if sym == nil || sym.Signature == nil {
		return nil, nil
	}

	sigInfo := formatSignatureHelp(sym)
	if sigInfo == nil {
		return nil, nil
	}

	activeParam := h.countActiveParameter(p.TextDocument.URI, p.Position.Line, p.Position.Character)

	return SignatureHelp{
		Signatures:      []SignatureInformation{*sigInfo},
		ActiveSignature: 0,
		ActiveParameter: activeParam,
	}, nil
}

// wordPrefixAt extracts the Java identifier prefix ending at the given cursor
// position from the overlay (or disk). E.g. if the line is "  ArrayLi|" with
// cursor at position 9, it returns "ArrayLi".
func (h *Handler) wordPrefixAt(fileURI string, line, character int) string {
	content := h.getFileContent(fileURI)
	if content == "" {
		return ""
	}

	// Find the target line.
	cur := 0
	for l := 0; l < line; l++ {
		idx := strings.IndexByte(content[cur:], '\n')
		if idx < 0 {
			return ""
		}
		cur += idx + 1
	}

	lineEnd := strings.IndexByte(content[cur:], '\n')
	if lineEnd < 0 {
		lineEnd = len(content) - cur
	}
	lineText := content[cur : cur+lineEnd]

	byteOff := utf16Index(lineText, character)

	// Walk backwards from cursor to find the start of the identifier.
	start := byteOff
	for start > 0 {
		ch := lineText[start-1]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '$' {
			start--
		} else {
			break
		}
	}

	return lineText[start:byteOff]
}

// countActiveParameter counts the number of commas between the nearest unmatched
// opening parenthesis and the cursor position, using the overlay buffer content.
// This determines which parameter is "active" for signature help.
func (h *Handler) countActiveParameter(fileURI string, line, character int) int {
	content := h.getFileContent(fileURI)
	if content == "" {
		return 0
	}

	// Compute the absolute byte offset of the cursor in the full content.
	cur := 0
	for l := 0; l < line; l++ {
		idx := strings.IndexByte(content[cur:], '\n')
		if idx < 0 {
			return 0
		}
		cur += idx + 1
	}

	lineEnd := strings.IndexByte(content[cur:], '\n')
	if lineEnd < 0 {
		lineEnd = len(content) - cur
	}
	lineText := content[cur : cur+lineEnd]
	absOff := cur + utf16Index(lineText, character)

	// Walk backwards through the entire file from cursor to find the
	// nearest unmatched opening parenthesis, counting commas at the
	// top nesting level. This handles multi-line method calls.
	commas := 0
	depth := 0
	for i := absOff - 1; i >= 0; i-- {
		switch content[i] {
		case ')':
			depth++
		case '(':
			if depth == 0 {
				return commas
			}
			depth--
		case ',':
			if depth == 0 {
				commas++
			}
		}
	}
	return commas
}
