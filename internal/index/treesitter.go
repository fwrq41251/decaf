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

	isConstructor := strings.Contains(sym, "#`<init>`(")

	// Extract the short name from the symbol.
	name := ExtractShortName(sym)
	if name == "" {
		return -1, -1
	}

	rootNode := tree.RootNode()

	// We use a simple recursive search for a node that looks like a declaration of 'name'.
	return findNode(rootNode, name, content, isConstructor)
	}

	func ExtractShortName(sym string) string {

	// 1. Remove trailing SemanticDB markers.
	sym = strings.TrimRight(sym, "#.:().")

	// 2. Handle method overloads and special names like <init> (constructor).
	if idx := strings.Index(sym, "("); idx != -1 {
		sym = sym[:idx]
	}

	// 3. Take the last part.
	idx := strings.LastIndexAny(sym, "#/")
	if idx == -1 {
		return sym
	}
	name := sym[idx+1:]

	// Handle constructor: com/example/Test#`<init>`() -> Test
	if name == "`<init>`" {
		// Look at the part before #.
		parentPart := sym[:idx]
		if lastSlash := strings.LastIndexAny(parentPart, "#/"); lastSlash != -1 {
			return parentPart[lastSlash+1:]
		}
		return parentPart
	}

	return name
}

func findNode(n *slog.Node, name string, content []byte, isConstructor bool) (int, int) {
	// Check if this node is a declaration of 'name'.
	nodeType := n.Type()
	
	// If we are looking for a constructor, only match constructor_declaration.
	// If not, avoid constructor_declaration to prevent class vs constructor confusion.
	match := false
	if isConstructor {
		match = nodeType == "constructor_declaration"
	} else {
		match = nodeType == "class_declaration" || nodeType == "interface_declaration" || 
		        nodeType == "enum_declaration" || nodeType == "method_declaration" || 
				nodeType == "field_declaration" || nodeType == "variable_declarator"
	}

	if match {
		for i := 0; i < int(n.NamedChildCount()); i++ {
			child := n.NamedChild(i)
			if child.Type() == "identifier" {
				if child.Content(content) == name {
					start := child.StartPoint()
					return int(start.Row), int(start.Column)
				}
			}
		}
	}

	// Recurse.
	for i := 0; i < int(n.NamedChildCount()); i++ {
		row, col := findNode(n.NamedChild(i), name, content, isConstructor)
		if row != -1 {
			return row, col
		}
	}

	return -1, -1
}
