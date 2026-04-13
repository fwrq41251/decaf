package lsp

import (
	"fmt"
	"os"
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"github.com/fwrq41251/decaf/internal/uri"
	slog "github.com/smacker/go-tree-sitter"
)

// extractUnimplementedInfo parses a javac diagnostic message of the form:
//
//	"ClassName is not abstract and does not override abstract method methodName(...) in ParentName"
//
// and returns the class name, method name (without params), and parent name.
func extractUnimplementedInfo(msg string) (className, methodName, parentName string) {
	const marker = " is not abstract and does not override abstract method "
	idx := strings.Index(msg, marker)
	if idx < 0 {
		return "", "", ""
	}
	className = msg[:idx]

	rest := msg[idx+len(marker):]
	// rest looks like "methodName(...) in ParentName"
	parenIdx := strings.Index(rest, "(")
	if parenIdx < 0 {
		return "", "", ""
	}
	methodName = rest[:parenIdx]

	const inMarker = " in "
	inIdx := strings.Index(rest, inMarker)
	if inIdx < 0 {
		return "", "", ""
	}
	parentName = rest[inIdx+len(inMarker):]
	return className, methodName, parentName
}

// implementMethodsEdit computes a WorkspaceEdit that inserts method stubs
// for all unimplemented abstract methods in the class identified by diag.
func implementMethodsEdit(fileURI string, idx *index.Index, overlay string, diag Diagnostic) *WorkspaceEdit {
	className, _, _ := extractUnimplementedInfo(diag.Message)
	if className == "" {
		return nil
	}

	content := readContent(fileURI, overlay)
	if content == nil {
		return nil
	}

	tree, err := getTree(content)
	if err != nil {
		return nil
	}
	root := tree.RootNode()

	insertLine := findClassInsertionPoint(root, content, diag.Range.Start.Line)
	if insertLine < 0 {
		return nil
	}

	return implementMethodsForClass(fileURI, idx, className, insertLine)
}

func implementMethodsSourceEdit(fileURI string, idx *index.Index, overlay string, cursorLine int) *WorkspaceEdit {
	content := readContent(fileURI, overlay)
	if content == nil {
		return nil
	}

	tree, err := getTree(content)
	if err != nil {
		return nil
	}
	root := tree.RootNode()

	className, _ := findClassContext(root, content, cursorLine)
	if className == "" {
		return nil
	}

	insertLine := findClassInsertionPoint(root, content, cursorLine)
	if insertLine < 0 {
		return nil
	}

	return implementMethodsForClass(fileURI, idx, className, insertLine)
}

