package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	slog "github.com/smacker/go-tree-sitter"
)

var javaKeywords = []string{
	"abstract", "assert", "boolean", "break", "byte", "case", "catch", "char", "class", "const",
	"continue", "default", "do", "double", "else", "enum", "extends", "final", "finally", "float",
	"for", "goto", "if", "implements", "import", "instanceof", "int", "interface", "long", "native",
	"new", "package", "private", "protected", "public", "return", "short", "static", "strictfp",
	"super", "switch", "synchronized", "this", "throw", "throws", "transient", "try", "void",
	"volatile", "while",
}

var javaLiterals = []string{
	"true", "false", "null",
}

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
	cctx := parseCompletionCtx(h.logger, []byte(content), p.Position.Line, p.Position.Character)

	var items []CompletionItem

	contentBytes := []byte(content)
	if cctx.Kind == CompletionDot {
		items = h.completeDot(cctx, p.TextDocument.URI)
	} else {
		items = h.completeLexical(cctx, p.TextDocument.URI, contentBytes)
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
	// Also track whether this is a static access (e.g. "Objects.") vs instance access (e.g. "obj.").
	typeExpr, staticAccess := h.resolveReceiverTypeExpr(cctx, resolver)
	if typeExpr == nil {
		return nil
	}

	// Get members of the resolved type.
	members := h.idx.MembersOfType(typeExpr.Sym)
	query := cctx.Prefix

	var items []CompletionItem
	seen := make(map[string]int) // name -> index in items (for dedup of overloads)
	for _, m := range members {
		if !index.FuzzyMatch(m.Name, query) {
			continue
		}
		// Filter by static/instance context.
		if staticAccess && !m.IsStatic {
			continue
		}
		if !staticAccess && m.IsStatic {
			continue
		}
		// Build sortText: exact case match first, then fields before methods.
		sortPrefix := "1" // case-insensitive match
		if cctx.Prefix != "" && strings.HasPrefix(m.Name, cctx.Prefix) {
			sortPrefix = "0" // exact case match
		}
		kind := sdbKindToCompletionKind(m.Kind)
		kindOrder := "1" // methods
		if kind == CompletionKindField || kind == CompletionKindProperty {
			kindOrder = "0" // fields first
		}
		sortText := sortPrefix + kindOrder + m.Name

		// Merge overloaded methods: keep one item per name, update detail for overloads.
		if idx, ok := seen[m.Name]; ok {
			if items[idx].Detail != "" && !strings.Contains(items[idx].Detail, " (+") {
				items[idx].Detail += " (+1 overload)"
			} else if strings.Contains(items[idx].Detail, " (+") {
				items[idx].Detail = overloadDetail(items[idx].Detail)
			}
			continue
		}

		item := methodCompletionItem(m.Name, kind, m.Signature, sortText, m.Doc)
		seen[m.Name] = len(items)
		items = append(items, item)
		if len(items) >= 100 {
			break
		}
	}
	return items
}

// resolveReceiverTypeExpr resolves the type of a receiver expression, preserving generic arguments.
// Returns the resolved type and whether this is a static access (class name before the dot).
func (h *Handler) resolveReceiverTypeExpr(cctx *CompletionCtx, resolver *typeResolver) (*index.TypeExpr, bool) {
	recv := cctx.Receiver

	// Handle "this" → enclosing class.
	if recv == "this" {
		if sym := resolver.resolve(cctx.EnclosingClass); sym != "" {
			return &index.TypeExpr{Sym: sym}, false
		}
		return nil, false
	}
	// Handle "super" → parent of enclosing class.
	if recv == "super" {
		classSym := resolver.resolve(cctx.EnclosingClass)
		if classSym != "" {
			if pts := h.idx.ParentTypesOf(classSym); len(pts) > 0 {
				return pts[0], false
			}
			// Fallback to old ParentsOf.
			if parents := h.idx.ParentsOf(classSym); len(parents) > 0 {
				return &index.TypeExpr{Sym: parents[0]}, false
			}
		}
		return nil, false
	}

	// Handle chained dot access (e.g. "foo.bar"): resolve left-to-right.
	parts := strings.Split(recv, ".")
	if len(parts) == 0 {
		return nil, false
	}

	// Resolve the first part.
	typeExpr, staticAccess := h.resolveIdentifierTypeExpr(parts[0], cctx, resolver)

	// Resolve each subsequent part as a field/method access.
	// Once we resolve through a member, it's no longer a static access.
	for i := 1; i < len(parts) && typeExpr != nil; i++ {
		typeExpr = h.resolveMemberTypeExpr(typeExpr, parts[i])
		staticAccess = false
	}

	return typeExpr, staticAccess
}

