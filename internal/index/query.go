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
	relURI := idx.toRelativeURI(uri)

	sym := idx.lockedSymbolAt(relURI, line, character)
	idx.ensureMembersOf(sym)

	result, jdkRoot, depSources := idx.lockedDefinitionLookup(uri, relURI, sym)
	if result != nil {
		return result
	}

	if jdkRoot != "" || len(depSources) > 0 {
		if ext := idx.resolveExternalSymbol(sym); ext != nil {
			return []Symbol{*ext}
		}
	}
	return nil
}

// lockedSymbolAt returns the symbol string at the given position under RLock.
func (idx *Index) lockedSymbolAt(relURI string, line, character int) string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.symbolAt(relURI, line, character)
}

// lockedDefinitionLookup performs the definition lookup under RLock.
// Returns (result, jdkRoot, depSources). If result is non-nil, the caller should return it directly.
// Otherwise jdkRoot/depSources are provided for external resolution outside the lock.
func (idx *Index) lockedDefinitionLookup(uri, relURI, sym string) ([]Symbol, string, []string) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	idx.logger.Printf("Definition request: uri=%s, relURI=%s, symbolAt=%s", uri, relURI, sym)

	if sym == "" {
		return nil, "", nil
	}

	defs := idx.definitions[sym]

	// If it's an internal symbol with definitions that have source locations, return them.
	if len(defs) > 0 {
		result := deduplicateSymbols(copySymbols(idx, defs))
		// Only return results if at least one symbol has a valid range.
		// ClassIndexer may have added symbols to definitions without ranges
		// for completion/hover support; these should not block external resolution.
		for _, s := range result {
			if !s.Range.IsEmpty() {
				return result, "", nil
			}
		}
	}

	// No definitions with source locations — caller will try external resolution.
	idx.logger.Printf("No local definitions for %s. Probing external: jdkRoot=%s, depSources=%d", sym, idx.jdkSourceRoot, len(idx.dependencySources))
	return nil, idx.jdkSourceRoot, idx.dependencySources
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
	return deduplicateOccurrences(idx.copyOccurrences(refs))
}

// Hover returns the symbol information at the given position (for hover).
func (idx *Index) Hover(uri string, line, character int) *Symbol {
	relURI := idx.toRelativeURI(uri)

	sym := idx.lockedSymbolAt(relURI, line, character)
	idx.ensureMembersOf(sym)

	if result := idx.lockedHoverLookup(sym); result != nil {
		return result
	}

	// Fallback for external symbols (JDK/Dependencies).
	if ext := idx.resolveExternalSymbol(sym); ext != nil {
		return ext
	}
	return nil
}

// lockedHoverLookup performs the hover definition lookup under RLock.
func (idx *Index) lockedHoverLookup(sym string) *Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if sym == "" {
		return nil
	}

	defs := idx.definitions[sym]
	if len(defs) > 0 {
		result := *idx.symbol(defs[0])
		return &result
	}
	return nil
}

// FileSymbols returns all symbol definitions in the given file (for documentSymbol).
func (idx *Index) FileSymbols(uri string) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	return copySymbols(idx, idx.fileSymbols[relURI])
}

// symbolAt returns the SemanticDB symbol string at the given position.
func (idx *Index) symbolAt(uri string, line, character int) string {
	uri = filepath.ToSlash(uri)
	if occ := idx.findOccurrenceAt(uri, line, character); occ != nil {
		return occ.Symbol
	}
	return ""
}

