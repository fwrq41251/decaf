package lsp

import (
	"context"
	"log"
	"runtime/debug"
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	slog "github.com/smacker/go-tree-sitter"
)

// CompletionKind distinguishes between lexical and dot-triggered completion.
type CompletionKind int

const (
	CompletionLexical CompletionKind = iota
	CompletionDot
)

type CompletionScope int

const (
	ScopeUnknown CompletionScope = iota
	ScopeClass                   // Inside a class/interface/enum body
	ScopeBlock                   // Inside a method/constructor/initializer block
)

// VarInitializer holds info about a var declaration's initializer expression
// for deferred type inference (requires index access).
type VarInitializer struct {
	Receiver   string // receiver expression, e.g. "List" for List.of(), "builder.name" for builder.name().build()
	MethodName string // method name, e.g. "of", "build"
}

// ValueDecl represents a variable/field/parameter declaration with its type.
type ValueDecl struct {
	Name        string
	Type        *index.TypeExpr
	Initializer *VarInitializer // non-nil when type is "var" with a method call initializer
}

// CallContext holds information about an enclosing method call at the cursor,
// used for type-aware completion ranking.
type CallContext struct {
	Receiver    string // receiver expression text (empty for unqualified calls)
	MethodName  string // method name being called
	ParamIndex  int    // 0-based index of the parameter the cursor is in
	IsNewExpr   bool   // true if this is a "new Foo(...)" expression
	Constructor string // type name for new-expression (e.g. "Foo")
}

type MethodDecl struct {
	Name       string
	Params     []string
	ReturnType *index.TypeExpr
}

// CompletionCtx holds the parsed context for a completion request.
type CompletionCtx struct {
	Kind           CompletionKind
	Scope          CompletionScope
	Receiver       string       // the text before the dot (for dot completion)
	Prefix         string       // the identifier prefix being typed
	LambdaParams   []ValueDecl  // parameters of the nearest enclosing lambda
	Locals         []ValueDecl  // local variables visible at cursor
	Params         []ValueDecl  // method parameters
	ClassFields    []ValueDecl  // fields of enclosing class
	ClassMethods   []MethodDecl // methods of enclosing class
	Imports        []ImportSpec
	Package        string
	EnclosingClass string       // simple name of the enclosing class
	Call           *CallContext // non-nil when cursor is inside method call arguments
}

var _ = javaParserPool

// parseCompletionCtx parses the buffer content with Tree-sitter and extracts
// completion context at the given cursor position (0-indexed line and character).
func parseCompletionCtx(logger *log.Logger, content []byte, line, character int) (ctx *CompletionCtx) {
	defer func() {
		if r := recover(); r != nil {
			if logger != nil {
				logger.Printf("panic in parseCompletionCtx: %v\n%s", r, debug.Stack())
			}
			// Return an empty context if parsing or extraction panics.
			ctx = &CompletionCtx{}
		}
	}()

	parser := javaParserPool.Get().(*slog.Parser)
	defer javaParserPool.Put(parser)

	tree, err := parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return &CompletionCtx{}
	}
	root := tree.RootNode()

	ctx = &CompletionCtx{}

	// 1. Determine cursor byte offset.
	cursorOffset := PositionToByteOffset(content, line, character)

	// 2. Determine completion kind and extract receiver/prefix.
	ctx.Kind, ctx.Receiver, ctx.Prefix = determineCompletionKind(content, cursorOffset, root)

	// 3. Extract imports and package from root.
	extractImportsAndPackage(root, content, ctx)

	// 4. Find enclosing class and method to determine scope.
	cursorNode := nodeAtPosition(root, line, character)
	if cursorNode != nil {
		classNode := findAncestor(cursorNode, "class_declaration", "interface_declaration", "enum_declaration")
		if classNode != nil {
			ctx.Scope = ScopeClass
			for i := 0; i < int(classNode.NamedChildCount()); i++ {
				child := classNode.NamedChild(i)
				if child.Type() == "identifier" {
					ctx.EnclosingClass = child.Content(content)
					break
				}
			}

			// 5. Extract class fields and methods.
			extractClassMembers(classNode, content, ctx)
		}

		// Find enclosing method or block for scope.
		blockNode := findAncestor(cursorNode, "block", "constructor_body", "static_initializer")
		if blockNode != nil {
			ctx.Scope = ScopeBlock
		}

		// Find specific method/constructor for parameter extraction.
		methodNode := findAncestor(cursorNode, "method_declaration", "constructor_declaration")
		if methodNode != nil {
			// 7. Extract method parameters.
			extractMethodParams(methodNode, content, ctx)
		}

		// 8. Extract local variables from current scope.
		extractLocals(cursorNode, content, cursorOffset, ctx)

		// 9. Detect if cursor is inside a method call's argument list.
		ctx.Call = extractCallContext(cursorNode, content, cursorOffset)

		// 10. Extract parameters from the nearest enclosing lambda body.
		extractLambdaParams(cursorNode, content, ctx)
	}

	return ctx
}

