package index

import "strings"

// JVM generic signature parser (JVM Spec §4.7.9.1).
//
// Class signature format:
//   <E:Ljava/lang/Object;>Ljava/util/AbstractList<TE;>;Ljava/util/List<TE;>;
//   ^^^^^^^^^^^^^^^^^^^^^^^^ type params         ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^ parent types
//
// Method signature format:
//   (I)TE;      — returns type variable E
//   <T:Ljava/lang/Object;>(TT;)Ljava/util/List<TT;>;
//
// Field signature format:
//   Ljava/util/List<Ljava/lang/String;>;
//   TE;

// classGenericInfo holds parsed generic information from a class Signature attribute.
type classGenericInfo struct {
	typeParams []string    // type parameter names, e.g. ["E", "K", "V"]
	parents    []*TypeExpr // parent types with generic args
}

// memberGenericInfo holds parsed generic information from a field/method Signature attribute.
type memberGenericInfo struct {
	paramTypes []*TypeExpr
	returnType *TypeExpr // for methods: the return type; for fields: the field type
}

// parseClassGenericSig parses a class-level generic signature.
// classSym is used to build type parameter symbols in the form "classSym[E]".
//
// Example: "<E:Ljava/lang/Object;>Ljava/util/AbstractList<TE;>;Ljava/util/List<TE;>;"
func parseClassGenericSig(sig, classSym string) *classGenericInfo {
	if sig == "" {
		return nil
	}

	p := &sigParser{sig: sig}
	info := &classGenericInfo{}

	// Parse type parameters if present.
	if p.peek() == '<' {
		info.typeParams = p.parseTypeParamNames()
	}

	// Parse parent types (superclass + superinterfaces).
	for !p.eof() {
		te := p.parseTypeExpr(classSym, info.typeParams)
		if te != nil {
			info.parents = append(info.parents, te)
		}
	}

	return info
}

// parseMethodGenericSig parses a method-level generic signature and returns
// parameter and return types as TypeExprs.
//
// Example: "(I)TE;" or "<T:Ljava/lang/Object;>(TT;)Ljava/util/List<TT;>;"
func parseMethodGenericSig(sig, classSym string, classTypeParamNames []string) *memberGenericInfo {
	if sig == "" {
		return nil
	}

	p := &sigParser{sig: sig}

	// Skip method-level type parameters if present.
	var methodTypeParams []string
	if p.peek() == '<' {
		methodTypeParams = p.parseTypeParamNames()
	}

	// Merge class + method type params for resolution.
	allParams := append(classTypeParamNames, methodTypeParams...)

	var paramTypes []*TypeExpr
	// Parse parameter types: (...)
	if p.peek() == '(' {
		p.advance() // skip '('
		for !p.eof() && p.peek() != ')' {
			paramTypes = append(paramTypes, p.parseTypeExpr(classSym, allParams))
		}
		if !p.eof() {
			p.advance() // skip ')'
		}
	}

	// Parse return type.
	retType := p.parseTypeExpr(classSym, allParams)

	return &memberGenericInfo{paramTypes: paramTypes, returnType: retType}
}

// parseFieldGenericSig parses a field-level generic signature.
//
// Example: "Ljava/util/List<Ljava/lang/String;>;" or "TE;"
func parseFieldGenericSig(sig, classSym string, classTypeParamNames []string) *memberGenericInfo {
	if sig == "" {
		return nil
	}

	p := &sigParser{sig: sig}
	te := p.parseTypeExpr(classSym, classTypeParamNames)
	return &memberGenericInfo{returnType: te}
}

// sigParser is a simple recursive-descent parser for JVM generic signatures.
type sigParser struct {
	sig string
	pos int
}

func (p *sigParser) eof() bool {
	return p.pos >= len(p.sig)
}

func (p *sigParser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.sig[p.pos]
}

func (p *sigParser) advance() {
	if !p.eof() {
		p.pos++
	}
}

// parseTypeParamNames parses "<E:bound;K:bound;V:bound;>" and returns ["E", "K", "V"].
// It only extracts the names, skipping bound information.
func (p *sigParser) parseTypeParamNames() []string {
	if p.peek() != '<' {
		return nil
	}
	p.advance() // skip '<'

	var names []string
	for !p.eof() && p.peek() != '>' {
		// Each type param: Name : ClassBound {InterfaceBound}
		// Name is everything up to the first ':'
		start := p.pos
		for !p.eof() && p.peek() != ':' {
			p.advance()
		}
		name := p.sig[start:p.pos]
		names = append(names, name)

		// Skip ':' and class bound.
		if !p.eof() {
			p.advance() // skip ':'
		}
		// Class bound may be empty (just ':') or a type signature.
		if !p.eof() && p.peek() != ':' && p.peek() != '>' {
			p.skipTypeSignature()
		}
		// Skip interface bounds (each preceded by ':').
		for !p.eof() && p.peek() == ':' {
			p.advance() // skip ':'
			p.skipTypeSignature()
		}
	}
	if !p.eof() {
		p.advance() // skip '>'
	}
	return names
}

