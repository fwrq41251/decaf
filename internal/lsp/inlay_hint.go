package lsp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	slog "github.com/smacker/go-tree-sitter"
)

func (h *Handler) handleInlayHint(ctx context.Context, params json.RawMessage) (any, error) {
	var p InlayHintParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return []InlayHint{}, nil
	}

	content := h.getFileContent(p.TextDocument.URI)
	if content == "" {
		return []InlayHint{}, nil
	}

	src := []byte(content)
	tree, err := getTree(src)
	if err != nil {
		return []InlayHint{}, nil
	}

	var hints []InlayHint

	// Walk the AST to find var declarations and method calls within the requested range.
	h.collectInlayHints(tree.RootNode(), src, p, &hints)

	h.logger.Printf("inlayHint at %s range %d:%d-%d:%d -> %d hints",
		p.TextDocument.URI,
		p.Range.Start.Line, p.Range.Start.Character,
		p.Range.End.Line, p.Range.End.Character,
		len(hints))

	return hints, nil
}

func (h *Handler) collectInlayHints(node *slog.Node, content []byte, p InlayHintParams, hints *[]InlayHint) {
	if node == nil {
		return
	}

	// Skip nodes entirely outside the requested range.
	nodeStart := pointToPosition(node.StartPoint())
	nodeEnd := pointToPosition(node.EndPoint())
	if !rangeOverlaps(nodeStart, nodeEnd, p.Range) {
		return
	}

	switch node.Type() {
	case "local_variable_declaration":
		h.collectVarTypeHint(node, content, hints)
	case "method_invocation":
		h.collectParameterNameHints(node, content, p, hints)
	case "object_creation_expression":
		h.collectConstructorParameterNameHints(node, content, p, hints)
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		h.collectInlayHints(node.Child(i), content, p, hints)
	}
}

// collectVarTypeHint adds a type hint after the "var" keyword in local variable declarations.
func (h *Handler) collectVarTypeHint(node *slog.Node, content []byte, hints *[]InlayHint) {
	// Check if this is a "var" declaration.
	var varNode *slog.Node
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() == "type_identifier" && child.Content(content) == "var" {
			varNode = child
			break
		}
	}
	if varNode == nil {
		return
	}

	// Find the variable name from the variable_declarator.
	var nameNode *slog.Node
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() == "variable_declarator" {
			for j := 0; j < int(child.NamedChildCount()); j++ {
				gc := child.NamedChild(j)
				if gc.Type() == "identifier" {
					nameNode = gc
					break
				}
			}
			break
		}
	}
	if nameNode == nil {
		return
	}

	// Build a completion context at the var declaration to resolve the type.
	line := int(node.StartPoint().Row)
	cctx := parseCompletionCtx(h.logger, content, line, int(node.EndPoint().Column))
	resolver := &typeResolver{idx: h.idx, imports: cctx.Imports, pkg: cctx.Package}

	// Try to infer the type from the initializer.
	typeExpr, vi := inferTypeFromDeclarator(node, content, cctx)
	if typeExpr == nil && vi != nil {
		typeExpr = h.resolveVarInitializer(vi, cctx, resolver)
	}
	if typeExpr == nil {
		return
	}
	// Resolve non-primitive types through the resolver for full symbol names.
	if !isPrimitiveType(typeExpr.Sym) {
		resolved := resolver.resolveParameterized(typeExpr)
		if resolved != nil {
			typeExpr = resolved
		}
	}

	typeStr := formatTypeExprSimple(typeExpr)
	if typeStr == "" {
		return
	}

	// Place the hint after the variable name.
	end := nameNode.EndPoint()
	*hints = append(*hints, InlayHint{
		Position:     Position{Line: int(end.Row), Character: int(end.Column)},
		Label:        ": " + typeStr,
		Kind:         InlayHintKindType,
		PaddingLeft:  false,
		PaddingRight: true,
	})
}

// collectParameterNameHints adds parameter name hints before each argument in a method call.
func (h *Handler) collectParameterNameHints(node *slog.Node, content []byte, p InlayHintParams, hints *[]InlayHint) {
	argList := node.ChildByFieldName("arguments")
	if argList == nil || argList.NamedChildCount() == 0 {
		return
	}

	// Resolve the method symbol to get parameter names.
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	methodName := nameNode.Content(content)

	line := int(node.StartPoint().Row)
	col := int(nameNode.StartPoint().Column)
	cctx := parseCompletionCtx(h.logger, content, line, col)
	resolver := &typeResolver{idx: h.idx, imports: cctx.Imports, pkg: cctx.Package}

	var ownerType *index.TypeExpr
	objNode := node.ChildByFieldName("object")
	if objNode != nil {
		recvText := exprToReceiver(objNode, content)
		if recvText != "" {
			cctx.Receiver = recvText
			ownerType, _ = h.resolveReceiverTypeExpr(cctx, resolver)
		}
	} else {
		// Unqualified call — resolve from enclosing class.
		if cctx.EnclosingClass != "" {
			if sym := resolver.resolve(cctx.EnclosingClass); sym != "" {
				ownerType = &index.TypeExpr{Sym: sym}
			}
		}
	}
	if ownerType == nil {
		return
	}

	// Find matching method with the right parameter count.
	argCount := int(argList.NamedChildCount())
	sym := h.findMethodByArgCount(ownerType.Sym, methodName, argCount)
	if sym == nil || sym.Signature == nil {
		return
	}

	paramNames := extractParamNames(sym.Signature)
	if len(paramNames) == 0 {
		return
	}

	h.addParameterHints(argList, content, paramNames, hints)
}