// determineCompletionKind examines text before cursor to detect dot completion,
// using the Tree-sitter AST to extract the receiver expression.
func determineCompletionKind(content []byte, cursorOffset int, root *slog.Node) (CompletionKind, string, string) {
	// Walk backwards from cursor to collect identifier prefix.
	pos := cursorOffset
	for pos > 0 && isIdentChar(content[pos-1]) {
		pos--
	}
	prefix := string(content[pos:cursorOffset])

	// Check if there's a dot before the prefix.
	dotPos := pos
	// Skip whitespace (including newlines) between dot and prefix.
	for dotPos > 0 && (content[dotPos-1] == ' ' || content[dotPos-1] == '\t' || content[dotPos-1] == '\n' || content[dotPos-1] == '\r') {
		dotPos--
	}
	if dotPos > 0 && content[dotPos-1] == '.' {
		dotBytePos := dotPos - 1 // index of the dot
		receiver := extractReceiverFromAST(root, content, dotBytePos)
		if receiver != "" {
			return CompletionDot, receiver, prefix
		}
	}

	return CompletionLexical, "", prefix
}

// extractReceiverFromAST uses the Tree-sitter AST to extract the receiver
// expression before the dot at dotBytePos.
func extractReceiverFromAST(root *slog.Node, content []byte, dotBytePos int) string {
	if dotBytePos <= 0 {
		return ""
	}

	// Find the last non-whitespace byte before the dot.
	// This handles chained calls across lines like:
	//   items.stream()
	//       .filter(...)
	// where the byte before '.' may be whitespace/newline, not ')'.
	beforeDot := dotBytePos - 1
	for beforeDot > 0 && (content[beforeDot] == ' ' || content[beforeDot] == '\t' || content[beforeDot] == '\n' || content[beforeDot] == '\r') {
		beforeDot--
	}

	// Find the AST node at the last non-whitespace byte before the dot.
	line, col := byteOffsetToPosition(content, beforeDot)
	node := nodeAtPosition(root, line, col)
	if node == nil {
		return ""
	}

	// Walk up to the highest expression node that ends at or before the dot.
	for node.Parent() != nil {
		parent := node.Parent()
		if int(parent.EndByte()) <= dotBytePos {
			node = parent
		} else {
			break
		}
	}

	// For debugging, we can't easily log here without h.logger.
	// We'll trust the caller (handleCompletion) to log the resulting receiver string.
	return exprToReceiver(node, content)
}

// exprToReceiver converts a Tree-sitter expression node to a dot-separated
// receiver string suitable for type resolution (stripping method arguments).
func exprToReceiver(node *slog.Node, content []byte) string {
	if node == nil {
		return ""
	}
	switch node.Type() {
	case "identifier":
		return node.Content(content)
	case "this":
		return "this"
	case "super":
		return "super"
	case "string_literal":
		return node.Content(content)
	case "parenthesized_expression":
		return node.Content(content)
	case "field_access":
		obj := node.ChildByFieldName("object")
		field := node.ChildByFieldName("field")
		if obj != nil && field != nil {
			recv := exprToReceiver(obj, content)
			if recv != "" {
				return recv + "." + field.Content(content)
			}
			return field.Content(content)
		}
		if obj != nil {
			return exprToReceiver(obj, content)
		}
	case "method_invocation":
		obj := node.ChildByFieldName("object")
		name := node.ChildByFieldName("name")
		if obj != nil && name != nil {
			recv := exprToReceiver(obj, content)
			if recv != "" {
				return recv + "." + name.Content(content)
			}
			return name.Content(content)
		}
		if name != nil {
			return name.Content(content)
		}
	}
	// Fallback: return content if it looks like an identifier.
	text := node.Content(content)
	if len(text) > 0 && isIdentChar(text[0]) {
		return text
	}
	return ""
}

