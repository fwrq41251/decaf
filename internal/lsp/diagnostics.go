package lsp

import (
	"context"
	"fmt"
	"strings"

	slog "github.com/smacker/go-tree-sitter"
	"github.com/tree-sitter/tree-sitter-java/bindings/go"
)

// DiagnosticSeverity constants (LSP spec).
const (
	SeverityError       = 1
	SeverityWarning     = 2
	SeverityInformation = 3
	SeverityHint        = 4
)

var javaLanguage = slog.NewLanguage(tree_sitter_java.Language())

// computeDiagnostics parses content with tree-sitter and returns syntax error diagnostics.
func computeDiagnostics(content string) []Diagnostic {
	parser := slog.NewParser()
	defer parser.Close()
	parser.SetLanguage(javaLanguage)

	tree, err := parser.ParseCtx(context.Background(), nil, []byte(content))
	if err != nil {
		return nil
	}
	defer tree.Close()

	var diags []Diagnostic
	collectErrors(tree.RootNode(), []byte(content), &diags)
	return diags
}

// collectErrors walks the AST and collects ERROR/MISSING nodes as diagnostics.
func collectErrors(n *slog.Node, content []byte, diags *[]Diagnostic) {
	nodeType := n.Type()

	if nodeType == "ERROR" {
		start := n.StartPoint()
		end := n.EndPoint()
		text := n.Content(content)
		msg := formatErrorMessage(text)
		*diags = append(*diags, Diagnostic{
			Range: Range{
				Start: Position{Line: int(start.Row), Character: int(start.Column)},
				End:   Position{Line: int(end.Row), Character: int(end.Column)},
			},
			Severity: SeverityError,
			Message:  msg,
			Source:   "decaf",
		})
		return
	}

	if n.IsMissing() {
		start := n.StartPoint()
		*diags = append(*diags, Diagnostic{
			Range: Range{
				Start: Position{Line: int(start.Row), Character: int(start.Column)},
				End:   Position{Line: int(start.Row), Character: int(start.Column)},
			},
			Severity: SeverityError,
			Message:  fmt.Sprintf("missing '%s'", nodeType),
			Source:   "decaf",
		})
		return
	}

	for i := 0; i < int(n.ChildCount()); i++ {
		collectErrors(n.Child(i), content, diags)
	}
}

// formatErrorMessage creates a human-readable message from an ERROR node's content.
func formatErrorMessage(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "syntax error"
	}
	if len(text) > 40 {
		text = text[:40] + "…"
	}
	return fmt.Sprintf("syntax error near '%s'", text)
}
