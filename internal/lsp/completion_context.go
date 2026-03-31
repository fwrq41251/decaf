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

// VarInitializer holds info about a var declaration's initializer expression
// for deferred type inference (requires index access).
type VarInitializer struct {
	Receiver   string // receiver class name, e.g. "List" for List.of()
	MethodName string // method name, e.g. "of"
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

// CompletionCtx holds the parsed context for a completion request.
type CompletionCtx struct {
	Kind           CompletionKind
	Receiver       string       // the text before the dot (for dot completion)
	Prefix         string       // the identifier prefix being typed
	Locals         []ValueDecl  // local variables visible at cursor
	Params         []ValueDecl  // method parameters
	ClassFields    []ValueDecl  // fields of enclosing class
	ClassMethods   []string     // method names of enclosing class
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

	// 4. Find enclosing class.
	cursorNode := nodeAtPosition(root, line, character)
	if cursorNode != nil {
		classNode := findAncestor(cursorNode, "class_declaration", "interface_declaration", "enum_declaration")
		if classNode != nil {
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

		// Find enclosing method.
		methodNode := findAncestor(cursorNode, "method_declaration", "constructor_declaration")
		if methodNode != nil {
			// 7. Extract method parameters.
			extractMethodParams(methodNode, content, ctx)
		}

		// 8. Extract local variables from current scope.
		extractLocals(cursorNode, content, cursorOffset, ctx)

		// 9. Detect if cursor is inside a method call's argument list.
		ctx.Call = extractCallContext(cursorNode, content, cursorOffset)
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
	// Skip whitespace between dot and prefix.
	for dotPos > 0 && (content[dotPos-1] == ' ' || content[dotPos-1] == '\t') {
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

	// Find the AST node at the byte just before the dot.
	line, col := byteOffsetToPosition(content, dotBytePos-1)
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
			for j := 0; j < int(child.NamedChildCount()); j++ {
				gc := child.NamedChild(j)
				if gc.Type() == "identifier" {
					ctx.ClassMethods = append(ctx.ClassMethods, gc.Content(content))
					break
				}
			}
		}
	}
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
// Returns a directly-resolved TypeExpr for simple cases (new, cast, string literal),
// or a VarInitializer for method calls that require index-based resolution.
func inferTypeFromDeclarator(node *slog.Node, content []byte) (*index.TypeExpr, *VarInitializer) {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() == "variable_declarator" {
			// Find the initializer expression (the child after "=").
			for j := 0; j < int(child.NamedChildCount()); j++ {
				init := child.NamedChild(j)
				switch init.Type() {
				case "object_creation_expression":
					// new ArrayList<String>() → extract type from the constructor.
					return extractTypeFromNewExpr(init, content), nil
				case "cast_expression":
					// (MyClass) expr → extract the target type.
					return extractTypeFromCast(init, content), nil
				case "string_literal":
					return &index.TypeExpr{Sym: "String"}, nil
				case "method_invocation":
					// List.of(...) → store receiver + method for deferred resolution.
					if vi := extractMethodInvocationInfo(init, content); vi != nil {
						return nil, vi
					}
				}
			}
		}
	}
	return nil, nil
}

// extractMethodInvocationInfo extracts receiver and method name from a method_invocation.
// e.g. "List.of(...)" → VarInitializer{Receiver: "List", MethodName: "of"}
// Only handles simple "Class.method(...)" patterns (static factory methods).
func extractMethodInvocationInfo(node *slog.Node, content []byte) *VarInitializer {
	obj := node.ChildByFieldName("object")
	name := node.ChildByFieldName("name")
	if obj == nil || name == nil {
		return nil
	}
	// Only handle simple identifiers as receiver (e.g. "List", "Path", "Files").
	if obj.Type() != "identifier" {
		return nil
	}
	return &VarInitializer{
		Receiver:   obj.Content(content),
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

	// Count which argument the cursor is in (0-based).
	paramIndex := 0
	for i := 0; i < int(argList.NamedChildCount()); i++ {
		child := argList.NamedChild(i)
		if int(child.StartByte()) >= cursorOffset {
			break
		}
		paramIndex = i
	}
	// If cursor is before the first argument or argList has no named children,
	// paramIndex stays 0.
	if argList.NamedChildCount() == 0 {
		paramIndex = 0
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