// byteOffsetToPosition converts a byte offset to a 0-indexed (line, column) pair.
func byteOffsetToPosition(content []byte, offset int) (int, int) {
	line, col := 0, 0
	for i := 0; i < offset && i < len(content); i++ {
		if content[i] == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	return line, col
}

func isIdentChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '$'
}

// extractImportsAndPackage walks root children to find package and import declarations.
func extractImportsAndPackage(root *slog.Node, content []byte, ctx *CompletionCtx) {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		switch child.Type() {
		case "package_declaration":
			// The package name is in a scoped_identifier or identifier child.
			for j := 0; j < int(child.NamedChildCount()); j++ {
				gc := child.NamedChild(j)
				if gc.Type() == "scoped_identifier" || gc.Type() == "identifier" {
					ctx.Package = gc.Content(content)
				}
			}
		case "import_declaration":
			spec := parseImport(child, content)
			if spec.Path != "" {
				ctx.Imports = append(ctx.Imports, spec)
			}
		}
	}
}

// extractType extracts the type structure from a type node.
func extractType(node *slog.Node, content []byte) *index.TypeExpr {
	if node == nil {
		return nil
	}
	switch node.Type() {
	case "type_identifier", "primitive_type", "void_type", "boolean_type":
		// Only extract the content if it's a simple identifier/keyword.
		// If it contains dots or spaces, Tree-sitter probably merged nodes due to an ERROR.
		c := node.Content(content)
		if strings.ContainsAny(c, ". \n\t") {
			return nil
		}
		return &index.TypeExpr{Sym: c}
	case "generic_type":
		// generic_type has a base type and type_arguments.
		var base *index.TypeExpr
		var args []*index.TypeExpr
		for i := 0; i < int(node.NamedChildCount()); i++ {
			child := node.NamedChild(i)
			if child.Type() == "type_arguments" {
				for j := 0; j < int(child.NamedChildCount()); j++ {
					arg := extractType(child.NamedChild(j), content)
					if arg != nil {
						args = append(args, arg)
					}
				}
			} else {
				base = extractType(child, content)
			}
		}
		if base != nil {
			base.Args = args
			return base
		}
	case "array_type":
		// Element type + "[]".
		if node.NamedChildCount() > 0 {
			base := extractType(node.NamedChild(0), content)
			if base != nil {
				base.Sym += "[]"
				return base
			}
		}
	case "scoped_type_identifier":
		c := node.Content(content)
		// For scoped identifiers (e.g. "java.util.List"), we allow dots but not whitespace.
		if strings.ContainsAny(c, " \n\t") {
			return nil
		}
		return &index.TypeExpr{Sym: c}
	}
	// Fallback to content if it's not a complex node.
	c := node.Content(content)
	if strings.ContainsAny(c, " \n\t") {
		return nil
	}
	return &index.TypeExpr{Sym: c}
}

// extractDeclarators extracts variable names from variable_declarator children.
func extractDeclarators(node *slog.Node, content []byte) []string {
	var names []string
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() == "variable_declarator" {
			for j := 0; j < int(child.NamedChildCount()); j++ {
				gc := child.NamedChild(j)
				if gc.Type() == "identifier" {
					names = append(names, gc.Content(content))
					break
				}
			}
		}
	}
	return names
}

// extractClassMembers extracts fields and methods from the enclosing class.
func extractClassMembers(classNode *slog.Node, content []byte, ctx *CompletionCtx) {
	// Find the class_body.
	var classBody *slog.Node
	for i := 0; i < int(classNode.NamedChildCount()); i++ {
		child := classNode.NamedChild(i)
		if child.Type() == "class_body" || child.Type() == "interface_body" || child.Type() == "enum_body" {
			classBody = child
			break
		}
	}
	if classBody == nil {
		return
	}

	for i := 0; i < int(classBody.NamedChildCount()); i++ {
		child := classBody.NamedChild(i)
		switch child.Type() {
		case "field_declaration":
			var typeExpr *index.TypeExpr
			for j := 0; j < int(child.NamedChildCount()); j++ {
				gc := child.NamedChild(j)
				switch gc.Type() {
				case "type_identifier", "primitive_type", "generic_type", "array_type",
					"scoped_type_identifier", "void_type", "boolean_type":
					typeExpr = extractType(gc, content)
				}
			}
			names := extractDeclarators(child, content)
			for _, name := range names {
				ctx.ClassFields = append(ctx.ClassFields, ValueDecl{Name: name, Type: typeExpr})
			}
		case "method_declaration":
			if decl := extractMethodDecl(child, content); decl.Name != "" {
				ctx.ClassMethods = append(ctx.ClassMethods, decl)
			}
		}
	}
}

