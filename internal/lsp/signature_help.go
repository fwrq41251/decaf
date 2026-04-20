package lsp

import (
	"context"
	"encoding/json"

	"github.com/fwrq41251/decaf/internal/index"
)

func (h *Handler) handleSignatureHelp(ctx context.Context, params json.RawMessage) (any, error) {
	var p SignatureHelpParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return nil, nil
	}

	syms := h.index().SymbolSignatures(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	if len(syms) == 0 {
		syms = h.resolveSignatureFromAST(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	}
	if len(syms) == 0 {
		return nil, nil
	}

	var sigs []SignatureInformation
	for i := range syms {
		si := formatSignatureHelp(&syms[i])
		if si != nil {
			sigs = append(sigs, *si)
		}
	}
	if len(sigs) == 0 {
		return nil, nil
	}

	activeParam := h.countActiveParameter(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	activeSig := 0
	for i, sig := range sigs {
		if len(sig.Parameters) >= activeParam+1 {
			activeSig = i
			break
		}
	}

	return SignatureHelp{
		Signatures:      sigs,
		ActiveSignature: activeSig,
		ActiveParameter: activeParam,
	}, nil
}

func (h *Handler) wordPrefixAt(fileURI string, line, character int) string {
	content := h.getFileContent(fileURI)
	if content == "" {
		return ""
	}

	contentBytes := []byte(content)
	byteOff := PositionToByteOffset(contentBytes, line, character)
	start := byteOff
	for start > 0 {
		ch := contentBytes[start-1]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '$' {
			start--
		} else {
			break
		}
	}
	return string(contentBytes[start:byteOff])
}

func (h *Handler) resolveSignatureFromAST(fileURI string, line, character int) []index.Symbol {
	content := h.getFileContent(fileURI)
	if content == "" {
		return nil
	}

	src := []byte(content)
	tree, err := getTree(src)
	if err != nil {
		return nil
	}

	node := nodeAtPosition(tree.RootNode(), line, character)
	if node == nil {
		return nil
	}

	callNode := node
	for callNode != nil {
		switch callNode.Type() {
		case "method_invocation", "object_creation_expression":
			goto found
		}
		callNode = callNode.Parent()
	}
	return nil

found:
	cctx := parseCompletionCtxWithContext(context.Background(), h.logger, src, line, character)
	resolver := &typeResolver{idx: h.index(), imports: cctx.Imports, pkg: cctx.Package}

	if callNode.Type() == "object_creation_expression" {
		te := extractTypeFromNewExpr(callNode, src)
		if te == nil {
			return nil
		}
		if sym := resolver.resolve(te.Sym); sym != "" {
			return h.findMembersByName(sym, te.Sym)
		}
		return nil
	}

	nameNode := callNode.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	methodName := nameNode.Content(src)

	objNode := callNode.ChildByFieldName("object")
	if objNode == nil {
		if cctx.EnclosingClass == "" {
			return nil
		}
		classSym := resolver.resolve(cctx.EnclosingClass)
		if classSym == "" {
			return nil
		}
		return h.findMembersByName(classSym, methodName)
	}

	recvText := exprToReceiver(objNode, src)
	if recvText == "" {
		return nil
	}
	cctx.Receiver = recvText
	typeExpr, _ := h.resolveReceiverTypeExpr(cctx, resolver)
	if typeExpr == nil {
		return nil
	}
	return h.findMembersByName(typeExpr.Sym, methodName)
}

func (h *Handler) findMembersByName(typeSym, name string) []index.Symbol {
	members := h.index().MembersOfType(typeSym)
	var result []index.Symbol
	for _, m := range members {
		if m.Name == name {
			result = append(result, m)
		}
	}
	return result
}

func (h *Handler) countActiveParameter(fileURI string, line, character int) int {
	content := h.getFileContent(fileURI)
	if content == "" {
		return 0
	}

	src := []byte(content)
	parser := index.AcquireJavaParser()
	defer index.ReleaseJavaParser(parser)

	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return 0
	}

	node := nodeAtPosition(tree.RootNode(), line, character)
	if node == nil {
		return 0
	}

	argList := node
	for argList != nil && argList.Type() != "argument_list" {
		argList = argList.Parent()
	}
	if argList == nil {
		return 0
	}

	cursorByte := PositionToByteOffset(src, line, character)
	active := 0
	for i := 0; i < int(argList.ChildCount()); i++ {
		child := argList.Child(i)
		if int(child.StartByte()) >= cursorByte {
			break
		}
		if child.Type() == "," {
			active++
		}
	}
	return active
}
