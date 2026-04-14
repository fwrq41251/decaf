package lsp

import (
	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

func findClassSymbol(fileURI string, className string, idx *index.Index) (index.Symbol, bool) {
	for _, sym := range idx.FileSymbols(fileURI) {
		if sym.Name == className && sym.Kind == sdb.SymbolInformation_CLASS {
			return sym, true
		}
	}
	return index.Symbol{}, false
}
