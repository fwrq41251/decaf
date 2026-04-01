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

	// Determine the expected symbol kind from the SemanticDB symbol string.
	symKind := classifySymbol(sym)

	// Extract the short name from the symbol.
	name := ExtractShortName(sym)
	if name == "" {
		return -1, -1
	}

	rootNode := tree.RootNode()
	row, col := findNode(rootNode, name, content, symKind)
	if row != -1 {
		return row, col
	}

	// Fallback: simple text search if Tree-sitter fails to find a "declaration" node.
	// This can happen for some complex symbols or if our findNode is too strict.
	idx := strings.Index(string(content), name)
	if idx != -1 {
		// Calculate line/col from index.
		lines := strings.Split(string(content[:idx]), "\n")
		return len(lines) - 1, len(lines[len(lines)-1])
	}

	return -1, -1
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

// symbolKind classifies the expected declaration type for a SemanticDB symbol.
type symbolKind int

const (
	symbolKindType        symbolKind = iota // class, interface, enum
	symbolKindMethod                        // method
	symbolKindConstructor                   // constructor (<init>)
	symbolKindField                         // field, variable
	symbolKindUnknown                       // fallback: match any declaration
)

// classifySymbol determines the expected declaration kind from a SemanticDB symbol string.
// Examples:
//
//	"com/example/Foo#"             → symbolKindType      (trailing #)
//	"com/example/Foo#bar()."       → symbolKindMethod    (contains "()" before trailing .)
//	"com/example/Foo#`<init>`()."  → symbolKindConstructor
//	"com/example/Foo#baz."         → symbolKindField     (trailing . without "()")
func classifySymbol(sym string) symbolKind {
	if strings.Contains(sym, "#`<init>`(") {
		return symbolKindConstructor
	}
	if strings.HasSuffix(sym, "#") {
		return symbolKindType
	}
	// Method symbols contain parentheses before the trailing period.
	if strings.Contains(sym, "(") {
		return symbolKindMethod
	}
	if strings.HasSuffix(sym, ".") {
		return symbolKindField
	}
	return symbolKindUnknown
}

// nodeTypesForKind returns the set of Tree-sitter node types to match for a given symbol kind.
func nodeTypesForKind(kind symbolKind) []string {
	switch kind {
	case symbolKindType:
		return []string{"class_declaration", "interface_declaration", "enum_declaration"}
	case symbolKindMethod:
		return []string{"method_declaration"}
	case symbolKindConstructor:
		return []string{"constructor_declaration"}
	case symbolKindField:
		return []string{"field_declaration", "variable_declarator"}
	default:
		// Unknown: match any declaration type.
		return []string{"class_declaration", "interface_declaration", "enum_declaration",
			"method_declaration", "constructor_declaration",
			"field_declaration", "variable_declarator"}
	}
}

func findNode(n *slog.Node, name string, content []byte, kind symbolKind) (int, int) {
	nodeType := n.Type()

	allowed := nodeTypesForKind(kind)
	match := false
	for _, t := range allowed {
		if nodeType == t {
			match = true
			break
		}
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
		row, col := findNode(n.NamedChild(i), name, content, kind)
		if row != -1 {
			return row, col
		}
	}

	return -1, -1
}
