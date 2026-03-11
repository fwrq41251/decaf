package lsp

import (
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

// formatHover produces a markdown string for hover display.
func formatHover(sym *index.Symbol) string {
	var b strings.Builder

	b.WriteString("```java\n")

	kindStr := symbolKindLabel(sym.Kind)
	if kindStr != "" {
		b.WriteString(kindStr)
		b.WriteString(" ")
	}

	if sym.Signature != nil {
		b.WriteString(sym.Signature.Label)
	} else {
		b.WriteString(sym.Name)
	}

	b.WriteString("\n```")

	if sym.Symbol != "" {
		b.WriteString("\n\n---\n`")
		b.WriteString(sym.Symbol)
		b.WriteString("`")
	}

	return b.String()
}

func symbolKindLabel(kind sdb.SymbolInformation_Kind) string {
	switch kind {
	case sdb.SymbolInformation_CLASS:
		return "class"
	case sdb.SymbolInformation_INTERFACE:
		return "interface"
	case sdb.SymbolInformation_METHOD:
		return "method"
	case sdb.SymbolInformation_CONSTRUCTOR:
		return "constructor"
	case sdb.SymbolInformation_FIELD:
		return "field"
	case sdb.SymbolInformation_PARAMETER:
		return "param"
	case sdb.SymbolInformation_TYPE_PARAMETER:
		return "type param"
	case sdb.SymbolInformation_PACKAGE:
		return "package"
	default:
		return ""
	}
}

// formatSignatureHelp produces a SignatureInformation for signatureHelp.
func formatSignatureHelp(sym *index.Symbol) *SignatureInformation {
	if sym.Signature == nil || len(sym.Signature.Params) == 0 {
		return nil
	}

	params := make([]ParameterInformation, len(sym.Signature.Params))
	for i, p := range sym.Signature.Params {
		params[i] = ParameterInformation{Label: p}
	}

	return &SignatureInformation{
		Label:      sym.Signature.Label,
		Parameters: params,
	}
}
