package index

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"github.com/fwrq41251/decaf/internal/uri"
)

// Definition returns the definition locations for a symbol at the given position.
func (idx *Index) Definition(uri string, line, character int) []Symbol {
	idx.mu.RLock()

	// Find the symbol at the given position.
	relURI := idx.toRelativeURI(uri)
	sym := idx.symbolAt(relURI, line, character)
	idx.logger.Printf("Definition request: uri=%s, relURI=%s, symbolAt=%s", uri, relURI, sym)

	if sym == "" {
		idx.mu.RUnlock()
		return nil
	}

	defs := idx.definitions[sym]

	// If it's an internal symbol with definitions that have source locations, return them.
	if len(defs) > 0 {
		result := deduplicateSymbols(copySymbols(defs))
		// Only return results if at least one symbol has a valid range.
		// ClassIndexer may have added symbols to definitions without ranges
		// for completion/hover support; these should not block external resolution.
		hasRange := false
		for _, s := range result {
			if s.Range != nil {
				hasRange = true
				break
			}
		}
		if hasRange {
			idx.mu.RUnlock()
			return result
		}
	}

	// No definitions with source locations — try external symbol resolution
	// (JDK source, dependency source JARs).
	jdkRoot := idx.jdkSourceRoot
	depSources := idx.dependencySources
	idx.logger.Printf("No local definitions for %s. Probing external: jdkRoot=%s, depSources=%d", sym, jdkRoot, len(depSources))
	idx.mu.RUnlock()

	if jdkRoot != "" || len(depSources) > 0 {
		if ext := idx.resolveExternalSymbol(sym); ext != nil {
			return []Symbol{*ext}
		}
	}
	return nil
}

// References returns all reference locations for a symbol at the given position.
func (idx *Index) References(uri string, line, character int) []Occurrence {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	sym := idx.symbolAt(relURI, line, character)
	if sym == "" {
		return nil
	}

	refs := idx.references[sym]
	return deduplicateOccurrences(copyOccurrences(refs))
}

// Hover returns the symbol information at the given position (for hover).
func (idx *Index) Hover(uri string, line, character int) *Symbol {
	idx.mu.RLock()

	relURI := idx.toRelativeURI(uri)
	sym := idx.symbolAt(relURI, line, character)
	if sym == "" {
		idx.mu.RUnlock()
		return nil
	}

	defs := idx.definitions[sym]
	if len(defs) > 0 {
		result := *defs[0]
		idx.mu.RUnlock()
		return &result
	}

	idx.mu.RUnlock()

	// Fallback for external symbols (JDK/Dependencies).
	if ext := idx.resolveExternalSymbol(sym); ext != nil {
		return ext
	}
	return nil
}

// FileSymbols returns all symbol definitions in the given file (for documentSymbol).
func (idx *Index) FileSymbols(uri string) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	return copySymbols(idx.fileSymbols[relURI])
}

// symbolAt returns the SemanticDB symbol string at the given position.
// Occurrences are sorted by start position, so we use binary search to find
// the neighborhood and then check containment.
func (idx *Index) symbolAt(uri string, line, character int) string {
	uri = filepath.ToSlash(uri)
	occs, ok := idx.fileOccurrences[uri]
	if !ok {
		return ""
	}

	line32, char32 := int32(line), int32(character)

	// Binary search: find the first occurrence that starts after (line, character).
	i := sort.Search(len(occs), func(i int) bool {
		r := occs[i].Range
		if r == nil {
			return false
		}
		if r.StartLine != line32 {
			return r.StartLine > line32
		}
		return r.StartCharacter > char32
	})

	// Check candidates: the match is at index i-1 or earlier (multi-line spans).
	// Walk backwards from i to find an occurrence that contains the position.
	for j := i - 1; j >= 0; j-- {
		r := occs[j].Range
		if r == nil {
			continue
		}
		// Stop early: if this occurrence ends before our line, no earlier one can contain us.
		if r.EndLine < line32 {
			break
		}
		if containsPosition(r, line, character) {
			return occs[j].Symbol
		}
	}
	return ""
}

// AllSymbols returns all indexed symbol definitions (for completion, etc).
func (idx *Index) AllSymbols() []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []Symbol
	for _, defs := range idx.definitions {
		for _, d := range defs {
			result = append(result, *d)
		}
	}
	return result
}

// toRelativeURI converts a file:// URI or an absolute path to a relative path matching SemanticDB URIs.
func (idx *Index) toRelativeURI(u string) string {
	if !uri.IsURI(u) && !filepath.IsAbs(u) {
		return filepath.ToSlash(u)
	}
	return uri.Rel(idx.sourceRoot, u)
}

// SourceRoot returns the workspace source root path.
func (idx *Index) SourceRoot() string {
	return idx.sourceRoot
}

// SearchSymbols returns symbols matching the query string (case-insensitive substring match).
func (idx *Index) SearchSymbols(query string) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	query = strings.ToLower(query)
	var result []Symbol
	for _, defs := range idx.definitions {
		for _, d := range defs {
			if strings.Contains(strings.ToLower(d.Name), query) {
				result = append(result, *d)
			}
		}
	}
	return result
}

