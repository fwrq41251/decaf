package index

import (
	"context"
	"os"
	"strings"
	"sync"

	slog "github.com/smacker/go-tree-sitter"
	"github.com/tree-sitter/tree-sitter-java/bindings/go"
)

var parserPool = sync.Pool{
	New: func() any {
		p := slog.NewParser()
		p.SetLanguage(slog.NewLanguage(tree_sitter_java.Language()))
		return p
	},
}

// FindSymbolLocation uses Tree-sitter to find the line/column of a symbol's definition in a Java file.
// sym: the SemanticDB symbol name, e.g., "java/lang/String#length()."
// filePath: the absolute path to the .java file.
func FindSymbolLocation(filePath, sym string) (int, int) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return -1, -1
	}

	parser := parserPool.Get().(*slog.Parser)
	defer parserPool.Put(parser)

	tree, err := parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return -1, -1
	}

	// Extract the short name from the symbol.
	// "java/lang/String#" -> "String"
	// "java/lang/String#length()." -> "length"
	name := extractShortName(sym)
	if name == "" {
		return -1, -1
	}

	rootNode := tree.RootNode()
	
	// We use a simple recursive search for a node that looks like a declaration of 'name'.
	return findNode(rootNode, name, content)
}

func extractShortName(sym string) string {
	// Remove trailing dots/parens: "length()." -> "length"
	sym = strings.TrimRight(sym, "().")
	
	// Split by # or / and take the last part.
	idx := strings.LastIndexAny(sym, "#/")
	if idx == -1 {
		return sym
	}
	return sym[idx+1:]
}

func findNode(n *slog.Node, name string, content []byte) (int, int) {
	// Check if this node is a declaration of 'name'.
	switch n.Type() {
	case "class_declaration", "interface_declaration", "enum_declaration", "method_declaration", "constructor_declaration", "field_declaration", "variable_declarator":
		// Find the 'identifier' child.
		for i := 0; i < int(n.NamedChildCount()); i++ {
			child := n.NamedChild(i)
			if child.Type() == "identifier" || child.Type() == "variable_declarator" {
				idNode := child
				if child.Type() == "variable_declarator" {
					for j := 0; j < int(child.NamedChildCount()); j++ {
						if gc := child.NamedChild(j); gc.Type() == "identifier" {
							idNode = gc
							break
						}
					}
				}

				if idNode.Content(content) == name {
					start := idNode.StartPoint()
					return int(start.Row), int(start.Column)
				}
			}
		}
	}

	// Recurse.
	for i := 0; i < int(n.NamedChildCount()); i++ {
		row, col := findNode(n.NamedChild(i), name, content)
		if row != -1 {
			return row, col
		}
	}

	return -1, -1
}