func implementMethodsForClass(fileURI string, idx *index.Index, className string, insertLine int) *WorkspaceEdit {
	// Find the class symbol from the index.
	var classSym index.Symbol
	found := false
	for _, sym := range idx.FileSymbols(fileURI) {
		if sym.Name == className && sym.Kind == sdb.SymbolInformation_CLASS {
			classSym = sym
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	stubs := missingAbstractMethodStubs(classSym.Symbol, idx)
	if len(stubs) == 0 {
		return nil
	}

	return &WorkspaceEdit{
		Changes: map[string][]TextEdit{
			fileURI: {{
				Range: Range{
					Start: Position{Line: insertLine, Character: 0},
					End:   Position{Line: insertLine, Character: 0},
				},
				NewText: strings.Join(stubs, ""),
			}},
		},
	}
}

func missingAbstractMethodStubs(classSym string, idx *index.Index) []string {
	implemented := make(map[string]bool)
	// Collect methods already implemented in the class itself.
	for _, m := range idx.DirectMembersOfType(classSym) {
		if m.Kind == sdb.SymbolInformation_METHOD && !m.IsStatic && !m.IsAbstract {
			implemented[m.Name] = true
		}
	}

	type abstractMethod struct {
		sym   index.Symbol
		owner *index.TypeExpr
	}
	var abstractMethods []abstractMethod
	seenAbstract := make(map[string]bool)
	visitedTypes := make(map[string]bool)

	var collect func(sym string, owner *index.TypeExpr)
	collect = func(sym string, owner *index.TypeExpr) {
		if sym == "" || sym == "java/lang/Object#" || visitedTypes[sym] {
			return
		}
		visitedTypes[sym] = true

		members := idx.DirectMembersOfType(sym)
		// First pass: collect all concrete implementations in this type.
		for _, m := range members {
			if m.Kind == sdb.SymbolInformation_METHOD && !m.IsStatic && !m.IsAbstract {
				implemented[m.Name] = true
			}
		}

		// Second pass: collect abstract methods.
		for _, m := range members {
			if m.Kind == sdb.SymbolInformation_METHOD && !m.IsStatic && m.IsAbstract {
				if !seenAbstract[m.Name] {
					seenAbstract[m.Name] = true
					abstractMethods = append(abstractMethods, abstractMethod{m, owner})
				}
			}
		}

		// Recurse to parents.
		for _, parent := range parentTypesForStub(sym, idx) {
			collect(parent.Sym, parent)
		}
	}

	for _, parent := range parentTypesForStub(classSym, idx) {
		collect(parent.Sym, parent)
	}

	var stubs []string
	for _, am := range abstractMethods {
		if !implemented[am.sym.Name] {
			stubs = append(stubs, generateMethodStubForOwner(am.sym, am.owner, idx))
		}
	}
	return stubs
}

// generateMethodStub generates a Java method stub from an index Symbol.
func generateMethodStub(sym index.Symbol) string {
	return generateMethodStubForOwner(sym, nil, nil)
}

func generateMethodStubForOwner(sym index.Symbol, ownerType *index.TypeExpr, idx *index.Index) string {
	returnType := methodStubReturnType(sym, ownerType, idx)
	params := methodStubParams(sym, ownerType, idx)
	var sb strings.Builder
	sb.WriteString("\n    @Override\n")
	sb.WriteString("    public ")
	sb.WriteString(returnType)
	sb.WriteString(" ")
	sb.WriteString(sym.Name)
	sb.WriteString("(")
	sb.WriteString(params)
	sb.WriteString(") {\n")
	sb.WriteString("        throw new UnsupportedOperationException(\"Not implemented\");\n")
	sb.WriteString("    }\n")

	return sb.String()
}

func parentTypesForStub(classSym string, idx *index.Index) []*index.TypeExpr {
	if pts := idx.ParentTypesOf(classSym); len(pts) > 0 {
		return pts
	}
	parentSyms := idx.ParentsOf(classSym)
	if len(parentSyms) == 0 {
		return nil
	}
	result := make([]*index.TypeExpr, 0, len(parentSyms))
	for _, sym := range parentSyms {
		result = append(result, &index.TypeExpr{Sym: sym})
	}
	return result
}

func methodStubReturnType(sym index.Symbol, ownerType *index.TypeExpr, idx *index.Index) string {
	if idx != nil {
		if te := idx.DeclTypeOf(sym.Symbol); te != nil {
			if ownerType != nil {
				te = substituteTypeParams(te, ownerType, idx)
				te = substituteNamedTypeParams(te, ownerType, idx)
			}
			if rendered := formatMethodStubType(te); rendered != "" {
				return rendered
			}
		}
	}
	if sym.Signature != nil && sym.Signature.ReturnTypeSym != "" {
		return formatMethodStubType(&index.TypeExpr{Sym: sym.Signature.ReturnTypeSym})
	}
	if sym.Signature != nil && sym.Signature.Label != "" {
		label := sym.Signature.Label
		if parenIdx := strings.Index(label, "("); parenIdx >= 0 {
			prefix := label[:parenIdx]
			if spaceIdx := strings.LastIndex(prefix, " "); spaceIdx >= 0 {
				return prefix[:spaceIdx]
			}
		}
	}
	return "void"
}

func methodStubParams(sym index.Symbol, ownerType *index.TypeExpr, idx *index.Index) string {
	if idx != nil && sym.Signature != nil {
		if pts := idx.DeclParamTypesOf(sym.Symbol); len(pts) > 0 && len(pts) == len(sym.Signature.Params) {
			var params []string
			for i, p := range sym.Signature.Params {
				te := pts[i]
				if ownerType != nil {
					te = substituteTypeParams(te, ownerType, idx)
					te = substituteNamedTypeParams(te, ownerType, idx)
				}
				typeName := formatMethodStubType(te)
				if typeName == "" {
					typeName = p.Type
				}
				if typeName == "" {
					typeName = "Object"
				}
				name := p.Name
				if name == "" {
					name = fmt.Sprintf("arg%d", i)
				}
				params = append(params, typeName+" "+name)
			}
			return strings.Join(params, ", ")
		}
	}
	if sym.Signature != nil {
		if len(sym.Signature.Params) > 0 {
			var params []string
			for i, p := range sym.Signature.Params {
				typeName := p.Type
				if typeName == "" && p.TypeSym != "" {
					typeName = formatMethodStubType(&index.TypeExpr{Sym: p.TypeSym})
				}
				if typeName == "" {
					typeName = "Object"
				}
				name := p.Name
				if name == "" {
					name = fmt.Sprintf("arg%d", i)
				}
				params = append(params, typeName+" "+name)
			}
			return strings.Join(params, ", ")
		}
		if sym.Signature.Label != "" {
			label := sym.Signature.Label
			if parenIdx := strings.Index(label, "("); parenIdx >= 0 {
				if closeIdx := strings.LastIndex(label, ")"); closeIdx > parenIdx+1 {
					return label[parenIdx+1 : closeIdx]
				}
			}
		}
	}
	return ""
}

func formatMethodStubType(te *index.TypeExpr) string {
	if te == nil {
		return ""
	}
	name := simpleMethodStubTypeName(te.Sym)
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
		sb.WriteString(formatMethodStubType(arg))
	}
	sb.WriteByte('>')
	return sb.String()
}

func simpleMethodStubTypeName(sym string) string {
	switch sym {
	case "scala/Int#", "int":
		return "int"
	case "scala/Long#", "long":
		return "long"
	case "scala/Short#", "short":
		return "short"
	case "scala/Byte#", "byte":
		return "byte"
	case "scala/Float#", "float":
		return "float"
	case "scala/Double#", "double":
		return "double"
	case "scala/Boolean#", "boolean":
		return "boolean"
	case "scala/Char#", "char":
		return "char"
	case "scala/Unit#", "void":
		return "void"
	}

	if start := strings.LastIndexByte(sym, '['); start >= 0 {
		if end := strings.LastIndexByte(sym, ']'); end > start+1 {
			return sym[start+1 : end]
		}
	}
	sym = strings.TrimSuffix(sym, "#")
	sym = strings.TrimSuffix(sym, ".")
	if idx := strings.LastIndexAny(sym, "/."); idx >= 0 {
		return sym[idx+1:]
	}
	return sym
}

// findClassInsertionPoint locates the line of the closing brace of the class
// body that contains diagLine, using tree-sitter AST.
func findClassInsertionPoint(root *slog.Node, content []byte, diagLine int) int {
	node := nodeAtPosition(root, diagLine, 0)
	if node == nil {
		return -1
	}

	// Walk up to find the enclosing class/interface/enum declaration.
	classNode := findAncestor(node, "class_declaration", "interface_declaration", "enum_declaration")
	if classNode == nil {
		// The node itself might be the declaration.
		if t := node.Type(); t == "class_declaration" || t == "interface_declaration" || t == "enum_declaration" {
			classNode = node
		} else {
			return -1
		}
	}

	body := findChildByType(classNode, "class_body")
	if body == nil {
		body = findChildByType(classNode, "interface_body")
	}
	if body == nil {
		body = findChildByType(classNode, "enum_body")
	}
	if body == nil {
		return -1
	}

	// The closing brace is the last line of the body.
	return int(body.EndPoint().Row)
}

// overrideMethodAction returns a single "Override method..." CodeAction with a command
// if there are overridable methods in the parent classes. The command triggers a
// window/showMessageRequest to let the user pick which method to override.
func overrideMethodAction(fileURI string, idx *index.Index, overlay string, cursorLine int) *CodeAction {
	if !hasOverridableMethods(fileURI, idx, overlay, cursorLine) {
		return nil
	}

	return &CodeAction{
		Title: "Override method...",
		Kind:  "source",
		Command: &Command{
			Title:   "Override method...",
			Command: "decaf.overrideMethod",
			Arguments: []any{
				fileURI,
				cursorLine,
			},
		},
	}
}

// hasOverridableMethods checks whether there are any overridable methods for the
// class at the given cursor line.
func hasOverridableMethods(fileURI string, idx *index.Index, overlay string, cursorLine int) bool {
	content := readContent(fileURI, overlay)
	if content == nil {
		return false
	}

	tree, err := getTree(content)
	if err != nil {
		return false
	}
	root := tree.RootNode()

	className, classNode := findClassContext(root, content, cursorLine)
	if className == "" || classNode == nil {
		return false
	}

	var classSym index.Symbol
	found := false
	for _, sym := range idx.FileSymbols(fileURI) {
		if sym.Name == className && sym.Kind == sdb.SymbolInformation_CLASS {
			classSym = sym
			found = true
			break
		}
	}
	if !found {
		return false
	}

	ownMethods := make(map[string]bool)
	for _, m := range idx.DirectMembersOfType(classSym.Symbol) {
		if m.Kind == sdb.SymbolInformation_METHOD {
			ownMethods[m.Name] = true
		}
	}

	for _, parentType := range parentTypesForStub(classSym.Symbol, idx) {
		if parentType.Sym == "java/lang/Object#" {
			continue
		}
		for _, m := range idx.DirectMembersOfType(parentType.Sym) {
			if m.Kind != sdb.SymbolInformation_METHOD || m.Kind == sdb.SymbolInformation_CONSTRUCTOR {
				continue
			}
			if m.IsAbstract || m.IsStatic {
				continue
			}
			if !ownMethods[m.Name] {
				return true
			}
		}
	}
	return false
}

// collectOverridableMethods returns the list of overridable methods and the
// insertion line for the class at the given cursor line.
type overridableMethod struct {
	method     index.Symbol
	parentType *index.TypeExpr
}

func collectOverridableMethods(fileURI string, idx *index.Index, overlay string, cursorLine int) (methods []overridableMethod, insertLine int) {
	content := readContent(fileURI, overlay)
	if content == nil {
		return nil, -1
	}

	tree, err := getTree(content)
	if err != nil {
		return nil, -1
	}
	root := tree.RootNode()

	className, classNode := findClassContext(root, content, cursorLine)
	if className == "" || classNode == nil {
		return nil, -1
	}

	var classSym index.Symbol
	found := false
	for _, sym := range idx.FileSymbols(fileURI) {
		if sym.Name == className && sym.Kind == sdb.SymbolInformation_CLASS {
			classSym = sym
			found = true
			break
		}
	}
	if !found {
		return nil, -1
	}

	ownMethods := make(map[string]bool)
	for _, m := range idx.DirectMembersOfType(classSym.Symbol) {
		if m.Kind == sdb.SymbolInformation_METHOD {
			ownMethods[m.Name] = true
		}
	}

	insertLine = findClassInsertionPoint(root, content, cursorLine)
	if insertLine < 0 {
		return nil, -1
	}

	seen := make(map[string]bool)
	for _, parentType := range parentTypesForStub(classSym.Symbol, idx) {
		if parentType.Sym == "java/lang/Object#" {
			continue
		}
		for _, m := range idx.DirectMembersOfType(parentType.Sym) {
			if m.Kind == sdb.SymbolInformation_CONSTRUCTOR {
				continue
			}
			if m.Kind != sdb.SymbolInformation_METHOD {
				continue
			}
			if m.IsAbstract || m.IsStatic {
				continue
			}
			if ownMethods[m.Name] {
				continue
			}
			if seen[m.Name] {
				continue
			}
			seen[m.Name] = true
			methods = append(methods, overridableMethod{method: m, parentType: parentType})
		}
	}
	return methods, insertLine
}

// readContent returns file content from overlay or disk.
func readContent(fileURI string, overlay string) []byte {
	if overlay != "" {
		return []byte(overlay)
	}
	content, err := os.ReadFile(uri.ToPath(fileURI))
	if err != nil {
		return nil
	}
	return content
}
