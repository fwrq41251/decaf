package lsp

import (
	"context"

	"github.com/fwrq41251/decaf/internal/index"
	slog "github.com/smacker/go-tree-sitter"
)

// CompletionKind distinguishes between lexical and dot-triggered completion.
type CompletionKind int

const (
	CompletionLexical CompletionKind = iota
	CompletionDot
)

// ValueDecl represents a variable/field/parameter declaration with its type.
type ValueDecl struct {
	Name string
	Type *index.TypeExpr // Changed from string to *index.TypeExpr
}

// CompletionCtx holds the parsed context for a completion request.
type CompletionCtx struct {
	Kind           CompletionKind
	Receiver       string      // the text before the dot (for dot completion)
	Prefix         string      // the identifier prefix being typed
	Locals         []ValueDecl // local variables visible at cursor
	Params         []ValueDecl // method parameters
	ClassFields    []ValueDecl // fields of enclosing class
	ClassMethods   []string    // method names of enclosing class
	Imports        []ImportSpec
	Package        string
	EnclosingClass string // simple name of the enclosing class
}

var _ = javaParserPool

// parseCompletionCtx parses the buffer content with Tree-sitter and extracts
// completion context at the given cursor position (0-indexed line and character).
func parseCompletionCtx(content []byte, line, character int) *CompletionCtx {
	parser := javaParserPool.Get().(*slog.Parser)
	defer javaParserPool.Put(parser)

	tree, err := parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return &CompletionCtx{}
	}
	root := tree.RootNode()

	ctx := &CompletionCtx{}

	// 1. Determine cursor byte offset.
	cursorOffset := byteOffsetForPosition(content, line, character)

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
	}

	return ctx
}
// byteOffsetForPosition converts a 0-indexed line and character to a byte offset.
func byteOffsetForPosition(content []byte, line, character int) int {
	offset := 0
	for l := 0; l < line; l++ {
		idx := indexByte(content[offset:], '\n')
		if idx < 0 {
			return len(content)
		}
		offset += idx + 1
	}
	offset += character
	if offset > len(content) {
		offset = len(content)
	}
	return offset
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
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
		return &index.TypeExpr{Sym: node.Content(content)}
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
		return &index.TypeExpr{Sym: node.Content(content)}
	}
	return &index.TypeExpr{Sym: node.Content(content)}
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
}

// collectLocalDecls extracts variables from a single local_variable_declaration node.
func collectLocalDecls(node *slog.Node, content []byte, ctx *CompletionCtx) {
	if node.Type() == "local_variable_declaration" {
		var typeExpr *index.TypeExpr
		for j := 0; j < int(node.NamedChildCount()); j++ {
			gc := node.NamedChild(j)
			switch gc.Type() {
			case "type_identifier", "primitive_type", "generic_type", "array_type",
				"scoped_type_identifier", "void_type", "boolean_type":
				typeExpr = extractType(gc, content)
			}
		}
		names := extractDeclarators(node, content)
		for _, name := range names {
			ctx.Locals = append(ctx.Locals, ValueDecl{Name: name, Type: typeExpr})
		}
	}
}