// resolveIdentifierTypeExpr resolves the type of a simple identifier, preserving generic arguments.
// Searches locals → params → fields (Tree-sitter source, may have "List<String>"),
// then falls back to class name resolution (static access).
// Returns the resolved type and whether this is a static access (class name, not a variable).
func (h *Handler) resolveIdentifierTypeExpr(name string, cctx *CompletionCtx, resolver *typeResolver) (*index.TypeExpr, bool) {
	// Search locals (innermost first).
	for i := 0; i < len(cctx.Locals); i++ {
		if cctx.Locals[i].Name == name {
			if te := resolver.resolveParameterized(cctx.Locals[i].Type); te != nil {
				return te, false
			}
			// Deferred var inference: resolve method return type via index.
			if cctx.Locals[i].Initializer != nil {
				if te := h.resolveVarInitializer(cctx.Locals[i].Initializer, cctx, resolver); te != nil {
					return te, false
				}
			}
		}
	}
	// Search params.
	for _, p := range cctx.Params {
		if p.Name == name {
			return resolver.resolveParameterized(p.Type), false
		}
	}
	// Search class fields.
	for _, f := range cctx.ClassFields {
		if f.Name == name {
			return resolver.resolveParameterized(f.Type), false
		}
	}
	// Maybe it's a class name (static access).
	if sym := resolver.resolve(name); sym != "" {
		return &index.TypeExpr{Sym: sym}, true
	}
	return nil, false
}

// resolveVarInitializer resolves the return type of a method call
// used as an initializer for a "var" declaration.
// Supports static calls (List.of(...)), instance calls (list.get(0)),
// and chained calls (builder.name("a").build()).
func (h *Handler) resolveVarInitializer(vi *VarInitializer, cctx *CompletionCtx, resolver *typeResolver) *index.TypeExpr {
	// Resolve the receiver chain (e.g. "builder.name" or "List").
	parts := strings.Split(vi.Receiver, ".")
	if len(parts) == 0 {
		return nil
	}

	typeExpr, _ := h.resolveIdentifierTypeExpr(parts[0], cctx, resolver)
	if typeExpr == nil {
		return nil
	}

	for i := 1; i < len(parts); i++ {
		typeExpr = h.resolveMemberTypeExpr(typeExpr, parts[i])
		if typeExpr == nil {
			return nil
		}
	}

	return h.resolveMemberTypeExpr(typeExpr, vi.MethodName)
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
func (h *Handler) completeLexical(cctx *CompletionCtx, fileURI string, content []byte) []CompletionItem {
	prefix := strings.ToLower(cctx.Prefix)
	if prefix == "" {
		return nil
	}

	// Resolve expected parameter type if cursor is inside a method call.
	expectedType := h.resolveExpectedType(cctx)

	seen := make(map[string]struct{})
	var items []CompletionItem

	// casePrefix returns "0" if name starts with the original-case prefix, "1" otherwise.
	casePrefix := func(name string) string {
		if cctx.Prefix != "" && strings.HasPrefix(name, cctx.Prefix) {
			return "0"
		}
		return "1"
	}

	addItem := func(name string, kind int, detail string, scopeOrder string) {
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		// Type-aware boost: prepend "0" for type-matching candidates, "1" otherwise.
		typePrefix := "1"
		if expectedType != "" && typeMatchesExpected(detail, expectedType) {
			typePrefix = "0"
		}
		item := CompletionItem{
			Label:      name,
			Kind:       kind,
			InsertText: name,
			Detail:     detail,
			SortText:   typePrefix + casePrefix(name) + scopeOrder + name,
			FilterText: name,
		}
		items = append(items, item)
	}

	matchPrefix := func(name string) bool {
		return strings.HasPrefix(strings.ToLower(name), prefix)
	}

	// 1. Local variables (scope "0").
	for i := len(cctx.Locals) - 1; i >= 0; i-- {
		l := cctx.Locals[i]
		if matchPrefix(l.Name) {
			addItem(l.Name, SymbolKindVariable, l.Type.String(), "0")
		}
	}

	// 2. Method parameters (scope "1").
	for _, p := range cctx.Params {
		if matchPrefix(p.Name) {
			addItem(p.Name, SymbolKindVariable, p.Type.String(), "1")
		}
	}

	// 3. Class fields (scope "2").
	for _, f := range cctx.ClassFields {
		if matchPrefix(f.Name) {
			addItem(f.Name, SymbolKindField, f.Type.String(), "2")
		}
	}

	// 4. Class methods (scope "3").
	for _, m := range cctx.ClassMethods {
		if matchPrefix(m) {
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}
			items = append(items, CompletionItem{
				Label:            m,
				Kind:             SymbolKindMethod,
				InsertText:       m + "($1)$0",
				InsertTextFormat: InsertTextFormatSnippet,
				SortText:         "1" + casePrefix(m) + "3" + m,
				FilterText:       m,
			})
		}
	}

	// 5. Keywords and Literals (scope "8").
	// Only suggest keywords in lexical completion.
	for _, kw := range javaKeywords {
		if matchPrefix(kw) {
			addItem(kw, CompletionKindText, "keyword", "8")
		}
	}
	for _, lit := range javaLiterals {
		if matchPrefix(lit) {
			addItem(lit, CompletionKindText, "literal", "8")
		}
	}

	if len(items) >= 100 {
		return items[:100]
	}

	// 6. Global type and symbol completion from the index.
	symbols := h.idx.CompletionSymbols(fileURI, cctx.Prefix)
	for _, s := range symbols {
		if len(items) >= 100 {
			break
		}
		if _, ok := seen[s.Name]; ok {
			continue
		}
		seen[s.Name] = struct{}{}

		scopeOrder := "7" // default: other symbol
		if s.SameFile {
			if isTypeCompletionKind(sdbKindToCompletionKind(s.Kind)) {
				scopeOrder = "4" // same-file type
			} else {
				scopeOrder = "6" // same-file other
			}
		} else {
			if isTypeCompletionKind(sdbKindToCompletionKind(s.Kind)) {
				scopeOrder = "5" // other type
			}
		}

		kind := sdbKindToCompletionKind(s.Kind)
		sortText := "1" + casePrefix(s.Name) + scopeOrder + s.Name
		item := methodCompletionItem(s.Name, kind, s.Signature, sortText, s.Doc)

		// Auto-import for type symbols from other packages.
		if s.Kind == sdb.SymbolInformation_CLASS || s.Kind == sdb.SymbolInformation_INTERFACE {
			if fqn := fqnFromSymbol(s.Symbol); fqn != "" {
				if edit := computeImportEdit(content, cctx.Imports, cctx.Package, fqn); edit != nil {
					item.AdditionalTextEdits = []TextEdit{*edit}
					item.Detail = fqn
				}
			}
		}

		items = append(items, item)
	}

	return items
}

