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

	// If it's an internal symbol with definitions, return them.
	if len(defs) > 0 {
		result := deduplicateSymbols(copySymbols(defs))
		idx.mu.RUnlock()
		return result
	}

	// It's a possible external symbol. Release RLock before doing potential I/O in resolveExternalSymbol.
	jdkRoot := idx.jdkSourceRoot
	depSources := idx.dependencySources
	idx.mu.RUnlock()

	if jdkRoot != "" || len(depSources) > 0 {
		// Fallback for external symbols (JDK/Dependencies).
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

// CompletionSymbols returns symbols matching the given prefix for completion.
// Results from the same file are prioritized.
func (idx *Index) CompletionSymbols(uri string, prefix string) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	prefix = strings.ToLower(prefix)
	relURI := idx.toRelativeURI(uri)

	var sameFile []Symbol
	var otherFile []Symbol
	for _, defs := range idx.definitions {
		for _, d := range defs {
			if !strings.HasPrefix(strings.ToLower(d.Name), prefix) {
				continue
			}
			if d.URI == relURI {
				sameFile = append(sameFile, *d)
			} else {
				otherFile = append(otherFile, *d)
			}
		}
	}

	result := make([]Symbol, 0, len(sameFile)+len(otherFile))
	result = append(result, sameFile...)
	result = append(result, otherFile...)

	// Cap at 100 results.
	if len(result) > 100 {
		result = result[:100]
	}
	return result
}

// SymbolSignature returns the method signature for the symbol at the given position.
func (idx *Index) SymbolSignature(uri string, line, character int) *Symbol {
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

	// Collect all occurrences (definitions + references).
	for _, fileOccs := range idx.fileOccurrences {
		for _, occ := range fileOccs {
			if occ.Symbol == sym {
				result = append(result, *occ)
			}
		}
	}

	return sym, result
}

// OccurrenceAt returns the SemanticDB occurrence at the given position.
func (idx *Index) OccurrenceAt(uri string, line, character int) *Occurrence {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	occs, ok := idx.fileOccurrences[relURI]
	if !ok {
		return nil
	}
	for _, occ := range occs {
		r := occ.Range
		if r == nil {
			continue
		}
		if containsPosition(r, line, character) {
			return occ
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
