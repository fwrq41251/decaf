package index

// TypeExpr represents a type with optional generic type arguments.
// Mirrors SemanticDB's TypeRef but decoupled from protobuf lifetime.
type TypeExpr struct {
	Sym  string      // SemanticDB symbol, e.g. "java/util/List#"
	Args []*TypeExpr // generic type arguments, e.g. [{Sym: "java/lang/String#"}]
}
