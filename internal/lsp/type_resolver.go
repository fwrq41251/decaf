package lsp

import (
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
)

// typeResolver resolves simple Java type names to SemanticDB symbol strings
// using import declarations and the index.
type typeResolver struct {
	idx     *index.Index
	imports []ImportSpec
	pkg     string // current file's package, e.g. "com.example"
}

// resolveParameterized resolves a type expression into a TypeExpr with SemanticDB symbols.
func (r *typeResolver) resolveParameterized(te *index.TypeExpr) *index.TypeExpr {
	if te == nil {
		return nil
	}
	sym := r.resolve(te.Sym)
	if sym == "" {
		return nil
	}
	result := &index.TypeExpr{Sym: sym}
	for _, arg := range te.Args {
		argTE := r.resolveParameterized(arg)
		if argTE != nil {
			result.Args = append(result.Args, argTE)
		}
	}
	return result
}

// resolveParameterizedStr resolves a type name string (e.g. "List<String>") into a TypeExpr.
func (r *typeResolver) resolveParameterizedStr(name string) *index.TypeExpr {
	baseName, argNames := splitGenericName(name)
	sym := r.resolve(baseName)
	if sym == "" {
		return nil
	}
	te := &index.TypeExpr{Sym: sym}
	for _, arg := range argNames {
		argTE := r.resolveParameterizedStr(arg)
		if argTE != nil {
			te.Args = append(te.Args, argTE)
		}
	}
	return te
}

// splitGenericName splits "Map<String, List<Integer>>" into ("Map", ["String", "List<Integer>"]).
// If no generic arguments, returns (name, nil).
func splitGenericName(name string) (string, []string) {
	ltIdx := strings.IndexByte(name, '<')
	if ltIdx < 0 {
		return name, nil
	}
	baseName := name[:ltIdx]

	// Strip outer < ... >.
	inner := name[ltIdx+1:]
	if len(inner) > 0 && inner[len(inner)-1] == '>' {
		inner = inner[:len(inner)-1]
	}

	// Split by ',' respecting nested angle brackets.
	var args []string
	depth := 0
	start := 0
	for i := 0; i < len(inner); i++ {
		switch inner[i] {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				args = append(args, strings.TrimSpace(inner[start:i]))
				start = i + 1
			}
		}
	}
	args = append(args, strings.TrimSpace(inner[start:]))

	return baseName, args
}

// resolve converts a simple type name (e.g. "List") to a SemanticDB symbol (e.g. "java/util/List#").
// Resolution order: explicit imports → same package → java.lang → wildcard imports → global fallback.
func (r *typeResolver) resolve(name string) string {
	if name == "" || isPrimitive(name) {
		return ""
	}

	// 0. Strip generic arguments for symbol resolution.
	baseName := name
	if ltIdx := strings.IndexByte(name, '<'); ltIdx >= 0 {
		baseName = name[:ltIdx]
	}
	if baseName != name {
		return r.resolve(baseName)
	}

	// Strip array suffix for resolution.
	arrayBase := strings.TrimSuffix(name, "[]")
	if arrayBase == "" || isPrimitive(arrayBase) {
		return ""
	}

	// 1. If it's already an FQN (contains dots), convert to symbol and check index.
	if strings.Contains(arrayBase, ".") {
		if sym := r.resolveInnerClassFQN(arrayBase); sym != "" {
			return sym
		}
	}

	// 1. Explicit imports: look for "import java.util.List" matching "List".
	for _, imp := range r.imports {
		if imp.Static || imp.Wildcard {
			continue
		}
		if idx := strings.LastIndex(imp.Path, "."); idx >= 0 {
			importSimple := imp.Path[idx+1:]
			if importSimple == arrayBase {
				if sym := r.resolveInnerClassFQN(imp.Path); sym != "" {
					return sym
				}
				return fqnToSymbol(imp.Path)
			}
		}
	}

	// 2. Same package: look for "com.example.Foo" if current package is "com.example".
	if r.pkg != "" {
		fqn := r.pkg + "." + arrayBase
		if syms := r.idx.TypeBySimpleName(arrayBase); len(syms) > 0 {
			for _, candidate := range fqnToSymbolVariants(fqn) {
				for _, s := range syms {
					if s.Symbol == candidate {
						return candidate
					}
				}
			}
		}
	}

	// 3. java.lang: String, Object, Integer, etc.
	javaLangSym := "java/lang/" + arrayBase + "#"
	if syms := r.idx.TypeBySimpleName(arrayBase); len(syms) > 0 {
		for _, s := range syms {
			if s.Symbol == javaLangSym {
				return javaLangSym
			}
		}
	}

	// 4. Wildcard imports: look for "import java.util.*" matching "List".
	for _, imp := range r.imports {
		if !imp.Wildcard || imp.Static {
			continue
		}
		pkg := strings.TrimSuffix(imp.Path, ".*")
		fqn := pkg + "." + arrayBase
		if syms := r.idx.TypeBySimpleName(arrayBase); len(syms) > 0 {
			for _, candidate := range fqnToSymbolVariants(fqn) {
				for _, s := range syms {
					if s.Symbol == candidate {
						return candidate
					}
				}
			}
		}
	}

	// 5. Global fallback: only resolve if there is exactly one match.
	if syms := r.idx.TypeBySimpleName(arrayBase); len(syms) == 1 {
		return syms[0].Symbol
	}

	return ""
}

// fqnToSymbol converts a Java FQN to a SemanticDB symbol.
// e.g. "java.util.List" → "java/util/List#"
func fqnToSymbol(fqn string) string {
	return strings.ReplaceAll(fqn, ".", "/") + "#"
}

// fqnToSymbolVariants returns all possible SemanticDB symbols for an FQN,
// accounting for inner classes. For "com.example.Outer.Inner", it returns:
//   - "com/example/Outer/Inner#"  (all dots as package separators)
//   - "com/example/Outer$Inner#"  (last dot as inner class separator)
//   - "com/example$Outer$Inner#"  (etc.)
func fqnToSymbolVariants(fqn string) []string {
	base := fqnToSymbol(fqn)
	variants := []string{base}

	// Progressively replace rightmost "/" with "$" to generate inner class variants.
	sym := base[:len(base)-1] // strip trailing "#"
	for {
		idx := strings.LastIndex(sym, "/")
		if idx < 0 {
			break
		}
		sym = sym[:idx] + "$" + sym[idx+1:]
		variants = append(variants, sym+"#")
	}
	return variants
}

// resolveInnerClassFQN tries to resolve an FQN to a symbol in the index,
// handling inner classes by trying "$" separator variants.
func (r *typeResolver) resolveInnerClassFQN(fqn string) string {
	for _, sym := range fqnToSymbolVariants(fqn) {
		if def := r.idx.SymbolDefinition(sym); def != nil {
			return sym
		}
	}
	return ""
}

func isPrimitive(name string) bool {
	switch name {
	case "int", "long", "short", "byte", "float", "double", "boolean", "char", "void":
		return true
	}
	return false
}
