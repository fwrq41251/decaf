package lsp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	slog "github.com/smacker/go-tree-sitter"
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

	// Resolve the receiver expression to a type with generic arguments.
	typeExpr := h.resolveReceiverTypeExpr(cctx, resolver)
	if typeExpr == nil {
		// Fallback: maybe it's a static access on a class name.
		if sym := resolver.resolve(cctx.Receiver); sym != "" {
			typeExpr = &index.TypeExpr{Sym: sym}
		}
	}
	if typeExpr == nil {
		return nil
	}

	// Get members of the resolved type.
	members := h.idx.MembersOfType(typeExpr.Sym)
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

// resolveReceiverTypeExpr resolves the type of a receiver expression, preserving generic arguments.
func (h *Handler) resolveReceiverTypeExpr(cctx *CompletionCtx, resolver *typeResolver) *index.TypeExpr {
	recv := cctx.Receiver

	// Handle "this" → enclosing class.
	if recv == "this" {
		if sym := resolver.resolve(cctx.EnclosingClass); sym != "" {
			return &index.TypeExpr{Sym: sym}
		}
		return nil
	}
	// Handle "super" → parent of enclosing class.
	if recv == "super" {
		classSym := resolver.resolve(cctx.EnclosingClass)
		if classSym != "" {
			if pts := h.idx.ParentTypesOf(classSym); len(pts) > 0 {
				return pts[0]
			}
			// Fallback to old ParentsOf.
			if parents := h.idx.ParentsOf(classSym); len(parents) > 0 {
				return &index.TypeExpr{Sym: parents[0]}
			}
		}
		return nil
	}

	// Handle chained dot access (e.g. "foo.bar"): resolve left-to-right.
	parts := strings.Split(recv, ".")
	if len(parts) == 0 {
		return nil
	}

	// Resolve the first part.
	typeExpr := h.resolveIdentifierTypeExpr(parts[0], cctx, resolver)

	// Resolve each subsequent part as a field/method access.
	for i := 1; i < len(parts) && typeExpr != nil; i++ {
		typeExpr = h.resolveMemberTypeExpr(typeExpr, parts[i])
	}

	return typeExpr
}

// resolveIdentifierTypeExpr resolves the type of a simple identifier, preserving generic arguments.
// Searches locals → params → fields (Tree-sitter source, may have "List<String>"),
// then falls back to SemanticDB symbolDeclType for class fields.
func (h *Handler) resolveIdentifierTypeExpr(name string, cctx *CompletionCtx, resolver *typeResolver) *index.TypeExpr {
	// Search locals.
	for i := len(cctx.Locals) - 1; i >= 0; i-- {
		if cctx.Locals[i].Name == name {
			return resolver.resolveParameterized(cctx.Locals[i].Type)
		}
	}
	// Search params.
	for _, p := range cctx.Params {
		if p.Name == name {
			return resolver.resolveParameterized(p.Type)
		}
	}
	// Search class fields.
	for _, f := range cctx.ClassFields {
		if f.Name == name {
			return resolver.resolveParameterized(f.Type)
		}
	}
	// Maybe it's a class name (static access).
	if sym := resolver.resolve(name); sym != "" {
		return &index.TypeExpr{Sym: sym}
	}
	return nil
}

// resolveMemberTypeExpr resolves the type of a member on a given type,
// performing generic type parameter substitution.
func (h *Handler) resolveMemberTypeExpr(owner *index.TypeExpr, memberName string) *index.TypeExpr {
	members := h.idx.MembersOfType(owner.Sym)
	for _, m := range members {
		if m.Name != memberName {
			continue
		}
		// Try structured type first (preserves generics).
		if retType := h.idx.DeclTypeOf(m.Symbol); retType != nil {
			return substituteTypeParams(retType, owner, h.idx)
		}
		// Fallback to flat symbolType.
		if sym := h.idx.TypeOfSymbol(m.Symbol); sym != "" {
			result := &index.TypeExpr{Sym: sym}
			return substituteTypeParams(result, owner, h.idx)
		}
		return nil
	}
	return nil
}

