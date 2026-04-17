package lsp

import (
	"context"
	"encoding/json"

	"github.com/fwrq41251/decaf/internal/index"
)

var javaKeywords = []string{
	"abstract", "assert", "boolean", "break", "byte", "case", "catch", "char", "class", "const",
	"continue", "default", "do", "double", "else", "enum", "extends", "final", "finally", "float",
	"for", "goto", "if", "implements", "import", "instanceof", "int", "interface", "long", "native",
	"new", "package", "private", "protected", "public", "return", "short", "static", "strictfp",
	"super", "switch", "synchronized", "this", "throw", "throws", "transient", "try", "void",
	"volatile", "while",
}

var javaLiterals = []string{
	"true", "false", "null",
}

func (h *Handler) handleCompletion(ctx context.Context, params json.RawMessage) (any, error) {
	var p CompletionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return CompletionList{}, nil
	}

	content := h.getFileContent(p.TextDocument.URI)
	if content == "" {
		return CompletionList{}, nil
	}

	cctx := parseCompletionCtxWithContext(ctx, h.logger, []byte(content), p.Position.Line, p.Position.Character)

	var items []CompletionItem
	contentBytes := []byte(content)
	if cctx.Kind == CompletionDot {
		items = h.completeDot(cctx, p.TextDocument.URI)
		items = append(items, completePostfix(cctx, contentBytes)...)
	} else {
		items = h.completeLexical(cctx, p.TextDocument.URI, contentBytes)
	}
	sortCompletionItems(items)

	h.logger.Printf("completion at %s:%d:%d kind=%d prefix=%q receiver=%q imports=%d staticImports=%d -> %d items",
		p.TextDocument.URI, p.Position.Line, p.Position.Character,
		cctx.Kind, cctx.Prefix, cctx.Receiver, len(cctx.Imports), countStaticImports(cctx.Imports), len(items))
	return CompletionList{IsIncomplete: len(items) >= 100, Items: items}, nil
}

func completionTypeMatchPrefix(h *Handler, expectedType, candidateType *index.TypeExpr) string {
	if candidateType == nil || expectedType == nil {
		return "2"
	}
	if sameTypeExpr(candidateType, expectedType) {
		return "0"
	}
	if h.idx.IsAssignableTo(candidateType.Sym, expectedType.Sym) {
		return "1"
	}
	return "2"
}