func extractMethodDecl(methodNode *slog.Node, content []byte) MethodDecl {
	var decl MethodDecl
	for i := 0; i < int(methodNode.NamedChildCount()); i++ {
		child := methodNode.NamedChild(i)
		switch child.Type() {
		case "identifier":
			if decl.Name == "" {
				decl.Name = child.Content(content)
			}
		case "formal_parameters":
			decl.Params = extractParameterNames(child, content)
		case "type_identifier", "primitive_type", "generic_type", "array_type",
			"scoped_type_identifier", "void_type", "boolean_type":
			if decl.ReturnType == nil {
				decl.ReturnType = extractType(child, content)
			}
		}
	}
	return decl
}

func extractParameterNames(paramsNode *slog.Node, content []byte) []string {
	var params []string
	if paramsNode == nil {
		return nil
	}
	for i := 0; i < int(paramsNode.NamedChildCount()); i++ {
		param := paramsNode.NamedChild(i)
		if param.Type() != "formal_parameter" && param.Type() != "spread_parameter" {
			continue
		}
		for j := 0; j < int(param.NamedChildCount()); j++ {
			child := param.NamedChild(j)
			if child.Type() == "identifier" {
				params = append(params, child.Content(content))
				break
			}
		}
	}
	return params
}

// extractMethodParams extracts parameters from the enclosing method.
func extractMethodParams(methodNode *slog.Node, content []byte, ctx *CompletionCtx) {
	for i := 0; i < int(methodNode.NamedChildCount()); i++ {
		child := methodNode.NamedChild(i)
		if child.Type() == "formal_parameters" {
			for j := 0; j < int(child.NamedChildCount()); j++ {
				param := child.NamedChild(j)
				if param.Type() == "formal_parameter" || param.Type() == "spread_parameter" {
					var typeExpr *index.TypeExpr
					paramName := ""
					for k := 0; k < int(param.NamedChildCount()); k++ {
						gc := param.NamedChild(k)
						switch gc.Type() {
						case "type_identifier", "primitive_type", "generic_type", "array_type",
							"scoped_type_identifier", "void_type", "boolean_type":
							typeExpr = extractType(gc, content)
						case "identifier":
							paramName = gc.Content(content)
						}
					}
					if paramName != "" {
						ctx.Params = append(ctx.Params, ValueDecl{Name: paramName, Type: typeExpr})
					}
				}
			}
			break
		}
	}
}

func extractLambdaParams(cursorNode *slog.Node, content []byte, ctx *CompletionCtx) {
	var lambdas []*slog.Node
	for n := cursorNode; n != nil; n = n.Parent() {
		if n.Type() != "lambda_expression" {
			continue
		}
		body := n.ChildByFieldName("body")
		if body == nil || !nodeContains(body, cursorNode) {
			continue
		}
		lambdas = append(lambdas, n)
	}

	// Store parameters from outermost to innermost so later lookups can walk
	// the slice in reverse to apply Java's nearest-scope shadowing.
	for i := len(lambdas) - 1; i >= 0; i-- {
		appendLambdaParams(lambdas[i], content, ctx)
	}
}

func appendLambdaParams(lambdaNode *slog.Node, content []byte, ctx *CompletionCtx) {
	paramsNode := lambdaNode.ChildByFieldName("parameters")
	if paramsNode == nil {
		return
	}

	switch paramsNode.Type() {
	case "identifier", "_reserved_identifier":
		ctx.LambdaParams = append(ctx.LambdaParams, ValueDecl{Name: paramsNode.Content(content)})
	case "inferred_parameters":
		for i := 0; i < int(paramsNode.NamedChildCount()); i++ {
			child := paramsNode.NamedChild(i)
			if child.Type() == "identifier" || child.Type() == "_reserved_identifier" {
				ctx.LambdaParams = append(ctx.LambdaParams, ValueDecl{Name: child.Content(content)})
			}
		}
	case "formal_parameters":
		for i := 0; i < int(paramsNode.NamedChildCount()); i++ {
			param := paramsNode.NamedChild(i)
			if param.Type() != "formal_parameter" && param.Type() != "spread_parameter" {
				continue
			}
			var typeExpr *index.TypeExpr
			paramName := ""
			for j := 0; j < int(param.NamedChildCount()); j++ {
				child := param.NamedChild(j)
				switch child.Type() {
				case "type_identifier", "primitive_type", "generic_type", "array_type",
					"scoped_type_identifier", "void_type", "boolean_type":
					typeExpr = extractType(child, content)
				case "identifier", "_reserved_identifier":
					paramName = child.Content(content)
				}
			}
			if paramName != "" {
				ctx.LambdaParams = append(ctx.LambdaParams, ValueDecl{Name: paramName, Type: typeExpr})
			}
		}
	}
}

