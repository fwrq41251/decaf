package index

import (
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

// SignatureInfo holds pre-computed signature display information,
// replacing the heavy protobuf *sdb.Signature tree.
type SignatureInfo struct {
	Label  string   // formatted signature, e.g. "void main(String[] args)"
	Params []string // individual parameter labels, e.g. ["String[] args"]
}

// Symbol represents an indexed symbol definition.
type Symbol struct {
	Name      string
	Symbol    string // SemanticDB symbol string, e.g. "com/example/Foo#"
	Kind      sdb.SymbolInformation_Kind
	URI       string // relative URI from SemanticDB
	Range     *sdb.Range
	Signature *SignatureInfo
	Doc       string // Javadoc/Scaladoc documentation text
	IsStatic  bool
	SameFile  bool
}

// Occurrence represents a symbol occurrence (reference or definition).
type Occurrence struct {
	Symbol string
	Role   sdb.SymbolOccurrence_Role
	URI    string
	Range  *sdb.Range
}
