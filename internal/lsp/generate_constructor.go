package lsp

import (
	"os"
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"github.com/fwrq41251/decaf/internal/uri"
	slog "github.com/smacker/go-tree-sitter"
)

// findClassContext finds the class declaration containing the given line
// and returns the class name and the class AST node.
func findClassContext(root *slog.Node, content []byte, line int) (string, *slog.Node) {
	node := nodeAtPosition(root, line, 0)
	if node == nil {
		return "", nil
	}

	classNode := findAncestor(node, "class_declaration")
	if classNode == nil {
		if node.Type() == "class_declaration" {
			classNode = node
		} else {
			return "", nil
		}
	}

	for i := 0; i < int(classNode.NamedChildCount()); i++ {
		child := classNode.NamedChild(i)
		if child.Type() == "identifier" {
			return child.Content(content), classNode
		}
	}
	return "", nil
}

// generateConstructorEdit computes a WorkspaceEdit that inserts a constructor
// for the class at cursorLine, taking all non-static fields as parameters.
func generateConstructorEdit(fileURI string, idx *index.Index, overlay string, cursorLine int) *WorkspaceEdit {
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

	className, classNode := findClassContext(root, content, cursorLine)
	if className == "" || classNode == nil {
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

	// Check if the class already has a constructor.
	members := idx.DirectMembersOfType(classSym.Symbol)
	for _, m := range members {
		if m.Kind == sdb.SymbolInformation_CONSTRUCTOR {
			return nil
		}
	}

	// Collect non-static fields.
	var fields []index.Symbol
	for _, m := range members {
		if m.Kind == sdb.SymbolInformation_FIELD && !m.IsStatic {
			fields = append(fields, m)
		}
	}

	newText := formatConstructor(className, fields)

	insertLine := findConstructorInsertPoint(classNode, content)
	if insertLine < 0 {
		return nil
	}

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

// findConstructorInsertPoint returns the line where the constructor should be
// inserted — after the last field declaration, or after the opening brace.
func findConstructorInsertPoint(classNode *slog.Node, content []byte) int {
	body := findChildByType(classNode, "class_body")
	if body == nil {
		return -1
	}

	lastFieldEnd := -1
	for i := 0; i < int(body.NamedChildCount()); i++ {
		child := body.NamedChild(i)
		if child.Type() == "field_declaration" {
			lastFieldEnd = int(child.EndPoint().Row) + 1
		}
	}

	if lastFieldEnd >= 0 {
		return lastFieldEnd
	}
	return int(body.StartPoint().Row) + 1
}

// formatConstructor generates a Java constructor taking the given fields as parameters.
func formatConstructor(className string, fields []index.Symbol) string {
	if len(fields) == 0 {
		return "\n    public " + className + "() {\n    }\n"
	}

	var params []string
	var assignments []string
	for _, f := range fields {
		typeName := "Object"
		if f.Signature != nil && f.Signature.Label != "" {
			if idx := strings.Index(f.Signature.Label, ": "); idx >= 0 {
				typeName = f.Signature.Label[idx+2:]
			}
		}
		params = append(params, typeName+" "+f.Name)
		assignments = append(assignments, "        this."+f.Name+" = "+f.Name+";\n")
	}

	var sb strings.Builder
	sb.WriteString("\n    public ")
	sb.WriteString(className)
	sb.WriteString("(")
	sb.WriteString(strings.Join(params, ", "))
	sb.WriteString(") {\n")
	for _, a := range assignments {
		sb.WriteString(a)
	}
	sb.WriteString("    }\n")

	return sb.String()
}