// AllSymbols returns all indexed symbol definitions (for completion, etc).
func (idx *Index) AllSymbols() []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []Symbol
	for _, defs := range idx.definitions {
		for _, id := range defs {
			result = append(result, *idx.symbol(id))
		}
	}
	for sym, info := range idx.externalTypeInfo {
		result = append(result, Symbol{
			Name:   info.name,
			Symbol: sym,
			Kind:   info.kind,
		})
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
		for _, id := range defs {
			d := idx.symbol(id)
			if strings.Contains(strings.ToLower(d.Name), query) {
				result = append(result, *d)
			}
		}
	}
	for sym, info := range idx.externalTypeInfo {
		if strings.Contains(strings.ToLower(info.name), query) {
			result = append(result, Symbol{
				Name:   info.name,
				Symbol: sym,
				Kind:   info.kind,
			})
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
	for name, ids := range idx.typeBySimpleName {
		if !fuzzyMatchLower(name, query) {
			continue
		}
		for _, id := range ids {
			d := idx.symbol(id)
			if d.URI == relURI {
				s := *d
				s.SameFile = true
				sameFileTypes = append(sameFileTypes, s)
			} else {
				otherTypes = append(otherTypes, *d)
			}
		}
	}
	for name, ids := range idx.memberBySimpleName {
		if !fuzzyMatchLower(name, query) {
			continue
		}
		for _, id := range ids {
			d := idx.symbol(id)
			if d.URI == relURI {
				s := *d
				s.SameFile = true
				sameFileOther = append(sameFileOther, s)
			} else {
				otherOther = append(otherOther, *d)
			}
		}
	}
	for name, syms := range idx.externalTypesBySimpleName {
		if !fuzzyMatchLower(name, query) {
			continue
		}
		for _, sym := range syms {
			info := idx.externalTypeInfo[sym]
			otherTypes = append(otherTypes, Symbol{
				Name:   info.name,
				Symbol: sym,
				Kind:   info.kind,
			})
		}
	}

	// Priority: same-file types > other types > same-file members > other members.
	// Within each bucket, sort by an explicit match score:
	// exact > prefix > camel/word-start > substring > fuzzy subsequence.
	sortByQueryPriority := func(s []Symbol) {
		sort.SliceStable(s, func(i, j int) bool {
			iScore := CompletionMatchScore(s[i].Name, query)
			jScore := CompletionMatchScore(s[j].Name, query)
			if iScore != jScore {
				return iScore < jScore
			}
			if len(s[i].Name) != len(s[j].Name) {
				return len(s[i].Name) < len(s[j].Name)
			}
			if s[i].Name != s[j].Name {
				return s[i].Name < s[j].Name
			}
			return s[i].Symbol < s[j].Symbol
		})
	}
	sortByQueryPriority(sameFileTypes)
	sortByQueryPriority(otherTypes)
	sortByQueryPriority(sameFileOther)
	sortByQueryPriority(otherOther)
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
	return fuzzyMatchLower(strings.ToLower(name), strings.ToLower(query))
}

const (
	MatchExact = iota
	MatchPrefix
	MatchWordStart
	MatchSubstring
	MatchFuzzy
	MatchNone
)

// CompletionMatchScore ranks how well name matches query (case-insensitive).
// Lower is better: MatchExact < MatchPrefix < MatchWordStart < MatchSubstring < MatchFuzzy < MatchNone.
func CompletionMatchScore(name, query string) int {
	if query == "" {
		return MatchExact
	}
	lowerName := strings.ToLower(name)
	if lowerName == query {
		return MatchExact
	}
	if strings.HasPrefix(lowerName, query) {
		return MatchPrefix
	}
	if camelOrWordStartMatch(name, query) {
		return MatchWordStart
	}
	if strings.Contains(lowerName, query) {
		return MatchSubstring
	}
	if fuzzyMatchLower(lowerName, query) {
		return MatchFuzzy
	}
	return MatchNone
}

// fuzzyMatchLower is like FuzzyMatch but assumes both arguments are already lowercase.
func fuzzyMatchLower(name, query string) bool {
	if query == "" {
		return true
	}
	ni, qi := 0, 0
	for ni < len(name) && qi < len(query) {
		if name[ni] == query[qi] {
			qi++
		}
		ni++
	}
	return qi == len(query)
}

func camelOrWordStartMatch(name, query string) bool {
	if query == "" {
		return true
	}
	initials := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		if isWordStart(name, i) {
			c := name[i]
			if 'A' <= c && c <= 'Z' {
				c = c - 'A' + 'a'
			}
			initials = append(initials, c)
		}
	}
	return fuzzyMatchLower(string(initials), query)
}

