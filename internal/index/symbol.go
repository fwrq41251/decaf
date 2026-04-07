package index

import (
	"strings"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

// SignatureInfo holds pre-computed signature display information,
// replacing the heavy protobuf *sdb.Signature tree.
type ParamInfo struct {
	Name    string
	Type    string
	TypeSym string
	Varargs bool
}

type SignatureInfo struct {
	Label         string // formatted signature, e.g. "void main(String[] args)"
	ReturnTypeSym string // symbol of the return type (for structured resolution)
	HasParams     bool   // true if method has parameters (for snippet generation)
	Params        []ParamInfo
}

func (p ParamInfo) Label() string {
	typeName := strings.TrimSpace(p.Type)
	if p.Varargs {
		typeName = strings.TrimSuffix(typeName, "[]") + "..."
	}
	if p.Name == "" {
		return typeName
	}
	if typeName == "" {
		return p.Name
	}
	return typeName + " " + p.Name
}

// ParseParams lazily extracts parameter labels from the Label string.
// e.g. "void add(String name, int x)" → ["String name", "int x"]
func (s *SignatureInfo) ParseParams() []string {
	if s == nil || !s.HasParams {
		return nil
	}
	if len(s.Params) > 0 {
		params := make([]string, 0, len(s.Params))
		for _, p := range s.Params {
			if label := p.Label(); label != "" {
				params = append(params, label)
			}
		}
		if len(params) > 0 {
			return params
		}
	}
	// Find the parameter list between the first '(' and the matching ')'.
	start := strings.IndexByte(s.Label, '(')
	if start < 0 {
		return nil
	}
	end := strings.LastIndexByte(s.Label, ')')
	if end <= start+1 {
		return nil
	}
	inner := s.Label[start+1 : end]
	// Split by ", " respecting nested generics (e.g. "Map<String, Integer> m, int x").
	var params []string
	depth := 0
	paramStart := 0
	for i := 0; i < len(inner); i++ {
		switch inner[i] {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				p := strings.TrimSpace(inner[paramStart:i])
				if p != "" {
					params = append(params, p)
				}
				paramStart = i + 1
			}
		}
	}
	if p := strings.TrimSpace(inner[paramStart:]); p != "" {
		params = append(params, p)
	}
	return params
}

// Range is a compact, 16-byte representation of a SemanticDB range (4 * int32).
// It replaces the heavy *sdb.Range protobuf pointer to save memory and reduce GC pressure.
type Range struct {
	StartLine      int32
	StartCharacter int32
	EndLine        int32
	EndCharacter   int32
}

// IsEmpty returns true if the range is uninitialized (all zeros).
func (r Range) IsEmpty() bool {
	return r.StartLine == 0 && r.StartCharacter == 0 && r.EndLine == 0 && r.EndCharacter == 0
}

// ToSDB converts back to the SemanticDB protobuf format for external use.
func (r Range) ToSDB() *sdb.Range {
	if r.IsEmpty() {
		return nil
	}
	return &sdb.Range{
		StartLine:      r.StartLine,
		StartCharacter: r.StartCharacter,
		EndLine:        r.EndLine,
		EndCharacter:   r.EndCharacter,
	}
}

// FromSDB creates a compact Range from a SemanticDB protobuf range.
func FromSDB(r *sdb.Range) Range {
	if r == nil {
		return Range{}
	}
	return Range{
		StartLine:      r.StartLine,
		StartCharacter: r.StartCharacter,
		EndLine:        r.EndLine,
		EndCharacter:   r.EndCharacter,
	}
}

// Symbol represents an indexed symbol definition.
type Symbol struct {
	Name       string
	Symbol     string // SemanticDB symbol string, e.g. "com/example/Foo#"
	Kind       sdb.SymbolInformation_Kind
	URI        string // relative URI from SemanticDB
	Range      Range  // compact range (was *sdb.Range)
	Signature  *SignatureInfo
	Doc        string // Javadoc/Scaladoc documentation text
	IsStatic   bool
	IsAbstract bool
	SameFile   bool
}

type SymbolID int

// Occurrence represents a symbol occurrence (reference or definition).
type Occurrence struct {
	Symbol string
	Role   sdb.SymbolOccurrence_Role
	URI    string
	Range  Range // compact range (was *sdb.Range)
}