// resolveExpectedType resolves the expected parameter type at the cursor position.
// Returns a simple type name (e.g. "String", "int") or "" if unknown.
func (h *Handler) resolveExpectedType(cctx *CompletionCtx) string {
	if cctx.Call == nil {
		return ""
	}

	resolver := &typeResolver{idx: h.idx, imports: cctx.Imports, pkg: cctx.Package}

	var syms []index.Symbol
	if cctx.Call.IsNewExpr {
		// new Foo(...) → look up constructors.
		if sym := resolver.resolve(cctx.Call.Constructor); sym != "" {
			syms = h.findMembersByName(sym, cctx.Call.Constructor)
		}
	} else if cctx.Call.Receiver != "" {
		// obj.method(...) → resolve receiver type, find method.
		fakeCctx := &CompletionCtx{
			Receiver:       cctx.Call.Receiver,
			Locals:         cctx.Locals,
			Params:         cctx.Params,
			ClassFields:    cctx.ClassFields,
			Imports:        cctx.Imports,
			Package:        cctx.Package,
			EnclosingClass: cctx.EnclosingClass,
		}
		typeExpr, _ := h.resolveReceiverTypeExpr(fakeCctx, resolver)
		if typeExpr != nil {
			syms = h.findMembersByName(typeExpr.Sym, cctx.Call.MethodName)
		}
	} else {
		// Unqualified call: search enclosing class.
		if cctx.EnclosingClass != "" {
			classSym := resolver.resolve(cctx.EnclosingClass)
			if classSym != "" {
				syms = h.findMembersByName(classSym, cctx.Call.MethodName)
			}
		}
	}

	if len(syms) == 0 {
		return ""
	}

	// Find the best matching overload and extract param type at the given index.
	paramIdx := cctx.Call.ParamIndex
	for _, sym := range syms {
		if sym.Signature == nil {
			continue
		}
		if paramIdx < len(sym.Signature.Params) {
			param := sym.Signature.Params[paramIdx]
			if param.Type != "" {
				return param.Type
			}
		}
		params := sym.Signature.ParseParams()
		if paramIdx < len(params) {
			return extractParamTypeName(params[paramIdx])
		}
	}
	return ""
}