func isWordStart(name string, i int) bool {
	if i < 0 || i >= len(name) {
		return false
	}
	if i == 0 {
		return true
	}
	prev := name[i-1]
	curr := name[i]
	if prev == '_' || prev == '$' || prev == '.' || prev == '/' {
		return true
	}
	return ('a' <= prev && prev <= 'z') && ('A' <= curr && curr <= 'Z')
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
	relURI := idx.toRelativeURI(uri)

	sym := idx.lockedSymbolAt(relURI, line, character)
	idx.ensureMembersOf(sym)

	if result := idx.lockedSignaturesLookup(sym); result != nil {
		return result
	}

	// Fallback for external symbols (JDK/Dependencies).
	if ext := idx.resolveExternalSymbol(sym); ext != nil {
		return []Symbol{*ext}
	}
	return nil
}

// lockedSignaturesLookup performs the signatures lookup under RLock.
func (idx *Index) lockedSignaturesLookup(sym string) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if sym == "" {
		return nil
	}

	defs := idx.definitions[sym]
	if len(defs) > 0 {
		result := make([]Symbol, len(defs))
		for i, id := range defs {
			result[i] = *idx.symbol(id)
		}
		return result
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
	for _, id := range idx.definitions[sym] {
		d := idx.symbol(id)
		if !d.Range.IsEmpty() {
			result = append(result, Occurrence{
				Symbol: d.Symbol,
				Role:   sdb.SymbolOccurrence_DEFINITION,
				URI:    d.URI,
				Range:  d.Range,
			})
		}
	}

	// Collect reference occurrences.
	for _, occID := range idx.references[sym] {
		result = append(result, idx.occurrenceByID(occID))
	}

	return sym, result
}

// OccurrenceAt returns the SemanticDB occurrence at the given position.
func (idx *Index) OccurrenceAt(uri string, line, character int) *Occurrence {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	return idx.findOccurrenceAt(relURI, line, character)
}

// findOccurrenceAt returns the occurrence at the given position, or nil if none.
// Occurrences are sorted by start position, so we use binary search to find
// the neighborhood and then check containment.
func (idx *Index) findOccurrenceAt(uri string, line, character int) *Occurrence {
	occs, ok := idx.fileOccurrences[uri]
	if !ok {
		return nil
	}

	line32, char32 := int32(line), int32(character)

	// Binary search: find the first occurrence that starts after (line, character).
	i := sort.Search(len(occs), func(i int) bool {
		r := idx.occurrence(occs[i]).Range
		if r.IsEmpty() {
			return false
		}
		if r.StartLine != line32 {
			return r.StartLine > line32
		}
		return r.StartCharacter > char32
	})

	// Walk backwards from i to find an occurrence that contains the position.
	for j := i - 1; j >= 0; j-- {
		r := idx.occurrence(occs[j]).Range
		if r.IsEmpty() {
			continue
		}
		if r.EndLine < line32 {
			break
		}
		if containsPosition(r, line, character) {
			occ := idx.occurrenceByID(occs[j])
			return &occ
		}
	}
	return nil
}

// AllFileOccurrences returns all occurrences in the given file.
func (idx *Index) AllFileOccurrences(uri string) []Occurrence {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	return idx.copyOccurrences(idx.fileOccurrences[relURI])
}

