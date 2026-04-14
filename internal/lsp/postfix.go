package lsp

import (
	"fmt"
	"strings"
)

// postfixTemplate defines a postfix completion template.
type postfixTemplate struct {
	label  string // trigger name after the dot (e.g. "var", "if", "not")
	detail string // description shown in the completion menu
	// buildText returns the replacement text given the original expression.
	// The returned string uses snippet syntax (tabstops).
	buildText func(expr string) string
	scope     CompletionScope // ScopeBlock means only in method bodies
}

var postfixTemplates = []postfixTemplate{
	{
		label:  "var",
		detail: "Introduce local variable",
		buildText: func(expr string) string {
			return fmt.Sprintf("var ${1:name} = %s;$0", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "not",
		detail: "Negate boolean expression",
		buildText: func(expr string) string {
			if needsParens(expr) {
				return fmt.Sprintf("!(%s)$0", expr)
			}
			return fmt.Sprintf("!%s$0", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "if",
		detail: "if statement",
		buildText: func(expr string) string {
			return fmt.Sprintf("if (%s) {\n    $0\n}", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "else",
		detail: "if-else statement",
		buildText: func(expr string) string {
			return fmt.Sprintf("if (%s) {\n    $1\n} else {\n    $0\n}", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "null",
		detail: "if (expr == null)",
		buildText: func(expr string) string {
			return fmt.Sprintf("if (%s == null) {\n    $0\n}", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "notnull",
		detail: "if (expr != null)",
		buildText: func(expr string) string {
			return fmt.Sprintf("if (%s != null) {\n    $0\n}", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "nn",
		detail: "if (expr != null)",
		buildText: func(expr string) string {
			return fmt.Sprintf("if (%s != null) {\n    $0\n}", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "for",
		detail: "for-each loop",
		buildText: func(expr string) string {
			return fmt.Sprintf("for (var ${1:item} : %s) {\n    $0\n}", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "fori",
		detail: "for loop with index",
		buildText: func(expr string) string {
			return fmt.Sprintf("for (int ${1:i} = 0; ${1:i} < %s; ${1:i}++) {\n    $0\n}", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "sout",
		detail: "System.out.println(expr)",
		buildText: func(expr string) string {
			return fmt.Sprintf("System.out.println(%s);$0", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "soutv",
		detail: `System.out.println("expr = " + expr)`,
		buildText: func(expr string) string {
			return fmt.Sprintf("System.out.println(\"%s = \" + %s);$0", expr, expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "serr",
		detail: "System.err.println(expr)",
		buildText: func(expr string) string {
			return fmt.Sprintf("System.err.println(%s);$0", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "return",
		detail: "return expr",
		buildText: func(expr string) string {
			return fmt.Sprintf("return %s;$0", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "throw",
		detail: "throw expr",
		buildText: func(expr string) string {
			return fmt.Sprintf("throw %s;$0", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "while",
		detail: "while loop",
		buildText: func(expr string) string {
			return fmt.Sprintf("while (%s) {\n    $0\n}", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "synchronized",
		detail: "synchronized block",
		buildText: func(expr string) string {
			return fmt.Sprintf("synchronized (%s) {\n    $0\n}", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "cast",
		detail: "Cast expression",
		buildText: func(expr string) string {
			return fmt.Sprintf("((${1:Type}) %s)$0", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "try",
		detail: "try-catch block",
		buildText: func(expr string) string {
			return fmt.Sprintf("try {\n    %s;\n} catch (${1:Exception} ${2:e}) {\n    $0\n}", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "assert",
		detail: "assert expr",
		buildText: func(expr string) string {
			return fmt.Sprintf("assert %s;$0", expr)
		},
		scope: ScopeBlock,
	},
	{
		label:  "opt",
		detail: "Optional.ofNullable(expr)",
		buildText: func(expr string) string {
			return fmt.Sprintf("Optional.ofNullable(%s)$0", expr)
		},
		scope: ScopeBlock,
	},
}

// needsParens returns true if the expression needs parentheses when negated.
func needsParens(expr string) bool {
	// Simple identifiers, method calls, and field accesses don't need parens.
	// Expressions with operators or spaces do.
	for _, c := range expr {
		switch c {
		case ' ', '+', '-', '*', '/', '%', '&', '|', '^', '<', '>', '=', '!', '?':
			return true
		}
	}
	return false
}

// completePostfix generates postfix completion items for dot-triggered completion.
// It uses the original content (not parseContent) to compute correct byte positions
// for TextEdit ranges.
func completePostfix(cctx *CompletionCtx, content []byte) []CompletionItem {
	if cctx.Kind != CompletionDot || cctx.ReceiverExpr == "" {
		return nil
	}
	if cctx.Scope != ScopeBlock {
		return nil
	}

	prefix := strings.ToLower(cctx.Prefix)
	expr := cctx.ReceiverExpr

	// Compute the range from receiver start to cursor (end of prefix).
	// This is the range that the postfix TextEdit will replace.
	startLine, startCol := byteOffsetToPosition(content, cctx.ReceiverStart)
	cursorOffset := cctx.DotOffset + 1 + len(cctx.Prefix) // dot + prefix
	endLine, endCol := byteOffsetToPosition(content, cursorOffset)

	var items []CompletionItem
	for _, tmpl := range postfixTemplates {
		if tmpl.scope != ScopeUnknown && tmpl.scope != cctx.Scope {
			continue
		}
		if prefix != "" && !strings.HasPrefix(tmpl.label, prefix) {
			continue
		}

		newText := tmpl.buildText(expr)
		items = append(items, CompletionItem{
			Label:            expr + "." + tmpl.label,
			Kind:             CompletionKindSnippet,
			Detail:           tmpl.detail,
			FilterText:       tmpl.label,
			SortText:         "1" + "3" + "0" + "12" + completionNameSortKey(tmpl.label),
			InsertTextFormat: InsertTextFormatSnippet,
			TextEdit: &TextEdit{
				Range: Range{
					Start: Position{Line: startLine, Character: startCol},
					End:   Position{Line: endLine, Character: endCol},
				},
				NewText: newText,
			},
		})
	}
	return items
}