// extractParamTypeName extracts the type name from a parameter label like "String arg" or "int x".
func extractParamTypeName(paramLabel string) string {
	// Parameter labels are formatted as "Type name", e.g. "String[] args", "int x".
	// The type is everything before the last space.
	lastSpace := strings.LastIndex(paramLabel, " ")
	if lastSpace <= 0 {
		return ""
	}
	return strings.TrimSpace(paramLabel[:lastSpace])
}

// typeMatchesExpected checks if a candidate's type (detail string) matches the expected type.
func typeMatchesExpected(candidateType, expectedType string) bool {
	if candidateType == "" || expectedType == "" {
		return false
	}
	// Strip generic arguments for comparison (e.g. "List<String>" → "List").
	candidateBase := stripGenerics(candidateType)
	expectedBase := stripGenerics(expectedType)

	// Exact match.
	if candidateBase == expectedBase {
		return true
	}

	// Match simplified names: "java/lang/String#" matches "String".
	candidateSimple := simplifyTypeName(candidateBase)
	expectedSimple := simplifyTypeName(expectedBase)
	return candidateSimple == expectedSimple
}

// stripGenerics removes generic type arguments: "List<String>" → "List".
func stripGenerics(name string) string {
	if idx := strings.IndexByte(name, '<'); idx >= 0 {
		return name[:idx]
	}
	return name
}

// simplifyTypeName extracts the simple class name from a potentially qualified name.
// e.g. "java/lang/String#" → "String", "java.util.List" → "List"
func simplifyTypeName(name string) string {
	name = strings.TrimSuffix(name, "#")
	name = strings.TrimSuffix(name, ".")
	if idx := strings.LastIndexAny(name, "/."); idx >= 0 {
		return name[idx+1:]
	}
	return name
}

// methodCompletionItem builds a CompletionItem for a member (method or field).
// Methods get snippet format with parentheses; fields get plain text.
func methodCompletionItem(name string, kind int, sig *index.SignatureInfo, sortText, doc string) CompletionItem {
	item := CompletionItem{
		Label:      name,
		Kind:       kind,
		InsertText: name,
		SortText:   sortText,
		FilterText: name,
	}
	if sig != nil {
		item.Detail = sig.Label
	}
	if doc != "" {
		item.Documentation = &MarkupContent{Kind: "markdown", Value: doc}
	}
	if kind == CompletionKindMethod || kind == CompletionKindConstructor {
		if sig != nil && sig.HasParams {
			item.InsertText = buildMethodInsertText(name, sig)
		} else {
			item.InsertText = name + "()$0"
		}
		item.InsertTextFormat = InsertTextFormatSnippet
	}
	return item
}

func buildMethodInsertText(name string, sig *index.SignatureInfo) string {
	if sig == nil || !sig.HasParams {
		return name + "()$0"
	}
	if len(sig.Params) == 0 {
		return name + "($1)$0"
	}

	var placeholders []string
	for i, p := range sig.Params {
		label := p.Name
		if label == "" {
			label = p.Type
		}
		if label == "" {
			label = "arg"
		}
		placeholders = append(placeholders, fmt.Sprintf("${%d:%s}", i+1, label))
	}
	return name + "(" + strings.Join(placeholders, ", ") + ")$0"
}

// overloadDetail increments the overload count in a detail string like "void foo(int) (+1 overload)".
func overloadDetail(detail string) string {
	// Parse existing count from " (+N overload)" or " (+N overloads)" suffix.
	idx := strings.Index(detail, " (+")
	if idx < 0 {
		return detail
	}
	base := detail[:idx]
	suffix := detail[idx+3:] // after " (+"
	end := strings.Index(suffix, " ")
	if end < 0 {
		return detail
	}
	n := 0
	for _, c := range suffix[:end] {
		if c < '0' || c > '9' {
			return detail
		}
		n = n*10 + int(c-'0')
	}
	n++
	noun := "overloads"
	if n == 1 {
		noun = "overload"
	}
	return fmt.Sprintf("%s (+%d %s)", base, n, noun)
}

func isTypeCompletionKind(kind int) bool {
	return kind == CompletionKindClass || kind == CompletionKindInterface || kind == CompletionKindEnum
}