// SymbolDefinition returns the definition for a SemanticDB symbol string.
// This is useful for resolving a symbol's fully-qualified type without a position.
func (idx *Index) SymbolDefinition(sym string) *Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	defs := idx.definitions[sym]
	if len(defs) > 0 {
		s := *idx.symbol(defs[0])
		return &s
	}
	if info, ok := idx.externalTypeInfo[sym]; ok {
		return &Symbol{
			Name:   info.name,
			Symbol: sym,
			Kind:   info.kind,
		}
	}
	return nil
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
	for _, occID := range idx.fileOccurrences[relURI] {
		occ := idx.occurrence(occID)
		if idx.stringByID(occ.SymbolID) == sym {
			result = append(result, idx.occurrenceFromStored(occ))
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
			for _, id := range defs {
				result = append(result, *idx.symbol(id))
			}
		}
	}
	return result
}

// DirectMembersOfType returns only the direct member symbols of a type (no inherited members).
func (idx *Index) DirectMembersOfType(typeSym string) []Symbol {
	idx.ensureMembersIndexed(typeSym)
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	members := idx.ownerMembers[typeSym]
	if len(members) == 0 {
		return nil
	}
	result := make([]Symbol, len(members))
	for i, id := range members {
		result[i] = *idx.symbol(id)
	}
	return result
}

// MembersOfType returns all direct member symbols of a type,
// plus inherited members from parent types.
func (idx *Index) MembersOfType(typeSym string) []Symbol {
	idx.ensureHierarchyIndexed(typeSym)
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

	for _, id := range idx.ownerMembers[typeSym] {
		*result = append(*result, *idx.symbol(id))
	}

	// Recurse into parent types for inherited members.
	for _, parent := range idx.childToParents[typeSym] {
		idx.collectMembers(parent, seen, result)
	}
}

// ensureHierarchyIndexed recursively ensures a class and its parents are indexed.
// It is safe to call because it releases the lock between levels.
func (idx *Index) ensureHierarchyIndexed(typeSym string) {
	seen := make(map[string]struct{})
	idx.ensureHierarchyIndexedRec(typeSym, seen)
}

func (idx *Index) ensureHierarchyIndexedRec(typeSym string, seen map[string]struct{}) {
	if _, ok := seen[typeSym]; ok {
		return
	}
	seen[typeSym] = struct{}{}

	idx.ensureMembersIndexed(typeSym)

	idx.mu.RLock()
	parents := idx.childToParents[typeSym]
	idx.mu.RUnlock()

	for _, p := range parents {
		idx.ensureHierarchyIndexedRec(p, seen)
	}
}

// TypeOfSymbol returns the type symbol for a given symbol.
// For fields: the declared type. For methods: the return type.
func (idx *Index) TypeOfSymbol(sym string) string {
	idx.ensureMembersOf(sym)
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.symbolType[sym]
}

// TypeBySimpleName returns type symbols (class/interface/enum) matching the given simple name.
func (idx *Index) TypeBySimpleName(name string) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	lowerName := strings.ToLower(name)
	var result []Symbol
	seen := make(map[string]struct{})
	for _, id := range idx.typeBySimpleName[lowerName] {
		d := idx.symbol(id)
		result = append(result, *d)
		seen[d.Symbol] = struct{}{}
	}
	for _, sym := range idx.externalTypesBySimpleName[lowerName] {
		if _, ok := seen[sym]; ok {
			continue
		}
		info, ok := idx.externalTypeInfo[sym]
		if !ok {
			continue
		}
		result = append(result, Symbol{
			Name:   info.name,
			Symbol: sym,
			Kind:   info.kind,
		})
	}
	return result
}

// DeclTypeOf returns the declared type of a symbol as a TypeExpr (preserving generics).
func (idx *Index) DeclTypeOf(sym string) *TypeExpr {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.symbolDeclType[sym]
}

// DeclParamTypesOf returns the declared parameter types of a method as TypeExprs (preserving generics).
func (idx *Index) DeclParamTypesOf(sym string) []*TypeExpr {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.symbolDeclParamTypes[sym]
}

// ClassTypeParams returns the type parameter symbols for a class (in declaration order).
func (idx *Index) ClassTypeParams(sym string) []string {
	idx.ensureClassSkeletonIndexed(sym)
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.classTypeParams[sym]
}

