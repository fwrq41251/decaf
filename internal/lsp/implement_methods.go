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

	var content []byte
	if overlay != "" {
		content = []byte(overlay)
	} else {
		filePath := uri.ToPath(fileURI)
		var err error
		content, err = os.ReadFile(filePath)
		if err != nil {
			return nil
		}
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

	// Collect methods the class already implements.
	ownMethods := make(map[string]bool)
	for _, m := range idx.DirectMembersOfType(classSym.Symbol) {
		if m.Kind == sdb.SymbolInformation_METHOD {
			ownMethods[m.Name] = true
		}
	}

	// Collect unimplemented abstract methods from all parents.
	var stubs []string
	seen := make(map[string]bool)
	for _, parentSym := range idx.ParentsOf(classSym.Symbol) {
		for _, m := range idx.DirectMembersOfType(parentSym) {
			if m.Kind == sdb.SymbolInformation_CONSTRUCTOR {
				continue
			}
			if m.Kind != sdb.SymbolInformation_METHOD || !m.IsAbstract {
				continue
			}
			if ownMethods[m.Name] {
				continue
			}
			if seen[m.Name] {
				continue
			}
			seen[m.Name] = true
			stubs = append(stubs, generateMethodStub(m))
		}
	}

	if len(stubs) == 0 {
		return nil
	}

	newText := strings.Join(stubs, "")

	editRange := Range{
		Start: Position{Line: insertLine, Character: 0},
		End:   Position{Line: insertLine, Character: 0},
	}

	return &WorkspaceEdit{
		Changes: map[string][]TextEdit{
			fileURI: {{Range: editRange, NewText: newText}},
		},
	}
}

// generateMethodStub generates a Java method stub from an index Symbol.
func generateMethodStub(sym index.Symbol) string {
	var returnType, params string

	if sym.Signature != nil && sym.Signature.Label != "" {
		label := sym.Signature.Label
		// Label format: "ReturnType methodName(params)"
		parenIdx := strings.Index(label, "(")
		if parenIdx >= 0 {
			prefix := label[:parenIdx]
			// prefix is "ReturnType methodName" — extract return type
			spaceIdx := strings.LastIndex(prefix, " ")
			if spaceIdx >= 0 {
				returnType = prefix[:spaceIdx]
			}
			closeIdx := strings.LastIndex(label, ")")
			if closeIdx > parenIdx+1 {
				params = label[parenIdx+1 : closeIdx]
			}
		}
	}

	if returnType == "" {
		returnType = "void"
	}

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

// overrideMethodActions generates one CodeAction per overridable parent method
// for the class at the given cursor line.
func overrideMethodActions(fileURI string, idx *index.Index, overlay string, cursorLine int) []CodeAction {
	var content []byte
	if overlay != "" {
		content = []byte(overlay)
	} else {
		var err error
		content, err = os.ReadFile(uri.ToPath(fileURI))
		if err != nil {
			return nil
		}
	}

	tree, err := getTree(content)
	if err != nil {
		return nil
	}
	root := tree.RootNode()

	className, classNode := findClassContext(root, content, cursorLine)
	if className == "" || classNode == nil {
		return nil
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
		return nil
	}

	ownMethods := make(map[string]bool)
	for _, m := range idx.DirectMembersOfType(classSym.Symbol) {
		if m.Kind == sdb.SymbolInformation_METHOD {
			ownMethods[m.Name] = true
		}
	}

	insertLine := findClassInsertionPoint(root, content, cursorLine)
	if insertLine < 0 {
		return nil
	}

	var actions []CodeAction
	seen := make(map[string]bool)
	for _, parentSym := range idx.ParentsOf(classSym.Symbol) {
		if parentSym == "java/lang/Object#" {
			continue
		}
		for _, m := range idx.DirectMembersOfType(parentSym) {
			if m.Kind == sdb.SymbolInformation_CONSTRUCTOR {
				continue
			}
			if m.Kind != sdb.SymbolInformation_METHOD {
				continue
			}
			if m.IsAbstract {
				continue
			}
			if m.IsStatic {
				continue
			}
			if ownMethods[m.Name] {
				continue
			}
			if seen[m.Name] {
				continue
			}
			seen[m.Name] = true

			editRange := Range{
				Start: Position{Line: insertLine, Character: 0},
				End:   Position{Line: insertLine, Character: 0},
			}
			actions = append(actions, CodeAction{
				Title: fmt.Sprintf("Override '%s'", m.Name),
				Kind:  "source",
				Edit: &WorkspaceEdit{
					Changes: map[string][]TextEdit{
						fileURI: {{Range: editRange, NewText: generateMethodStub(m)}},
					},
				},
			})
		}
	}

	return actions
}
