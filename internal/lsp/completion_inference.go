package lsp

import (
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

func (h *Handler) resolveInferredLambdaParamType(paramIndex int, cctx *CompletionCtx, resolver *typeResolver) *index.TypeExpr {
	targetType := h.resolveCurrentArgumentTypeExpr(cctx, resolver)
	if targetType == nil {
		return nil
	}
	if te := lambdaParamTypeFromTargetType(targetType, paramIndex); te != nil {
		return te
	}
	return samParamTypeFromTargetType(targetType, paramIndex, h.idx, resolver)
}

func (h *Handler) resolveCurrentArgumentTypeExpr(cctx *CompletionCtx, resolver *typeResolver) *index.TypeExpr {
	if cctx.Call == nil {
		return nil
	}

	var ownerType *index.TypeExpr
	var syms []index.Symbol

	if cctx.Call.IsNewExpr {
		if sym := resolver.resolve(cctx.Call.Constructor); sym != "" {
			syms = h.findMembersByName(sym, cctx.Call.Constructor)
		}
	} else if cctx.Call.Receiver != "" {
		fakeCctx := cloneCompletionCtxWithReceiver(cctx, cctx.Call.Receiver)
		ownerType, _ = h.resolveReceiverTypeExpr(fakeCctx, resolver)
		if ownerType != nil {
			syms = h.findMembersByName(ownerType.Sym, cctx.Call.MethodName)
		}
	} else if cctx.EnclosingClass != "" {
		classSym := resolver.resolve(cctx.EnclosingClass)
		if classSym != "" {
			syms = h.findMembersByName(classSym, cctx.Call.MethodName)
		}
	}

	paramIdx := cctx.Call.ParamIndex
	best := h.selectBestMethodOverload(syms, paramIdx+1, nil, resolver)
	if best == nil {
		return nil
	}

	te := h.methodParamTypeExpr(*best, paramIdx, resolver)
	if te == nil {
		return nil
	}
	if ownerType != nil {
		te = substituteTypeParams(te, ownerType, h.idx)
		te = substituteNamedTypeParams(te, ownerType, h.idx)
	}
	return te
}

func lambdaParamTypeFromTargetType(targetType *index.TypeExpr, paramIndex int) *index.TypeExpr {
	if targetType == nil {
		return nil
	}

	switch simplifyTypeName(targetType.Sym) {
	case "Function", "Consumer", "Predicate", "UnaryOperator", "ToIntFunction", "ToLongFunction", "ToDoubleFunction":
		if paramIndex == 0 && len(targetType.Args) >= 1 {
			return targetType.Args[0]
		}
	case "BiFunction", "BiConsumer", "BiPredicate":
		if paramIndex < 2 && len(targetType.Args) >= paramIndex+1 {
			return targetType.Args[paramIndex]
		}
	case "BinaryOperator":
		if paramIndex < 2 && len(targetType.Args) >= 1 {
			return targetType.Args[0]
		}
	case "Comparator":
		if paramIndex < 2 && len(targetType.Args) >= 1 {
			return targetType.Args[0]
		}
	}

	return nil
}

var objectMethodNames = map[string]struct{}{
	"clone":     {},
	"equals":    {},
	"finalize":  {},
	"getClass":  {},
	"hashCode":  {},
	"notify":    {},
	"notifyAll": {},
	"toString":  {},
	"wait":      {},
}

func samParamTypeFromTargetType(targetType *index.TypeExpr, paramIndex int, idx *index.Index, resolver *typeResolver) *index.TypeExpr {
	if targetType == nil || idx == nil {
		return nil
	}

	var candidates []index.Symbol
	seen := make(map[string]struct{})
	for _, m := range idx.MembersOfType(targetType.Sym) {
		if m.Kind != sdb.SymbolInformation_METHOD || m.IsStatic {
			continue
		}
		if _, skip := objectMethodNames[m.Name]; skip {
			continue
		}
		key := completionItemKeyForSymbol(m)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		candidates = append(candidates, m)
	}

	if len(candidates) != 1 {
		return nil
	}

	sam := candidates[0]
	if sam.Signature == nil || paramIndex >= len(sam.Signature.Params) {
		return nil
	}
	param := sam.Signature.Params[paramIndex]
	if te := signatureParamTypeExpr(param.Type, resolver); te != nil {
		return substituteNamedTypeParams(te, targetType, idx)
	}
	if param.TypeSym != "" {
		return &index.TypeExpr{Sym: param.TypeSym}
	}
	return nil
}

func (h *Handler) resolveVarInitializer(vi *VarInitializer, cctx *CompletionCtx, resolver *typeResolver) *index.TypeExpr {
	ownerType, result := h.resolveVarInitializerMethodCall(vi, cctx, resolver)
	if result == nil {
		return nil
	}
	return h.refineVarInitializerType(result, ownerType, vi, resolver)
}

func (h *Handler) resolveVarInitializerMethodCall(vi *VarInitializer, cctx *CompletionCtx, resolver *typeResolver) (*index.TypeExpr, *index.TypeExpr) {
	if vi == nil {
		return nil, nil
	}
	if vi.Receiver == "" {
		return h.resolveUnqualifiedVarInitializerMethodCall(vi, cctx, resolver)
	}
	fakeCctx := cloneCompletionCtxWithReceiver(cctx, vi.Receiver)
	ownerType, _ := h.resolveReceiverTypeExpr(fakeCctx, resolver)
	if ownerType == nil {
		return nil, nil
	}
	return ownerType, h.resolveMemberTypeExpr(ownerType, vi.MethodName)
}

func (h *Handler) resolveUnqualifiedVarInitializerMethodCall(vi *VarInitializer, cctx *CompletionCtx, resolver *typeResolver) (*index.TypeExpr, *index.TypeExpr) {
	var ownerType *index.TypeExpr
	var candidates []index.Symbol

	if cctx.EnclosingClass != "" {
		if classSym := resolver.resolve(cctx.EnclosingClass); classSym != "" {
			ownerType = &index.TypeExpr{Sym: classSym}
			candidates = append(candidates, h.findMembersByName(classSym, vi.MethodName)...)
		}
	}
	for _, imp := range cctx.Imports {
		if !imp.Static {
			continue
		}
		if imp.Wildcard {
			classFQN := strings.TrimSuffix(imp.Path, ".*")
			classSym := resolver.resolve(simpleNameFromFQN(classFQN))
			if classSym == "" {
				classSym = fqnToSymbol(classFQN)
			}
			candidates = append(candidates, h.findMembersByName(classSym, vi.MethodName)...)
			continue
		}
		lastDot := strings.LastIndex(imp.Path, ".")
		if lastDot < 0 || imp.Path[lastDot+1:] != vi.MethodName {
			continue
		}
		classFQN := imp.Path[:lastDot]
		classSym := resolver.resolve(simpleNameFromFQN(classFQN))
		if classSym == "" {
			classSym = fqnToSymbol(classFQN)
		}
		candidates = append(candidates, h.findMembersByName(classSym, vi.MethodName)...)
	}

	best := h.selectBestMethodOverload(candidates, len(vi.ArgTypes), vi.ArgTypes, resolver)
	if best == nil {
		return ownerType, nil
	}
	if te := h.idx.DeclTypeOf(best.Symbol); te != nil {
		if ownerType != nil {
			te = substituteTypeParams(te, ownerType, h.idx)
			te = substituteNamedTypeParams(te, ownerType, h.idx)
		}
		return ownerType, te
	}
	if sym := h.idx.TypeOfSymbol(best.Symbol); sym != "" {
		te := &index.TypeExpr{Sym: sym}
		if ownerType != nil {
			te = substituteTypeParams(te, ownerType, h.idx)
			te = substituteNamedTypeParams(te, ownerType, h.idx)
		}
		return ownerType, te
	}
	if best.Signature != nil && best.Signature.ReturnTypeSym != "" {
		return ownerType, &index.TypeExpr{Sym: best.Signature.ReturnTypeSym}
	}
	return ownerType, nil
}
