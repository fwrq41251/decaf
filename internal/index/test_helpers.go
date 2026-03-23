package index

import sdb "github.com/fwrq41251/decaf/internal/semanticdb"

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
	s := &Symbol{
		Name:   name,
		Symbol: sym,
		Kind:   sdb.SymbolInformation_CLASS,
	}
	idx.definitions[sym] = append(idx.definitions[sym], s)
	idx.typeBySimpleName[name] = append(idx.typeBySimpleName[name], s)
}