// collectConstructorParameterNameHints adds parameter name hints for new Foo(...) expressions.
func (h *Handler) collectConstructorParameterNameHints(node *slog.Node, content []byte, p InlayHintParams, hints *[]InlayHint) {
	// Find the argument_list child.
	var argList *slog.Node
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child.Type() == "argument_list" {
			argList = child
			break
		}
	}
	if argList == nil || argList.NamedChildCount() == 0 {
		return
	}

	te := extractTypeFromNewExpr(node, content)
	if te == nil {
		return
	}

	line := int(node.StartPoint().Row)
	col := int(node.StartPoint().Column)
	cctx := parseCompletionCtx(h.logger, content, line, col)
	resolver := &typeResolver{idx: h.idx, imports: cctx.Imports, pkg: cctx.Package}

	typeSym := resolver.resolve(te.Sym)
	if typeSym == "" {
		return
	}

	// Find a constructor with matching argument count.
	argCount := int(argList.NamedChildCount())
	sym := h.findMethodByArgCount(typeSym, te.Sym, argCount)
	if sym == nil || sym.Signature == nil {
		return
	}

	paramNames := extractParamNames(sym.Signature)
	if len(paramNames) == 0 {
		return
	}

	h.addParameterHints(argList, content, paramNames, hints)
}

// addParameterHints adds parameter name hints before each argument in an argument list.
func (h *Handler) addParameterHints(argList *slog.Node, content []byte, paramNames []string, hints *[]InlayHint) {
	argIdx := 0
	for i := 0; i < int(argList.NamedChildCount()) && argIdx < len(paramNames); i++ {
		arg := argList.NamedChild(i)

		// Skip adding hints for arguments that are already obvious:
		// - Simple identifiers matching the parameter name
		// - String/char/boolean/null literals
		argText := arg.Content(content)
		if argText == paramNames[argIdx] {
			argIdx++
			continue
		}

		start := arg.StartPoint()
		*hints = append(*hints, InlayHint{
			Position:     Position{Line: int(start.Row), Character: int(start.Column)},
			Label:        paramNames[argIdx] + ":",
			Kind:         InlayHintKindParameter,
			PaddingLeft:  false,
			PaddingRight: true,
		})
		argIdx++
	}
}

// findMethodByArgCount finds a method/constructor by name and argument count.
func (h *Handler) findMethodByArgCount(typeSym, methodName string, argCount int) *index.Symbol {
	members := h.idx.MembersOfType(typeSym)
	var bestMatch *index.Symbol
	for i := range members {
		m := &members[i]
		if m.Name != methodName {
			continue
		}
		if m.Signature == nil {
			continue
		}
		params := m.Signature.ParseParams()
		if len(params) == argCount {
			return m
		}
		// Fall back to a compatible varargs signature when no exact arity exists.
		if bestMatch == nil && supportsVarargs(params, argCount) {
			bestMatch = m
		}
	}
	return bestMatch
}

func pointToPosition(p slog.Point) Position {
	return Position{
		Line:      int(p.Row),
		Character: int(p.Column),
	}
}

func positionLess(a, b Position) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Character < b.Character
}

func rangeOverlaps(start, end Position, r Range) bool {
	if positionLess(end, r.Start) {
		return false
	}
	if positionLess(r.End, start) {
		return false
	}
	return true
}

func supportsVarargs(params []string, argCount int) bool {
	if len(params) == 0 {
		return false
	}
	last := strings.TrimSpace(params[len(params)-1])
	if !strings.Contains(last, "...") {
		return false
	}
	return argCount >= len(params)-1
}

// extractParamNames extracts just the parameter names from a signature.
// e.g. "void add(String name, int x)" → ["name", "x"]
func extractParamNames(sig *index.SignatureInfo) []string {
	if sig == nil {
		return nil
	}
	if len(sig.Params) > 0 {
		names := make([]string, 0, len(sig.Params))
		for _, p := range sig.Params {
			if p.Name != "" {
				names = append(names, p.Name)
			}
		}
		if len(names) > 0 {
			return names
		}
	}
	params := sig.ParseParams()
	if len(params) == 0 {
		return nil
	}
	names := make([]string, 0, len(params))
	for _, p := range params {
		// Each param is like "String name" or "int x" or "Map<String, Integer> map".
		// Extract the last word as the parameter name.
		name := extractLastWord(p)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// extractLastWord extracts the last word from a parameter declaration.
// e.g. "String name" → "name", "int[] args" → "args", "Map<String, Integer> map" → "map"
func extractLastWord(param string) string {
	param = strings.TrimSpace(param)
	// Handle varargs: "String... args"
	param = strings.ReplaceAll(param, "...", " ")
	// Handle array syntax in the name: "int[] x" or "String []x"
	param = strings.ReplaceAll(param, "[]", " ")
	param = strings.TrimSpace(param)

	if idx := strings.LastIndexByte(param, ' '); idx >= 0 {
		return strings.TrimSpace(param[idx+1:])
	}
	return ""
}

// isPrimitiveType returns true if the type name is a Java primitive or literal type.
func isPrimitiveType(name string) bool {
	switch name {
	case "int", "long", "short", "byte", "float", "double", "boolean", "char":
		return true
	}
	return false
}

// formatTypeExprSimple formats a TypeExpr into a simple readable type name.
// e.g. TypeExpr{Sym: "java/util/List#", Args: [{Sym: "java/lang/String#"}]} → "List<String>"
func formatTypeExprSimple(te *index.TypeExpr) string {
	if te == nil {
		return ""
	}
	name := index.SimpleTypeName(te.Sym)
	if len(te.Args) == 0 {
		return name
	}
	var sb strings.Builder
	sb.WriteString(name)
	sb.WriteByte('<')
	for i, arg := range te.Args {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(formatTypeExprSimple(arg))
	}
	sb.WriteByte('>')
	return sb.String()
}