func nodeContains(parent, child *slog.Node) bool {
	if parent == nil || child == nil {
		return false
	}
	return parent.StartByte() <= child.StartByte() && parent.EndByte() >= child.EndByte()
}

// extractLocals extracts local variable declarations that are visible at the cursor.
func extractLocals(cursorNode *slog.Node, content []byte, cursorOffset int, ctx *CompletionCtx) {
	// If the cursor is on a node (like a blank line), we want to collect preceding siblings
	// in the block it belongs to, and then move up to parent blocks.
	for n := cursorNode; n != nil; n = n.Parent() {
		parent := n.Parent()
		if parent == nil {
			break
		}

		// Stop if we hit a class boundary.
		if parent.Type() == "class_declaration" || parent.Type() == "interface_declaration" || parent.Type() == "enum_declaration" {
			break
		}

		// Extract variables introduced by enclosing control structures.
		switch parent.Type() {
		case "enhanced_for_statement":
			// for (Type item : collection) { ... }
			collectEnhancedForVar(parent, content, ctx)
		case "catch_clause":
			// catch (ExceptionType e) { ... }
			collectCatchVar(parent, content, ctx)
		case "try_with_resources_statement":
			// try (Type res = ...) { ... }
			collectResourceVars(parent, content, ctx)
		}

		// Collect all preceding siblings in this level.
		for i := 0; i < int(parent.NamedChildCount()); i++ {
			child := parent.NamedChild(i)
			// Only consider nodes that fully end before the cursor offset OR the current node starts after it.
			// Actually, if we are walking up, we just want nodes in the parent that appear before 'n'.
			if child.StartByte() >= n.StartByte() {
				break
			}
			collectLocalDecls(child, content, ctx)
		}

		// If the current node 'n' is a block, we also want to collect its children that are before the cursor.
		// This happens if the cursor is at the end of a block.
		if n.Type() == "block" || n.Type() == "constructor_body" {
			for i := 0; i < int(n.NamedChildCount()); i++ {
				child := n.NamedChild(i)
				if int(child.StartByte()) >= cursorOffset {
					break
				}
				collectLocalDecls(child, content, ctx)
			}
		}
	}

	// Deduplicate by name (keeping the first one found, which is innermost).
	seen := make(map[string]struct{})
	var deduped []ValueDecl
	for _, l := range ctx.Locals {
		if _, ok := seen[l.Name]; !ok {
			deduped = append(deduped, l)
			seen[l.Name] = struct{}{}
		}
	}
	ctx.Locals = deduped
}

// collectEnhancedForVar extracts the loop variable from an enhanced_for_statement.
// Tree-sitter structure: enhanced_for_statement [ type_identifier, identifier, expression, block ]
func collectEnhancedForVar(node *slog.Node, content []byte, ctx *CompletionCtx) {
	var typeExpr *index.TypeExpr
	var name string
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch child.Type() {
		case "type_identifier", "generic_type", "scoped_type_identifier":
			typeExpr = extractType(child, content)
		case "identifier":
			if name == "" {
				name = child.Content(content)
			}
		}
	}
	if name != "" {
		ctx.Locals = append(ctx.Locals, ValueDecl{Name: name, Type: typeExpr})
	}
}