// substituteTypeParams replaces type parameter references in retType with
// actual type arguments from owner, traversing the inheritance chain to
// resolve type parameters declared on parent types.
//
// Example: owner = {Sym:"ArrayList#", Args:[{Sym:"String#"}]}
//
//	ArrayList# extends AbstractList<E>, AbstractList<E> extends List<E>
//	retType = {Sym: "List#[E]"}  (return type of List.get())
//	→ builds chain: ArrayList#[E]→String#, AbstractList#[E]→String#, List#[E]→String#
//	→ returns {Sym: "String#"}
func substituteTypeParams(retType *index.TypeExpr, owner *index.TypeExpr, idx *index.Index) *index.TypeExpr {
	if retType == nil || owner == nil || len(owner.Args) == 0 {
		return retType
	}

	subst := buildSubstitutionMap(owner, idx)
	if len(subst) == 0 {
		return retType
	}

	return applySubstitution(retType, subst)
}

// buildSubstitutionMap builds a map from type parameter symbols to concrete
// TypeExprs by starting with the owner's type params and walking the
// inheritance chain via ParentTypesOf.
func buildSubstitutionMap(owner *index.TypeExpr, idx *index.Index) map[string]*index.TypeExpr {
	subst := make(map[string]*index.TypeExpr)

	// Seed with the owner's own type params → args.
	typeParams := idx.ClassTypeParams(owner.Sym)
	for i, tp := range typeParams {
		if i < len(owner.Args) {
			subst[tp] = owner.Args[i]
		}
	}

	// BFS through parent types to propagate substitutions up the hierarchy.
	visited := make(map[string]bool)
	queue := []string{owner.Sym}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if visited[current] {
			continue
		}
		visited[current] = true

		for _, parentType := range idx.ParentTypesOf(current) {
			parentParams := idx.ClassTypeParams(parentType.Sym)
			for i, pp := range parentParams {
				if i < len(parentType.Args) {
					subst[pp] = applySubstitution(parentType.Args[i], subst)
				}
			}
			queue = append(queue, parentType.Sym)
		}
	}

	return subst
}

// applySubstitution replaces type parameter references in te using the
// substitution map, recursing into generic type arguments.
func applySubstitution(te *index.TypeExpr, subst map[string]*index.TypeExpr) *index.TypeExpr {
	if te == nil {
		return nil
	}

	if resolved, ok := subst[te.Sym]; ok {
		return resolved
	}

	if len(te.Args) > 0 {
		result := &index.TypeExpr{Sym: te.Sym, Args: make([]*index.TypeExpr, len(te.Args))}
		for i, arg := range te.Args {
			result.Args[i] = applySubstitution(arg, subst)
		}
		return result
	}

	return te
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

// countActiveParameter uses the Tree-sitter AST to find the enclosing
// argument_list and counts which argument the cursor is in.
func (h *Handler) countActiveParameter(fileURI string, line, character int) int {
	content := h.getFileContent(fileURI)
	if content == "" {
		return 0
	}

	src := []byte(content)
	parser := javaParserPool.Get().(*slog.Parser)
	defer javaParserPool.Put(parser)

	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return 0
	}

	node := nodeAtPosition(tree.RootNode(), line, character)
	if node == nil {
		return 0
	}

	// Find the enclosing argument_list node.
	argList := node
	for argList != nil && argList.Type() != "argument_list" {
		argList = argList.Parent()
	}
	if argList == nil {
		return 0
	}

	// Count named children (arguments) that end before or contain the cursor.
	cursorByte := byteOffsetForPosition(src, line, character)
	active := 0
	for i := 0; i < int(argList.NamedChildCount()); i++ {
		child := argList.NamedChild(i)
		if int(child.StartByte()) >= cursorByte {
			break
		}
		active = i
	}
	return active
}