// CompletionSymbols returns symbols matching the given query for completion.
// Results from the same file are prioritized.
func (idx *Index) CompletionSymbols(uri string, query string) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	query = strings.ToLower(query)
	relURI := idx.toRelativeURI(uri)

	// Collect types and non-types separately so we can prioritize type names.
	var sameFileTypes, sameFileOther []Symbol
	var otherTypes, otherOther []Symbol
	for _, defs := range idx.definitions {
		for _, d := range defs {
			if !FuzzyMatch(d.Name, query) {
				continue
			}
			isType := isTypeKind(d.Kind)
			if d.URI == relURI {
				s := *d
				s.SameFile = true
				if isType {
					sameFileTypes = append(sameFileTypes, s)
				} else {
					sameFileOther = append(sameFileOther, s)
				}
			} else {
				if isType {
					otherTypes = append(otherTypes, *d)
				} else {
					otherOther = append(otherOther, *d)
				}
			}
		}
	}

	// Priority: same-file types > other types > same-file members > other members.
	result := make([]Symbol, 0, len(sameFileTypes)+len(otherTypes)+len(sameFileOther)+len(otherOther))
	result = append(result, sameFileTypes...)
	result = append(result, otherTypes...)
	result = append(result, sameFileOther...)
	result = append(result, otherOther...)

	// Cap at 100 results.
	if len(result) > 100 {
		result = result[:100]
	}
	return result
}

func FuzzyMatch(name, query string) bool {
	if query == "" {
		return true
	}
	name = strings.ToLower(name)
	query = strings.ToLower(query)
	ni, qi := 0, 0
	for ni < len(name) && qi < len(query) {
		if name[ni] == query[qi] {
			qi++
		}
		ni++
	}
	return qi == len(query)
}

// SymbolSignature returns the method signature for the symbol at the given position.
func (idx *Index) SymbolSignature(uri string, line, character int) *Symbol {
	sigs := idx.SymbolSignatures(uri, line, character)
	if len(sigs) == 0 {
		return nil
	}
	return &sigs[0]
}

// SymbolSignatures returns all overloaded method signatures for the symbol at the given position.
func (idx *Index) SymbolSignatures(uri string, line, character int) []Symbol {
	idx.mu.RLock()

	relURI := idx.toRelativeURI(uri)
	sym := idx.symbolAt(relURI, line, character)
	if sym == "" {
		idx.mu.RUnlock()
		return nil
	}

	defs := idx.definitions[sym]
	if len(defs) > 0 {
		result := make([]Symbol, len(defs))
		for i, d := range defs {
			result[i] = *d
		}
		idx.mu.RUnlock()
		return result
	}

	idx.mu.RUnlock()

	// Fallback for external symbols (JDK/Dependencies).
	if ext := idx.resolveExternalSymbol(sym); ext != nil {
		return []Symbol{*ext}
	}
	return nil
}

// RenameOccurrences returns all occurrences (definitions + references) for rename.
func (idx *Index) RenameOccurrences(uri string, line, character int) (string, []Occurrence) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	sym := idx.symbolAt(relURI, line, character)
	if sym == "" {
		return "", nil
	}

	var result []Occurrence

	// Collect definition occurrences.
	for _, d := range idx.definitions[sym] {
		if d.Range != nil {
			result = append(result, Occurrence{
				Symbol: d.Symbol,
				Role:   sdb.SymbolOccurrence_DEFINITION,
				URI:    d.URI,
				Range:  d.Range,
			})
		}
	}

	// Collect reference occurrences.
	for _, occ := range idx.references[sym] {
		result = append(result, *occ)
	}

	return sym, result
}

// OccurrenceAt returns the SemanticDB occurrence at the given position.
// Occurrences are sorted by start position, so we use binary search to find
// the neighborhood and then check containment.
func (idx *Index) OccurrenceAt(uri string, line, character int) *Occurrence {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	occs, ok := idx.fileOccurrences[relURI]
	if !ok {
		return nil
	}

	line32, char32 := int32(line), int32(character)

	// Binary search: find the first occurrence that starts after (line, character).
	i := sort.Search(len(occs), func(i int) bool {
		r := occs[i].Range
		if r == nil {
			return false
		}
		if r.StartLine != line32 {
			return r.StartLine > line32
		}
		return r.StartCharacter > char32
	})

	// Walk backwards from i to find an occurrence that contains the position.
	for j := i - 1; j >= 0; j-- {
		r := occs[j].Range
		if r == nil {
			continue
		}
		if r.EndLine < line32 {
			break
		}
		if containsPosition(r, line, character) {
			return occs[j]
		}
	}
	return nil
}

// AllFileOccurrences returns all occurrences in the given file.
func (idx *Index) AllFileOccurrences(uri string) []Occurrence {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	return copyOccurrences(idx.fileOccurrences[relURI])
}

// SymbolDefinition returns the definition for a SemanticDB symbol string.
// This is useful for resolving a symbol's fully-qualified type without a position.
func (idx *Index) SymbolDefinition(sym string) *Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	defs := idx.definitions[sym]
	if len(defs) == 0 {
		return nil
	}
	s := *defs[0]
	return &s
}