// ParentTypesOf returns parent types with their generic arguments.
func (idx *Index) ParentTypesOf(sym string) []*TypeExpr {
	idx.ensureClassSkeletonIndexed(sym)
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.parentTypes[sym]
}

// ParentsOf returns the parent type symbols for a given type symbol.
func (idx *Index) ParentsOf(typeSym string) []string {
	idx.ensureClassSkeletonIndexed(typeSym)
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.childToParents[typeSym]
}

// IsAssignableTo returns true if candidateSym is the same type as or a subtype
// of targetSym. It walks the type hierarchy (parents/interfaces) up to a
// bounded depth to avoid runaway traversal.
func (idx *Index) IsAssignableTo(candidateSym, targetSym string) bool {
	if candidateSym == "" || targetSym == "" {
		return false
	}
	if candidateSym == targetSym {
		return true
	}
	visited := make(map[string]bool)
	queue := []string{candidateSym}
	for len(queue) > 0 && len(visited) < 50 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur] {
			continue
		}
		visited[cur] = true
		for _, parent := range idx.ParentsOf(cur) {
			if parent == targetSym {
				return true
			}
			if !visited[parent] {
				queue = append(queue, parent)
			}
		}
	}
	return false
}

// SymbolReferences returns all references for a given symbol string.
func (idx *Index) SymbolReferences(sym string) []Occurrence {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return deduplicateOccurrences(idx.copyOccurrences(idx.references[sym]))
}

// Implementors returns the direct child type symbols that extend/implement the given type.
func (idx *Index) Implementors(typeSym string) []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	impls := idx.implementors[typeSym]
	if len(impls) == 0 {
		return nil
	}
	result := make([]string, len(impls))
	copy(result, impls)
	return result
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

// ensureMembersOf triggers lazy indexing of the class containing the given symbol.
func (idx *Index) ensureMembersOf(sym string) {
	if owner := extractOwner(sym); owner != "" {
		idx.ensureMembersIndexed(owner)
	} else if strings.HasSuffix(sym, "#") {
		idx.ensureMembersIndexed(sym)
	}
}

func containsPosition(r Range, line, character int) bool {
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

func copySymbols(idx *Index, ids []SymbolID) []Symbol {
	if len(ids) == 0 {
		return nil
	}
	out := make([]Symbol, len(ids))
	for i, id := range ids {
		out[i] = *idx.symbol(id)
	}
	return out
}

func (idx *Index) copyOccurrences(occIDs []OccurrenceID) []Occurrence {
	if len(occIDs) == 0 {
		return nil
	}
	out := make([]Occurrence, len(occIDs))
	for i, occID := range occIDs {
		out[i] = idx.occurrenceByID(occID)
	}
	return out
}

func (idx *Index) occurrenceFromStored(occ storedOccurrence) Occurrence {
	return Occurrence{
		Symbol: idx.stringByID(occ.SymbolID),
		Role:   occ.Role,
		URI:    idx.stringByID(occ.URIID),
		Range:  occ.Range,
	}
}

func (idx *Index) addOccurrence(occ storedOccurrence) OccurrenceID {
	idx.occurrences = append(idx.occurrences, occ)
	return OccurrenceID(len(idx.occurrences))
}

func (idx *Index) occurrence(id OccurrenceID) storedOccurrence {
	if id == 0 || int(id) > len(idx.occurrences) {
		return storedOccurrence{}
	}
	return idx.occurrences[int(id)-1]
}

func (idx *Index) occurrenceByID(id OccurrenceID) Occurrence {
	return idx.occurrenceFromStored(idx.occurrence(id))
}

func deduplicateSymbols(symbols []Symbol) []Symbol {
	if len(symbols) <= 1 {
		return symbols
	}
	seen := make(map[string]bool)
	var result []Symbol
	for _, s := range symbols {
		if s.Range.IsEmpty() {
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
		if o.Range.IsEmpty() {
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
