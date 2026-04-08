package index

import (
	"strings"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

// SetClassTypeParamsForTest sets classTypeParams for testing. Test-only.
func (idx *Index) SetClassTypeParamsForTest(sym string, params []string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.classTypeParams[sym] = params
}

// SetParentTypesForTest sets parentTypes for testing. Test-only.
func (idx *Index) SetParentTypesForTest(sym string, parents []*TypeExpr) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.parentTypes[sym] = parents
}

// AddDefinitionForTest adds a minimal definition for testing. Test-only.
func (idx *Index) AddDefinitionForTest(sym, name string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	id := idx.addSymbol(Symbol{
		Name:   name,
		Symbol: sym,
		Kind:   sdb.SymbolInformation_CLASS,
	})
	idx.definitions[sym] = append(idx.definitions[sym], id)
	idx.typeBySimpleName[strings.ToLower(name)] = append(idx.typeBySimpleName[strings.ToLower(name)], id)
}

// AddMemberForTest adds a minimal non-type definition for testing. Test-only.
func (idx *Index) AddMemberForTest(sym, name, uri string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	id := idx.addSymbol(Symbol{
		Name:   name,
		Symbol: sym,
		Kind:   sdb.SymbolInformation_METHOD,
		URI:    uri,
	})
	idx.definitions[sym] = append(idx.definitions[sym], id)
	idx.memberBySimpleName[strings.ToLower(name)] = append(idx.memberBySimpleName[strings.ToLower(name)], id)
}

// AddSymbolForTest appends a symbol to the central symbol store. Test-only.
func (idx *Index) AddSymbolForTest(s Symbol) SymbolID {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.addSymbol(s)
}

// SymbolForTest returns a mutable pointer to a stored symbol. Test-only.
func (idx *Index) SymbolForTest(id SymbolID) *Symbol {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.symbol(id)
}