// collectCatchVar extracts the exception variable from a catch_clause.
// Tree-sitter structure: catch_clause [ catch_formal_parameter [ catch_type [ type_identifier ], identifier ] ]
func collectCatchVar(node *slog.Node, content []byte, ctx *CompletionCtx) {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() == "catch_formal_parameter" {
			var typeExpr *index.TypeExpr
			var name string
			for j := 0; j < int(child.NamedChildCount()); j++ {
				gc := child.NamedChild(j)
				switch gc.Type() {
				case "catch_type":
					// catch_type may contain one or more type_identifiers (multi-catch).
					// Use the first one for type resolution.
					if gc.NamedChildCount() > 0 {
						typeExpr = extractType(gc.NamedChild(0), content)
					}
				case "identifier":
					name = gc.Content(content)
				}
			}
			if name != "" {
				ctx.Locals = append(ctx.Locals, ValueDecl{Name: name, Type: typeExpr})
			}
			return
		}
	}
}

// collectResourceVars extracts variables from try-with-resources.
// Tree-sitter structure: try_with_resources_statement [ resource_specification [ resource [ type, identifier, expression ] ] ]
func collectResourceVars(node *slog.Node, content []byte, ctx *CompletionCtx) {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() == "resource_specification" {
			for j := 0; j < int(child.NamedChildCount()); j++ {
				res := child.NamedChild(j)
				if res.Type() == "resource" {
					var typeExpr *index.TypeExpr
					var name string
					for k := 0; k < int(res.NamedChildCount()); k++ {
						gc := res.NamedChild(k)
						switch gc.Type() {
						case "type_identifier", "generic_type", "scoped_type_identifier":
							typeExpr = extractType(gc, content)
						case "identifier":
							if name == "" {
								name = gc.Content(content)
							}
						}
					}
					if name != "" {
						ctx.Locals = append(ctx.Locals, ValueDecl{Name: name, Type: typeExpr})
					}
				}
			}
			return
		}
	}
}

// collectLocalDecls extracts variables from a single local_variable_declaration node.
func collectLocalDecls(node *slog.Node, content []byte, ctx *CompletionCtx) {
	if node.Type() == "local_variable_declaration" {
		var typeExpr *index.TypeExpr
		isVar := false
		for j := 0; j < int(node.NamedChildCount()); j++ {
			gc := node.NamedChild(j)
			switch gc.Type() {
			case "type_identifier":
				if gc.Content(content) == "var" {
					isVar = true
				} else {
					typeExpr = extractType(gc, content)
				}
			case "primitive_type", "generic_type", "array_type",
				"scoped_type_identifier", "void_type", "boolean_type":
				typeExpr = extractType(gc, content)
			}
		}
		// For "var" declarations, infer type from the initializer expression.
		var init *VarInitializer
		if isVar {
			typeExpr, init = inferTypeFromDeclarator(node, content)
		}
		names := extractDeclarators(node, content)
		for _, name := range names {
			ctx.Locals = append(ctx.Locals, ValueDecl{Name: name, Type: typeExpr, Initializer: init})
		}
	}
}

// inferTypeFromDeclarator infers the type of a "var" declaration from its initializer.
// Returns a directly-resolved TypeExpr for simple cases (new, cast, literals),
// or a VarInitializer for method calls that require index-based resolution.
func inferTypeFromDeclarator(node *slog.Node, content []byte) (*index.TypeExpr, *VarInitializer) {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() == "variable_declarator" {
			// Find the initializer expression (the child after "=").
			for j := 0; j < int(child.NamedChildCount()); j++ {
				init := child.NamedChild(j)
				te, vi := inferTypeFromExpr(init, content)
				if te != nil || vi != nil {
					return te, vi
				}
			}
		}
	}
	return nil, nil
}

// inferTypeFromExpr infers the type from an expression node.
func inferTypeFromExpr(node *slog.Node, content []byte) (*index.TypeExpr, *VarInitializer) {
	switch node.Type() {
	case "object_creation_expression":
		return extractTypeFromNewExpr(node, content), nil
	case "cast_expression":
		return extractTypeFromCast(node, content), nil
	case "string_literal":
		return &index.TypeExpr{Sym: "String"}, nil
	case "integer_literal", "decimal_integer_literal", "hex_integer_literal",
		"octal_integer_literal", "binary_integer_literal":
		text := node.Content(content)
		if strings.HasSuffix(text, "L") || strings.HasSuffix(text, "l") {
			return &index.TypeExpr{Sym: "long"}, nil
		}
		return &index.TypeExpr{Sym: "int"}, nil
	case "decimal_floating_point_literal", "floating_point_literal":
		text := node.Content(content)
		if strings.HasSuffix(text, "f") || strings.HasSuffix(text, "F") {
			return &index.TypeExpr{Sym: "float"}, nil
		}
		return &index.TypeExpr{Sym: "double"}, nil
	case "true", "false":
		return &index.TypeExpr{Sym: "boolean"}, nil
	case "character_literal":
		return &index.TypeExpr{Sym: "char"}, nil
	case "null_literal":
		return nil, nil
	case "array_creation_expression":
		return extractTypeFromArrayCreation(node, content), nil
	case "method_invocation":
		if vi := extractMethodInvocationInfo(node, content); vi != nil {
			return nil, vi
		}
	}
	return nil, nil
}