// parseTypeExpr parses a type signature and returns a TypeExpr.
// classSym is the owning class symbol (for building type param refs).
// typeParamNames are the known type parameter names.
func (p *sigParser) parseTypeExpr(classSym string, typeParamNames []string) *TypeExpr {
	if p.eof() {
		return nil
	}

	switch p.peek() {
	case 'T':
		// Type variable reference: TE;
		p.advance() // skip 'T'
		start := p.pos
		for !p.eof() && p.peek() != ';' {
			p.advance()
		}
		name := p.sig[start:p.pos]
		if !p.eof() {
			p.advance() // skip ';'
		}
		// Build a type parameter symbol like "java/util/List#[E]"
		return &TypeExpr{Sym: classSym + "[" + name + "]"}

	case 'L':
		// Class type: Ljava/util/List<TE;>;
		p.advance() // skip 'L'
		start := p.pos
		// Read class name until '<', ';', or '.'
		for !p.eof() && p.peek() != '<' && p.peek() != ';' && p.peek() != '.' {
			p.advance()
		}
		className := p.sig[start:p.pos]
		te := &TypeExpr{Sym: className + "#"}

		// Parse type arguments if present.
		if !p.eof() && p.peek() == '<' {
			p.advance() // skip '<'
			for !p.eof() && p.peek() != '>' {
				// Handle wildcard type arguments.
				if p.peek() == '+' || p.peek() == '-' {
					p.advance() // skip wildcard indicator
					arg := p.parseTypeExpr(classSym, typeParamNames)
					if arg != nil {
						te.Args = append(te.Args, arg)
					}
				} else if p.peek() == '*' {
					p.advance() // unbounded wildcard
					te.Args = append(te.Args, &TypeExpr{Sym: "java/lang/Object#"})
				} else {
					arg := p.parseTypeExpr(classSym, typeParamNames)
					if arg != nil {
						te.Args = append(te.Args, arg)
					}
				}
			}
			if !p.eof() {
				p.advance() // skip '>'
			}
		}

		// Skip inner class suffixes (e.g. ".Entry") and trailing ';'.
		for !p.eof() && p.peek() != ';' {
			if p.peek() == '.' {
				// Inner class: Ljava/util/Map.Entry<TK;TV;>;
				p.advance() // skip '.'
				innerStart := p.pos
				for !p.eof() && p.peek() != '<' && p.peek() != ';' && p.peek() != '.' {
					p.advance()
				}
				innerName := p.sig[innerStart:p.pos]
				// Rewrite to use $ for inner class: "java/util/Map$Entry#"
				te.Sym = strings.TrimSuffix(te.Sym, "#") + "$" + innerName + "#"
				// Parse inner class type arguments if present.
				if !p.eof() && p.peek() == '<' {
					te.Args = nil // inner class args override outer
					p.advance()   // skip '<'
					for !p.eof() && p.peek() != '>' {
						if p.peek() == '+' || p.peek() == '-' {
							p.advance()
							arg := p.parseTypeExpr(classSym, typeParamNames)
							if arg != nil {
								te.Args = append(te.Args, arg)
							}
						} else if p.peek() == '*' {
							p.advance()
							te.Args = append(te.Args, &TypeExpr{Sym: "java/lang/Object#"})
						} else {
							arg := p.parseTypeExpr(classSym, typeParamNames)
							if arg != nil {
								te.Args = append(te.Args, arg)
							}
						}
					}
					if !p.eof() {
						p.advance() // skip '>'
					}
				}
			} else {
				p.advance()
			}
		}
		if !p.eof() {
			p.advance() // skip ';'
		}
		return te

	case '[':
		// Array type: skip and return the element type symbol.
		p.advance() // skip '['
		return p.parseTypeExpr(classSym, typeParamNames)

	default:
		// Primitive type.
		te := primitiveTypeExpr(p.peek())
		p.advance()
		return te
	}
}

// skipTypeSignature skips over a complete type signature without building a TypeExpr.
func (p *sigParser) skipTypeSignature() {
	if p.eof() {
		return
	}
	switch p.peek() {
	case 'T':
		// Type variable: TE;
		p.advance()
		for !p.eof() && p.peek() != ';' {
			p.advance()
		}
		if !p.eof() {
			p.advance() // skip ';'
		}
	case 'L':
		// Class type with possible generics.
		p.advance()
		depth := 0
		for !p.eof() {
			if p.peek() == '<' {
				depth++
			} else if p.peek() == '>' {
				depth--
			} else if p.peek() == ';' && depth == 0 {
				p.advance()
				return
			}
			p.advance()
		}
	case '[':
		p.advance()
		p.skipTypeSignature()
	default:
		// Primitive.
		p.advance()
	}
}

// primitiveTypeExpr returns a TypeExpr for a JVM primitive type character, or nil.
func primitiveTypeExpr(ch byte) *TypeExpr {
	switch ch {
	case 'V':
		return &TypeExpr{Sym: "void"}
	case 'Z':
		return &TypeExpr{Sym: "boolean"}
	case 'B':
		return &TypeExpr{Sym: "byte"}
	case 'C':
		return &TypeExpr{Sym: "char"}
	case 'S':
		return &TypeExpr{Sym: "short"}
	case 'I':
		return &TypeExpr{Sym: "int"}
	case 'J':
		return &TypeExpr{Sym: "long"}
	case 'F':
		return &TypeExpr{Sym: "float"}
	case 'D':
		return &TypeExpr{Sym: "double"}
	default:
		return nil
	}
}