func (h *Handler) handleSignatureHelp(ctx context.Context, params json.RawMessage) (any, error) {
	var p SignatureHelpParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return nil, nil
	}

	syms := h.idx.SymbolSignatures(p.TextDocument.URI, p.Position.Line, p.Position.Character)

	// Fallback: when SemanticDB has no occurrence (uncompiled code),
	// use Tree-sitter to find the enclosing method call and resolve signatures.
	if len(syms) == 0 {
		syms = h.resolveSignatureFromAST(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	}
	if len(syms) == 0 {
		return nil, nil
	}

	var sigs []SignatureInformation
	for i := range syms {
		si := formatSignatureHelp(&syms[i])
		if si != nil {
			sigs = append(sigs, *si)
		}
	}
	if len(sigs) == 0 {
		return nil, nil
	}

	activeParam := h.countActiveParameter(p.TextDocument.URI, p.Position.Line, p.Position.Character)

	// Pick the best overload: first signature whose param count >= activeParam+1.
	activeSig := 0
	for i, sig := range sigs {
		if len(sig.Parameters) >= activeParam+1 {
			activeSig = i
			break
		}
	}

	return SignatureHelp{
		Signatures:      sigs,
		ActiveSignature: activeSig,
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

	contentBytes := []byte(content)
	byteOff := PositionToByteOffset(contentBytes, line, character)

	// Walk backwards from cursor to find the start of the identifier.
	start := byteOff
	for start > 0 {
		ch := contentBytes[start-1]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '$' {
			start--
		} else {
			break
		}
	}

	return string(contentBytes[start:byteOff])
}

// resolveSignatureFromAST uses Tree-sitter to find the enclosing method call
// and resolves its signatures via the index, enabling signature help for
// uncompiled code that lacks SemanticDB occurrences.
func (h *Handler) resolveSignatureFromAST(fileURI string, line, character int) []index.Symbol {
	content := h.getFileContent(fileURI)
	if content == "" {
		return nil
	}

	src := []byte(content)
	tree, err := getTree(src)
	if err != nil {
		return nil
	}

	node := nodeAtPosition(tree.RootNode(), line, character)
	if node == nil {
		return nil
	}

	// Walk up to find the enclosing method_invocation or object_creation_expression.
	callNode := node
	for callNode != nil {
		switch callNode.Type() {
		case "method_invocation", "object_creation_expression":
			goto found
		}
		callNode = callNode.Parent()
	}
	return nil

found:
	// Parse completion context at the call site for type resolution.
	cctx := parseCompletionCtx(h.logger, src, line, character)
	resolver := &typeResolver{idx: h.idx, imports: cctx.Imports, pkg: cctx.Package}

	if callNode.Type() == "object_creation_expression" {
		// new Foo(...) → look up constructors of Foo.
		te := extractTypeFromNewExpr(callNode, src)
		if te == nil {
			return nil
		}
		if sym := resolver.resolve(te.Sym); sym != "" {
			return h.findMembersByName(sym, te.Sym)
		}
		return nil
	}

	// method_invocation: extract receiver (object) and method name.
	nameNode := callNode.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	methodName := nameNode.Content(src)

	objNode := callNode.ChildByFieldName("object")
	if objNode == nil {
		// Simple call without receiver (e.g. "doWork(...)") → search enclosing class.
		if cctx.EnclosingClass == "" {
			return nil
		}
		classSym := resolver.resolve(cctx.EnclosingClass)
		if classSym == "" {
			return nil
		}
		return h.findMembersByName(classSym, methodName)
	}

	// Has a receiver: build a fake CompletionCtx to reuse receiver resolution.
	recvText := exprToReceiver(objNode, src)
	if recvText == "" {
		return nil
	}
	cctx.Receiver = recvText
	typeExpr, _ := h.resolveReceiverTypeExpr(cctx, resolver)
	if typeExpr == nil {
		return nil
	}
	return h.findMembersByName(typeExpr.Sym, methodName)
}

// findMembersByName returns all members of a type matching the given name.
func (h *Handler) findMembersByName(typeSym, name string) []index.Symbol {
	members := h.idx.MembersOfType(typeSym)
	var result []index.Symbol
	for _, m := range members {
		if m.Name == name {
			result = append(result, m)
		}
	}
	return result
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

	// Count commas before the cursor to determine the active parameter index.
	cursorByte := PositionToByteOffset(src, line, character)
	active := 0
	for i := 0; i < int(argList.ChildCount()); i++ {
		child := argList.Child(i)
		if int(child.StartByte()) >= cursorByte {
			break
		}
		if child.Type() == "," {
			active++
		}
	}
	return active
}