// extractMethodInvocationInfo extracts receiver and method name from a method_invocation.
// Handles both simple patterns (List.of(...)) and chained calls (builder.name("a").build()).
// Uses exprToReceiver to flatten the receiver expression into a dot-separated string.
func extractMethodInvocationInfo(node *slog.Node, content []byte) *VarInitializer {
	name := node.ChildByFieldName("name")
	if name == nil {
		return nil
	}
	obj := node.ChildByFieldName("object")
	if obj == nil {
		return nil
	}
	recv := exprToReceiver(obj, content)
	if recv == "" {
		return nil
	}
	return &VarInitializer{
		Receiver:   recv,
		MethodName: name.Content(content),
	}
}

// extractTypeFromNewExpr extracts the type from an object_creation_expression node.
// e.g. "new ArrayList<String>()" → TypeExpr{Sym: "ArrayList", Args: [{Sym: "String"}]}
func extractTypeFromNewExpr(node *slog.Node, content []byte) *index.TypeExpr {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch child.Type() {
		case "type_identifier", "generic_type", "scoped_type_identifier":
			return extractType(child, content)
		}
	}
	return nil
}

// extractTypeFromCast extracts the target type from a cast_expression node.
// e.g. "(MyClass) expr" → TypeExpr{Sym: "MyClass"}
func extractTypeFromCast(node *slog.Node, content []byte) *index.TypeExpr {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch child.Type() {
		case "type_identifier", "generic_type", "scoped_type_identifier":
			return extractType(child, content)
		}
	}
	return nil
}

// extractTypeFromArrayCreation extracts the element type from an array_creation_expression.
// e.g. "new int[]{1,2,3}" → TypeExpr{Sym: "int"}, "new String[10]" → TypeExpr{Sym: "String"}
func extractTypeFromArrayCreation(node *slog.Node, content []byte) *index.TypeExpr {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch child.Type() {
		case "type_identifier", "generic_type", "scoped_type_identifier":
			return extractType(child, content)
		case "primitive_type", "boolean_type", "void_type",
			"integral_type", "floating_point_type":
			return &index.TypeExpr{Sym: child.Content(content)}
		}
	}
	return nil
}

// extractCallContext detects if the cursor is inside a method call's argument list
// and extracts the receiver, method name, and active parameter index.
func extractCallContext(cursorNode *slog.Node, content []byte, cursorOffset int) *CallContext {
	// Walk up to find the enclosing argument_list.
	argList := cursorNode
	for argList != nil && argList.Type() != "argument_list" {
		argList = argList.Parent()
	}
	if argList == nil {
		return nil
	}

	// Count which argument the cursor is in (0-based) by counting commas before cursor.
	paramIndex := 0
	for i := 0; i < int(argList.ChildCount()); i++ {
		child := argList.Child(i)
		if int(child.StartByte()) >= cursorOffset {
			break
		}
		if child.Type() == "," {
			paramIndex++
		}
	}

	callNode := argList.Parent()
	if callNode == nil {
		return nil
	}

	switch callNode.Type() {
	case "method_invocation":
		nameNode := callNode.ChildByFieldName("name")
		if nameNode == nil {
			return nil
		}
		cc := &CallContext{
			MethodName: nameNode.Content(content),
			ParamIndex: paramIndex,
		}
		if objNode := callNode.ChildByFieldName("object"); objNode != nil {
			cc.Receiver = exprToReceiver(objNode, content)
		}
		return cc

	case "object_creation_expression":
		te := extractTypeFromNewExpr(callNode, content)
		if te == nil {
			return nil
		}
		return &CallContext{
			IsNewExpr:   true,
			Constructor: te.Sym,
			ParamIndex:  paramIndex,
		}
	}

	return nil
}
