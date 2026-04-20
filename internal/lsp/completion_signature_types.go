package lsp

import (
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
)

func signatureParamTypeExpr(typeName string, resolver *typeResolver) *index.TypeExpr {
	typeName = normalizeTypeArgName(typeName)
	if typeName == "" {
		return nil
	}

	baseName, argNames := splitGenericName(typeName)
	baseName = normalizeTypeArgName(baseName)
	if baseName == "" {
		return nil
	}

	sym := resolver.resolve(baseName)
	if sym == "" {
		sym = baseName
	}

	te := &index.TypeExpr{Sym: sym}
	for _, arg := range argNames {
		argTE := signatureParamTypeExpr(arg, resolver)
		if argTE != nil {
			te.Args = append(te.Args, argTE)
		}
	}
	return te
}

func extractParamTypeName(paramLabel string) string {
	lastSpace := strings.LastIndex(paramLabel, " ")
	if lastSpace <= 0 {
		return ""
	}
	return strings.TrimSpace(paramLabel[:lastSpace])
}

func normalizeTypeArgName(name string) string {
	name = strings.TrimSpace(name)
	switch {
	case strings.HasPrefix(name, "? extends "):
		return strings.TrimSpace(strings.TrimPrefix(name, "? extends "))
	case strings.HasPrefix(name, "? super "):
		return strings.TrimSpace(strings.TrimPrefix(name, "? super "))
	case name == "?":
		return ""
	default:
		return name
	}
}

func (h *Handler) methodParamCount(sym index.Symbol, knownTypes int) (int, bool) {
	if knownTypes > 0 {
		return knownTypes, true
	}
	if sym.Signature == nil {
		return 0, false
	}
	if n := len(sym.Signature.ParseParams()); n > 0 {
		return n, true
	}
	if !sym.Signature.HasParams {
		return 0, true
	}
	return 0, false
}

func (h *Handler) methodParamTypes(sym index.Symbol, resolver *typeResolver) []*index.TypeExpr {
	if pts := h.index().DeclParamTypesOf(sym.Symbol); len(pts) > 0 {
		return pts
	}
	if sym.Signature == nil {
		return nil
	}

	var result []*index.TypeExpr
	for _, param := range sym.Signature.Params {
		if te := signatureParamTypeExpr(param.Type, resolver); te != nil {
			result = append(result, te)
			continue
		}
		if param.TypeSym != "" {
			result = append(result, &index.TypeExpr{Sym: param.TypeSym})
			continue
		}
		return nil
	}
	if len(result) > 0 {
		return result
	}

	for _, raw := range sym.Signature.ParseParams() {
		typeName := extractParamTypeName(raw)
		if typeName == "" {
			return nil
		}
		te := signatureParamTypeExpr(typeName, resolver)
		if te == nil {
			return nil
		}
		result = append(result, te)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func (h *Handler) methodParamTypeExpr(sym index.Symbol, paramIdx int, resolver *typeResolver) *index.TypeExpr {
	params := h.methodParamTypes(sym, resolver)
	if paramIdx < len(params) {
		return params[paramIdx]
	}
	return nil
}
