package lsp

import "github.com/fwrq41251/decaf/internal/index"

func (h *Handler) selectBestMethodOverload(candidates []index.Symbol, desiredArity int, actualArgTypes []*index.TypeExpr, resolver *typeResolver) *index.Symbol {
	var best *index.Symbol
	bestScore := -1 << 30

	for i := range candidates {
		candidate := candidates[i]
		score := 0

		paramTypes := h.methodParamTypes(candidate, resolver)
		paramCount, countKnown := h.methodParamCount(candidate, len(paramTypes))
		if desiredArity >= 0 && countKnown {
			if paramCount == desiredArity {
				score += 1000
			} else if paramCount < desiredArity {
				continue
			} else {
				score -= (paramCount - desiredArity) * 10
			}
		}
		if len(paramTypes) > 0 {
			score += 100
		}
		if actualArgTypes != nil && len(paramTypes) > 0 {
			for j := 0; j < len(actualArgTypes) && j < len(paramTypes); j++ {
				actual := actualArgTypes[j]
				if actual == nil {
					continue
				}
				resolved := resolver.resolveParameterized(actual)
				if resolved == nil {
					resolved = actual
				}
				switch {
				case sameTypeExpr(paramTypes[j], resolved):
					score += 20
				case isUnresolvedTypeParam(paramTypes[j]):
					score += 5
				case typeExprMatchesExpected(resolved, paramTypes[j]):
					score += 10
				}
			}
		}

		if best == nil || score > bestScore {
			best = &candidates[i]
			bestScore = score
		}
	}

	return best
}
