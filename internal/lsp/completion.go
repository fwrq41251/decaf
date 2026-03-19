package lsp

import (
	"context"
	"encoding/json"
	"strings"
)

func (h *Handler) handleCompletion(ctx context.Context, params json.RawMessage) (any, error) {
	var p CompletionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return CompletionList{}, nil
	}

	// Get the current buffer content (overlay or disk).
	content := h.getFileContent(p.TextDocument.URI)
	if content == "" {
		return CompletionList{}, nil
	}

	// Parse completion context using Tree-sitter.
	cctx := parseCompletionCtx([]byte(content), p.Position.Line, p.Position.Character)

	var items []CompletionItem

	if cctx.Kind == CompletionDot {
		items = h.completeDot(cctx, p.TextDocument.URI)
	} else {
		items = h.completeLexical(cctx, p.TextDocument.URI)
	}

	h.logger.Printf("completion at %s:%d:%d kind=%d prefix=%q receiver=%q -> %d items",
		p.TextDocument.URI, p.Position.Line, p.Position.Character,
		cctx.Kind, cctx.Prefix, cctx.Receiver, len(items))
	return CompletionList{IsIncomplete: len(items) >= 100, Items: items}, nil
}

// completeDot handles member completion after a dot (e.g. "foo.ba").
func (h *Handler) completeDot(cctx *CompletionCtx, fileURI string) []CompletionItem {
	resolver := &typeResolver{idx: h.idx, imports: cctx.Imports, pkg: cctx.Package}

	// Resolve the receiver expression to a type symbol.
	typeSym := h.resolveReceiverType(cctx, resolver)
	if typeSym == "" {
		// Fallback: maybe it's a static access on a class name.
		typeSym = resolver.resolve(cctx.Receiver)
	}
	if typeSym == "" {
		return nil
	}

	// Get members of the resolved type.
	members := h.idx.MembersOfType(typeSym)
	prefix := strings.ToLower(cctx.Prefix)

	var items []CompletionItem
	for _, m := range members {
		if prefix != "" && !strings.HasPrefix(strings.ToLower(m.Name), prefix) {
			continue
		}
		item := CompletionItem{
			Label:      m.Name,
			Kind:       sdbKindToCompletionKind(m.Kind),
			InsertText: m.Name,
		}
		if m.Signature != nil {
			item.Detail = m.Signature.Label
		}
		items = append(items, item)
		if len(items) >= 100 {
			break
		}
	}
	return items
}

// resolveReceiverType resolves the type of a receiver expression (the part before the dot).
func (h *Handler) resolveReceiverType(cctx *CompletionCtx, resolver *typeResolver) string {
	recv := cctx.Receiver

	// Handle "this" → enclosing class.
	if recv == "this" {
		return resolver.resolve(cctx.EnclosingClass)
	}
	// Handle "super" → parent of enclosing class.
	if recv == "super" {
		classSym := resolver.resolve(cctx.EnclosingClass)
		if classSym != "" {
			parents := h.idx.ParentsOf(classSym)
			if len(parents) > 0 {
				return parents[0]
			}
		}
		return ""
	}

	// Handle chained dot access (e.g. "foo.bar"): resolve left-to-right.
	parts := strings.Split(recv, ".")
	if len(parts) == 0 {
		return ""
	}

	// Resolve the first part.
	typeSym := h.resolveIdentifierType(parts[0], cctx, resolver)

	// Resolve each subsequent part as a field/method access.
	for i := 1; i < len(parts) && typeSym != ""; i++ {
		memberName := parts[i]
		typeSym = h.resolveMemberType(typeSym, memberName)
	}

	return typeSym
}

// resolveIdentifierType resolves the type of a simple identifier by searching
// locals → params → fields, then falling back to class name (static access).
func (h *Handler) resolveIdentifierType(name string, cctx *CompletionCtx, resolver *typeResolver) string {
	// Search locals.
	for i := len(cctx.Locals) - 1; i >= 0; i-- {
		if cctx.Locals[i].Name == name {
			return resolver.resolve(cctx.Locals[i].Type)
		}
	}
	// Search params.
	for _, p := range cctx.Params {
		if p.Name == name {
			return resolver.resolve(p.Type)
		}
	}
	// Search class fields.
	for _, f := range cctx.ClassFields {
		if f.Name == name {
			return resolver.resolve(f.Type)
		}
	}
	// Maybe it's a class name (static access).
	return resolver.resolve(name)
}

// resolveMemberType resolves the type of a member (field or method) on a given type.
func (h *Handler) resolveMemberType(ownerTypeSym string, memberName string) string {
	// Look up members of the owner type and find the matching member.
	members := h.idx.MembersOfType(ownerTypeSym)
	for _, m := range members {
		if m.Name == memberName {
			return h.idx.TypeOfSymbol(m.Symbol)
		}
	}
	return ""
}

// completeLexical handles bare identifier completion (e.g. "pri").
func (h *Handler) completeLexical(cctx *CompletionCtx, fileURI string) []CompletionItem {
	prefix := strings.ToLower(cctx.Prefix)
	if prefix == "" {
		return nil
	}

	seen := make(map[string]struct{})
	var items []CompletionItem

	addItem := func(name string, kind int, detail string) {
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		items = append(items, CompletionItem{
			Label:      name,
			Kind:       kind,
			InsertText: name,
			Detail:     detail,
		})
	}

	matchPrefix := func(name string) bool {
		return strings.HasPrefix(strings.ToLower(name), prefix)
	}

	// 1. Local variables.
	for i := len(cctx.Locals) - 1; i >= 0; i-- {
		l := cctx.Locals[i]
		if matchPrefix(l.Name) {
			addItem(l.Name, SymbolKindVariable, l.Type)
		}
	}

	// 2. Method parameters.
	for _, p := range cctx.Params {
		if matchPrefix(p.Name) {
			addItem(p.Name, SymbolKindVariable, p.Type)
		}
	}

	// 3. Class fields.
	for _, f := range cctx.ClassFields {
		if matchPrefix(f.Name) {
			addItem(f.Name, SymbolKindField, f.Type)
		}
	}

	// 4. Class methods.
	for _, m := range cctx.ClassMethods {
		if matchPrefix(m) {
			addItem(m, SymbolKindMethod, "")
		}
	}

	if len(items) >= 100 {
		return items[:100]
	}

	// 5. Global type and symbol completion from the index.
	symbols := h.idx.CompletionSymbols(fileURI, cctx.Prefix)
	for _, s := range symbols {
		if len(items) >= 100 {
			break
		}
		detail := ""
		if s.Signature != nil {
			detail = s.Signature.Label
		}
		addItem(s.Name, sdbKindToCompletionKind(s.Kind), detail)
	}

	return items
}

func (h *Handler) handleSignatureHelp(ctx context.Context, params json.RawMessage) (any, error) {
	var p SignatureHelpParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
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
