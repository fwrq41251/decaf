package index

import "strings"

// TypeExpr represents a type with optional generic type arguments.
// Mirrors SemanticDB's TypeRef but decoupled from protobuf lifetime.
type TypeExpr struct {
	Sym  string      // SemanticDB symbol, e.g. "java/util/List#"
	Args []*TypeExpr // generic type arguments, e.g. [{Sym: "java/lang/String#"}]
}

// String returns a human-readable representation of the type expression.
// e.g. "java/util/List#<java/lang/String#>"
func (te *TypeExpr) String() string {
	if te == nil {
		return ""
	}
	if len(te.Args) == 0 {
		return te.Sym
	}
	var sb strings.Builder
	sb.WriteString(te.Sym)
	sb.WriteByte('<')
	for i, arg := range te.Args {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(arg.String())
	}
	sb.WriteByte('>')
	return sb.String()
}

func typeExprToDisplay(te *TypeExpr) string {
	if te == nil {
		return ""
	}
	base := simplifyDisplayTypeSym(te.Sym)
	if len(te.Args) == 0 {
		return base
	}
	var sb strings.Builder
	sb.WriteString(base)
	sb.WriteByte('<')
	for i, arg := range te.Args {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(typeExprToDisplay(arg))
	}
	sb.WriteByte('>')
	return sb.String()
}

func typeExprBaseSym(te *TypeExpr) string {
	if te == nil {
		return ""
	}
	if strings.Contains(te.Sym, "[") {
		return ""
	}
	return te.Sym
}

func simplifyDisplayTypeSym(sym string) string {
	if start := strings.LastIndexByte(sym, '['); start >= 0 {
		if end := strings.LastIndexByte(sym, ']'); end > start+1 {
			return sym[start+1 : end]
		}
	}
	sym = strings.TrimSuffix(sym, "#")
	sym = strings.TrimSuffix(sym, ".")
	if idx := strings.LastIndexAny(sym, "/."); idx >= 0 {
		return sym[idx+1:]
	}
	return sym
}
