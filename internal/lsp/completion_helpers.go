package lsp

import (
	"fmt"

	"github.com/fwrq41251/decaf/internal/index"
)

func completionMatchScorePrefix(name string, lowerQuery string) string {
	return fmt.Sprintf("%d", index.CompletionMatchScoreLower(name, lowerQuery))
}

func cloneCompletionCtxWithReceiver(cctx *CompletionCtx, receiver string) *CompletionCtx {
	return &CompletionCtx{
		Receiver:       receiver,
		Locals:         cctx.Locals,
		LambdaParams:   cctx.LambdaParams,
		Params:         cctx.Params,
		ClassFields:    cctx.ClassFields,
		ClassMethods:   cctx.ClassMethods,
		Imports:        cctx.Imports,
		Package:        cctx.Package,
		EnclosingClass: cctx.EnclosingClass,
	}
}
