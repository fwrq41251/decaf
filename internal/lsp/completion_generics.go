package lsp

import (
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
)

func substituteNamedTypeParams(te *index.TypeExpr, owner *index.TypeExpr, idx *index.Index) *index.TypeExpr {
	if te == nil || owner == nil || len(owner.Args) == 0 {
		return te
	}

	typeParams := idx.ClassTypeParams(owner.Sym)
	if len(typeParams) == 0 {
		return te
	}

	subst := make(map[string]*index.TypeExpr)
	for i, tp := range typeParams {
		if i >= len(owner.Args) {
			break
		}
		name := typeParamSimpleName(tp)
		if name != "" {
			subst[name] = owner.Args[i]
		}
	}
	if len(subst) == 0 {
		return te
	}
	return applyNamedSubstitution(te, subst)
}

func typeParamSimpleName(sym string) string {
	start := strings.LastIndexByte(sym, '[')
	end := strings.LastIndexByte(sym, ']')
	if start < 0 || end <= start+1 {
		return ""
	}
	return sym[start+1 : end]
}

func applyNamedSubstitution(te *index.TypeExpr, subst map[string]*index.TypeExpr) *index.TypeExpr {
	if te == nil {
		return nil
	}
	if resolved, ok := subst[te.Sym]; ok {
		return resolved
	}
	if len(te.Args) == 0 {
		return te
	}
	result := &index.TypeExpr{Sym: te.Sym, Args: make([]*index.TypeExpr, len(te.Args))}
	for i, arg := range te.Args {
		result.Args[i] = applyNamedSubstitution(arg, subst)
	}
	return result
}

func (h *Handler) refineVarInitializerType(te, ownerType *index.TypeExpr, vi *VarInitializer, resolver *typeResolver) *index.TypeExpr {
	if te == nil || vi == nil || len(te.Args) == 0 {
		return te
	}

	hasUnresolved := false
	for _, arg := range te.Args {
		if isUnresolvedTypeParam(arg) {
			hasUnresolved = true
			break
		}
	}
	if !hasUnresolved {
		return te
	}

	if inferred := h.inferTypeParamsFromArgs(te, ownerType, vi, resolver); inferred != nil {
		return inferred
	}

	return te
}

func (h *Handler) inferTypeParamsFromArgs(te, ownerType *index.TypeExpr, vi *VarInitializer, resolver *typeResolver) *index.TypeExpr {
	if len(vi.ArgTypes) == 0 || ownerType == nil {
		return nil
	}

	method := h.selectBestMethodOverload(h.findMembersByName(ownerType.Sym, vi.MethodName), len(vi.ArgTypes), vi.ArgTypes, resolver)
	if method == nil {
		return nil
	}
	formalParams := h.methodParamTypes(*method, resolver)
	if len(formalParams) == 0 {
		return nil
	}

	bindings := make(map[string]*index.TypeExpr)
	for i, formal := range formalParams {
		if i >= len(vi.ArgTypes) {
			break
		}
		actual := vi.ArgTypes[i]
		if actual == nil {
			continue
		}
		resolved := resolver.resolveParameterized(actual)
		if resolved == nil {
			resolved = actual
		}
		formal = substituteTypeParams(formal, ownerType, h.idx)
		formal = substituteNamedTypeParams(formal, ownerType, h.idx)
		collectTypeParamBindings(formal, resolved, bindings)
	}
	if len(bindings) == 0 {
		return nil
	}

	result := &index.TypeExpr{Sym: te.Sym, Args: make([]*index.TypeExpr, len(te.Args))}
	changed := false
	for i, arg := range te.Args {
		if isUnresolvedTypeParam(arg) {
			if bound, ok := bindings[arg.Sym]; ok {
				result.Args[i] = bound
				changed = true
				continue
			}
		}
		result.Args[i] = arg
	}
	if !changed {
		return nil
	}
	return result
}

func collectTypeParamBindings(formal, actual *index.TypeExpr, bindings map[string]*index.TypeExpr) {
	if formal == nil || actual == nil {
		return
	}
	if isUnresolvedTypeParam(formal) {
		if existing, ok := bindings[formal.Sym]; ok {
			if !sameTypeExpr(existing, actual) {
				return
			}
		}
		bindings[formal.Sym] = actual
		return
	}
	if formal.Sym == actual.Sym && len(formal.Args) > 0 && len(formal.Args) == len(actual.Args) {
		for i := range formal.Args {
			collectTypeParamBindings(formal.Args[i], actual.Args[i], bindings)
		}
	}
}

func isUnresolvedTypeParam(te *index.TypeExpr) bool {
	if te == nil {
		return false
	}
	if strings.Contains(te.Sym, "[") {
		return true
	}
	return len(te.Args) == 0 && len(te.Sym) == 1 && te.Sym[0] >= 'A' && te.Sym[0] <= 'Z'
}

func sameTypeExpr(a, b *index.TypeExpr) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Sym != b.Sym || len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Args {
		if !sameTypeExpr(a.Args[i], b.Args[i]) {
			return false
		}
	}
	return true
}

func substituteTypeParams(retType *index.TypeExpr, owner *index.TypeExpr, idx *index.Index) *index.TypeExpr {
	if retType == nil || owner == nil || len(owner.Args) == 0 {
		return retType
	}

	subst := buildSubstitutionMap(owner, idx)
	if len(subst) == 0 {
		return retType
	}

	return applySubstitution(retType, subst)
}

func buildSubstitutionMap(owner *index.TypeExpr, idx *index.Index) map[string]*index.TypeExpr {
	subst := make(map[string]*index.TypeExpr)

	typeParams := idx.ClassTypeParams(owner.Sym)
	for i, tp := range typeParams {
		if i < len(owner.Args) {
			subst[tp] = owner.Args[i]
		}
	}

	visited := make(map[string]bool)
	queue := []string{owner.Sym}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if visited[current] {
			continue
		}
		visited[current] = true

		for _, parentType := range idx.ParentTypesOf(current) {
			parentParams := idx.ClassTypeParams(parentType.Sym)
			for i, pp := range parentParams {
				if i < len(parentType.Args) {
					subst[pp] = applySubstitution(parentType.Args[i], subst)
				}
			}
			queue = append(queue, parentType.Sym)
		}
	}

	return subst
}

func applySubstitution(te *index.TypeExpr, subst map[string]*index.TypeExpr) *index.TypeExpr {
	if te == nil {
		return nil
	}

	if resolved, ok := subst[te.Sym]; ok {
		return resolved
	}

	if len(te.Args) > 0 {
		result := &index.TypeExpr{Sym: te.Sym, Args: make([]*index.TypeExpr, len(te.Args))}
		for i, arg := range te.Args {
			result.Args[i] = applySubstitution(arg, subst)
		}
		return result
	}

	return te
}
