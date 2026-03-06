package index

import (
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

// Symbol represents an indexed symbol definition.
type Symbol struct {
	Name   string
	Symbol string // SemanticDB symbol string, e.g. "com/example/Foo#"
	Kind   sdb.SymbolInformation_Kind
	URI    string // relative URI from SemanticDB
	Range  *sdb.Range
}

// Occurrence represents a symbol occurrence (reference or definition).
type Occurrence struct {
	Symbol string
	Role   sdb.SymbolOccurrence_Role
	URI    string
	Range  *sdb.Range
}