// FileOccurrencesOf returns all occurrences of a symbol in a specific file.
func (idx *Index) FileOccurrencesOf(uri string, line, character int) []Occurrence {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	sym := idx.symbolAt(relURI, line, character)
	if sym == "" {
		return nil
	}

	var result []Occurrence
	for _, occ := range idx.fileOccurrences[relURI] {
		if occ.Symbol == sym {
			result = append(result, *occ)
		}
	}
	return result
}

// Implementations returns definitions of types that implement/extend the symbol at the given position.
func (idx *Index) Implementations(uri string, line, character int) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	sym := idx.symbolAt(relURI, line, character)
	if sym == "" {
		return nil
	}

	implSymbols := idx.implementors[sym]
	var result []Symbol
	for _, implSym := range implSymbols {
		if defs, ok := idx.definitions[implSym]; ok {
			for _, d := range defs {
				result = append(result, *d)
			}
		}
	}
	return result
}

// MembersOfType returns all direct member symbols of a type,
// plus inherited members from parent types.
func (idx *Index) MembersOfType(typeSym string) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	seen := make(map[string]struct{})
	var result []Symbol
	idx.collectMembers(typeSym, seen, &result)
	return result
}

func (idx *Index) collectMembers(typeSym string, seen map[string]struct{}, result *[]Symbol) {
	if _, ok := seen[typeSym]; ok {
		return
	}
	seen[typeSym] = struct{}{}

	for _, m := range idx.ownerMembers[typeSym] {
		*result = append(*result, *m)
	}

	// Recurse into parent types for inherited members.
	for _, parent := range idx.childToParents[typeSym] {
		idx.collectMembers(parent, seen, result)
	}
}

// TypeOfSymbol returns the type symbol for a given symbol.
// For fields: the declared type. For methods: the return type.
func (idx *Index) TypeOfSymbol(sym string) string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.symbolType[sym]
}

// TypeBySimpleName returns type symbols (class/interface/enum) matching the given simple name.
func (idx *Index) TypeBySimpleName(name string) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	name = strings.ToLower(name)
	var result []Symbol
	for _, d := range idx.typeBySimpleName[name] {
		result = append(result, *d)
	}
	return result
}

// DeclTypeOf returns the declared type of a symbol as a TypeExpr (preserving generics).
func (idx *Index) DeclTypeOf(sym string) *TypeExpr {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.symbolDeclType[sym]
}

// ClassTypeParams returns the type parameter symbols for a class (in declaration order).
func (idx *Index) ClassTypeParams(sym string) []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.classTypeParams[sym]
}

// ParentTypesOf returns parent types with their generic arguments.
func (idx *Index) ParentTypesOf(sym string) []*TypeExpr {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.parentTypes[sym]
}

// ParentsOf returns the parent type symbols for a given type symbol.
func (idx *Index) ParentsOf(typeSym string) []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.childToParents[typeSym]
}

func isTypeKind(kind sdb.SymbolInformation_Kind) bool {
	switch kind {
	case sdb.SymbolInformation_CLASS, sdb.SymbolInformation_INTERFACE,
		sdb.SymbolInformation_OBJECT, sdb.SymbolInformation_PACKAGE_OBJECT:
		return true
	default:
		return false
	}
}

func containsPosition(r *sdb.Range, line, character int) bool {
	if int(r.StartLine) > line || int(r.EndLine) < line {
		return false
	}
	if int(r.StartLine) == line && int(r.StartCharacter) > character {
		return false
	}
	if int(r.EndLine) == line && int(r.EndCharacter) <= character {
		return false
	}
	return true
}

func copySymbols(ptrs []*Symbol) []Symbol {
	if len(ptrs) == 0 {
		return nil
	}
	out := make([]Symbol, len(ptrs))
	for i, p := range ptrs {
		out[i] = *p
	}
	return out
}

func copyOccurrences(ptrs []*Occurrence) []Occurrence {
	if len(ptrs) == 0 {
		return nil
	}
	out := make([]Occurrence, len(ptrs))
	for i, p := range ptrs {
		out[i] = *p
	}
	return out
}

func deduplicateSymbols(symbols []Symbol) []Symbol {
	if len(symbols) <= 1 {
		return symbols
	}
	seen := make(map[string]bool)
	var result []Symbol
	for _, s := range symbols {
		if s.Range == nil {
			continue
		}
		if !seen[s.URI] {
			seen[s.URI] = true
			result = append(result, s)
		}
	}
	return result
}

func deduplicateOccurrences(occs []Occurrence) []Occurrence {
	if len(occs) <= 1 {
		return occs
	}
	seen := make(map[string]bool)
	var result []Occurrence
	for _, o := range occs {
		if o.Range == nil {
			continue
		}
		key := fmt.Sprintf("%s:%d:%d-%d:%d", o.URI, o.Range.StartLine, o.Range.StartCharacter, o.Range.EndLine, o.Range.EndCharacter)
		if !seen[key] {
			seen[key] = true
			result = append(result, o)
		}
	}
	return result
}
