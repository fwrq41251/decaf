package lsp

import (
	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

// buildDocumentSymbols converts indexed symbols into an LSP DocumentSymbol tree.
// Classes/interfaces/enums become top-level, methods/fields/constructors become children.
func buildDocumentSymbols(symbols []index.Symbol) []DocumentSymbol {
	// Separate top-level types from members.
	type container struct {
		sym      DocumentSymbol
		children []DocumentSymbol
	}

	containers := make(map[string]*container) // keyed by symbol string
	var topLevel []string                     // ordered keys
	var orphans []DocumentSymbol

	for _, s := range symbols {
		if s.Range == nil {
			continue
		}

		ds := DocumentSymbol{
			Name:           s.Name,
			Kind:           sdbKindToLSP(s.Kind),
			Range:          sdbRangeToLSP(s.Range),
			SelectionRange: sdbRangeToLSP(s.Range),
		}

		if isContainerKind(s.Kind) {
			c := &container{sym: ds}
			containers[s.Symbol] = c
			topLevel = append(topLevel, s.Symbol)
		} else {
			// Try to find parent container by checking if the symbol string
			// is a prefix match. E.g., "com/example/Foo#bar()." belongs to "com/example/Foo#".
			placed := false
			for key, c := range containers {
				if len(s.Symbol) > len(key) && s.Symbol[:len(key)] == key {
					c.children = append(c.children, ds)
					placed = true
					break
				}
			}
			if !placed {
				orphans = append(orphans, ds)
			}
		}
	}

	// Assemble result.
	result := make([]DocumentSymbol, 0, len(topLevel)+len(orphans))
	for _, key := range topLevel {
		c := containers[key]
		c.sym.Children = c.children
		result = append(result, c.sym)
	}
	result = append(result, orphans...)

	return result
}

func isContainerKind(kind sdb.SymbolInformation_Kind) bool {
	switch kind {
	case sdb.SymbolInformation_CLASS,
		sdb.SymbolInformation_INTERFACE,
		sdb.SymbolInformation_OBJECT,
		sdb.SymbolInformation_PACKAGE_OBJECT:
		return true
	default:
		return false
	}
}

func sdbKindToLSP(kind sdb.SymbolInformation_Kind) int {
	switch kind {
	case sdb.SymbolInformation_CLASS:
		return SymbolKindClass
	case sdb.SymbolInformation_INTERFACE:
		return SymbolKindInterface
	case sdb.SymbolInformation_METHOD:
		return SymbolKindMethod
	case sdb.SymbolInformation_CONSTRUCTOR:
		return SymbolKindConstructor
	case sdb.SymbolInformation_FIELD:
		return SymbolKindField
	case sdb.SymbolInformation_PARAMETER:
		return SymbolKindVariable
	case sdb.SymbolInformation_PACKAGE:
		return SymbolKindPackage
	case sdb.SymbolInformation_OBJECT:
		return SymbolKindClass
	default:
		return SymbolKindVariable
	}
}

func sdbKindToCompletionKind(kind sdb.SymbolInformation_Kind) int {
	switch kind {
	case sdb.SymbolInformation_CLASS:
		return CompletionKindClass
	case sdb.SymbolInformation_INTERFACE:
		return CompletionKindInterface
	case sdb.SymbolInformation_METHOD:
		return CompletionKindMethod
	case sdb.SymbolInformation_CONSTRUCTOR:
		return CompletionKindConstructor
	case sdb.SymbolInformation_FIELD:
		return CompletionKindField
	case sdb.SymbolInformation_PARAMETER:
		return CompletionKindVariable
	case sdb.SymbolInformation_PACKAGE:
		return CompletionKindModule
	case sdb.SymbolInformation_OBJECT:
		return CompletionKindClass
	default:
		return CompletionKindText
	}
}

func sdbRangeToLSP(r *sdb.Range) Range {
	return Range{
		Start: Position{Line: int(r.StartLine), Character: int(r.StartCharacter)},
		End:   Position{Line: int(r.EndLine), Character: int(r.EndCharacter)},
	}
}
