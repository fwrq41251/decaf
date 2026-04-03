package lsp

import "strings"

type javaSnippet struct {
	label      string
	detail     string
	insertText string
	scope      CompletionScope
}

var javaSnippets = []javaSnippet{
	// Block scope snippets (inside methods/blocks)
	{
		label:      "sout",
		detail:     "System.out.println()",
		insertText: "System.out.println($1);$0",
		scope:      ScopeBlock,
	},
	{
		label:      "serr",
		detail:     "System.err.println()",
		insertText: "System.err.println($1);$0",
		scope:      ScopeBlock,
	},
	{
		label:      "fori",
		detail:     "for loop",
		insertText: "for (int ${1:i} = 0; ${1:i} < ${2:max}; ${1:i}++) {\n    $0\n}",
		scope:      ScopeBlock,
	},
	{
		label:      "foreach",
		detail:     "enhanced for loop",
		insertText: "for (${1:Object} ${2:item} : ${3:collection}) {\n    $0\n}",
		scope:      ScopeBlock,
	},
	{
		label:      "ifn",
		detail:     "if null",
		insertText: "if (${1:var} == null) {\n    $0\n}",
		scope:      ScopeBlock,
	},
	{
		label:      "inn",
		detail:     "if not null",
		insertText: "if (${1:var} != null) {\n    $0\n}",
		scope:      ScopeBlock,
	},
	{
		label:      "printstack",
		detail:     "printStackTrace()",
		insertText: "e.printStackTrace();$0",
		scope:      ScopeBlock,
	},

	// Class scope snippets (inside class body)
	{
		label:      "main",
		detail:     "public static void main(String[] args)",
		insertText: "public static void main(String[] args) {\n    $0\n}",
		scope:      ScopeClass,
	},
	{
		label:      "psvm",
		detail:     "public static void main(String[] args)",
		insertText: "public static void main(String[] args) {\n    $0\n}",
		scope:      ScopeClass,
	},
	{
		label:      "test",
		detail:     "JUnit test method",
		insertText: "@Test\nvoid ${1:testName}() {\n    $0\n}",
		scope:      ScopeClass,
	},
}

func (h *Handler) completeSnippets(cctx *CompletionCtx) []CompletionItem {
	var items []CompletionItem
	for _, s := range javaSnippets {
		// Only show snippets if they match the scope AND the prefix (if any).
		if s.scope == cctx.Scope {
			if cctx.Prefix == "" || strings.HasPrefix(s.label, cctx.Prefix) {
				items = append(items, CompletionItem{
					Label:            s.label,
					Detail:           s.detail,
					Kind:             CompletionKindSnippet,
					InsertText:       s.insertText,
					InsertTextFormat: InsertTextFormatSnippet,
					// Give snippets high priority so they appear at the top
					// of the completion list when the prefix matches.
					SortText: "000_" + s.label,
				})
			}
		}
	}
	return items
}
