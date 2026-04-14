package lsp

import (
	"context"
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	slog "github.com/smacker/go-tree-sitter"
)

// ImportSpec represents a single import statement.
type ImportSpec struct {
	Path     string // e.g. "java.util.List" or "java.util.*"
	Static   bool
	Wildcard bool
}

func getTree(content []byte) (*slog.Tree, error) {
	return getTreeWithContext(context.Background(), content)
}

func getTreeWithContext(ctx context.Context, content []byte) (*slog.Tree, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return index.ParseJava(ctx, content)
}

// nodeAtPosition returns the deepest node containing the given position.
func nodeAtPosition(node *slog.Node, line, character int) *slog.Node {
	if node == nil {
		return nil
	}

	start := node.StartPoint()
	end := node.EndPoint()

	// Check if position is within this node.
	if !pointInRange(uint32(line), uint32(character), start, end) {
		return nil
	}

	// Try to find a deeper child that contains the position.
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		found := nodeAtPosition(child, line, character)
		if found != nil {
			return found
		}
	}

	return node
}

// parseImport extracts import details from an import_declaration node.
func parseImport(node *slog.Node, content []byte) ImportSpec {
	spec := ImportSpec{}
	text := node.Content(content)

	spec.Static = strings.Contains(text, "static ")

	// Check for wildcard.
	if strings.HasSuffix(strings.TrimRight(text, "; \t\n"), ".*") {
		spec.Wildcard = true
	}

	// Extract the path from children.
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch child.Type() {
		case "scoped_identifier", "identifier":
			spec.Path = child.Content(content)
		case "asterisk":
			spec.Wildcard = true
			// Append .* to path if not already there.
			if spec.Path != "" && !strings.HasSuffix(spec.Path, ".*") {
				spec.Path += ".*"
			}
		}
	}

	return spec
}

func pointInRange(row, col uint32, start, end slog.Point) bool {
	if row < start.Row || row > end.Row {
		return false
	}
	if row == start.Row && col < start.Column {
		return false
	}
	if row == end.Row && col > end.Column {
		return false
	}
	return true
}

// findAncestor walks up the tree from node to find the first ancestor with one of the given types.
func findAncestor(node *slog.Node, types ...string) *slog.Node {
	if node == nil {
		return nil
	}
	for n := node.Parent(); n != nil; n = n.Parent() {
		for _, t := range types {
			if n.Type() == t {
				return n
			}
		}
	}
	return nil
}
