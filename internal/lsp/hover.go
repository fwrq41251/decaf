package lsp

import (
	"fmt"
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

	// Format signature if available.
	if sym.Signature != nil {
		b.WriteString(formatSignature(sym.Name, sym.Signature))
	} else {
		b.WriteString(sym.Name)
	}

	b.WriteString("\n```")

	// Add symbol path as detail.
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

func formatSignature(name string, sig *sdb.Signature) string {
	switch s := sig.SealedValue.(type) {
	case *sdb.Signature_ClassSignature:
		return formatClassSig(name, s.ClassSignature)
	case *sdb.Signature_MethodSignature:
		return formatMethodSig(name, s.MethodSignature)
	case *sdb.Signature_ValueSignature:
		return fmt.Sprintf("%s: %s", name, formatType(s.ValueSignature.Tpe))
	case *sdb.Signature_TypeSignature:
		return name
	default:
		return name
	}
}

func formatClassSig(name string, sig *sdb.ClassSignature) string {
	var b strings.Builder
	b.WriteString(name)

	if len(sig.Parents) > 0 {
		first := true
		for _, p := range sig.Parents {
			typeName := formatType(p)
			if typeName == "Object" || typeName == "java/lang/Object#" {
				continue
			}
			if first {
				b.WriteString(" extends ")
				first = false
			} else {
				b.WriteString(", ")
			}
			b.WriteString(typeName)
		}
	}

	return b.String()
}

func formatMethodSig(name string, sig *sdb.MethodSignature) string {
	var b strings.Builder

	// Return type.
	retType := formatType(sig.ReturnType)

	b.WriteString(retType)
	b.WriteString(" ")
	b.WriteString(name)
	b.WriteString("(")

	// Parameters.
	first := true
	for _, paramList := range sig.ParameterLists {
		for _, param := range paramList.Hardlinks {
			if !first {
				b.WriteString(", ")
			}
			first = false
			paramType := ""
			if vs, ok := param.Signature.SealedValue.(*sdb.Signature_ValueSignature); ok {
				paramType = formatType(vs.ValueSignature.Tpe)
			}
			b.WriteString(paramType)
			b.WriteString(" ")
			b.WriteString(param.DisplayName)
		}
	}

	b.WriteString(")")
	return b.String()
}

// formatType formats a SemanticDB Type into a readable string.
func formatType(t *sdb.Type) string {
	if t == nil {
		return "void"
	}

	switch v := t.SealedValue.(type) {
	case *sdb.Type_TypeRef:
		name := simplifySymbol(v.TypeRef.Symbol)
		if len(v.TypeRef.TypeArguments) > 0 {
			var args []string
			for _, arg := range v.TypeRef.TypeArguments {
				args = append(args, formatType(arg))
			}
			return fmt.Sprintf("%s<%s>", name, strings.Join(args, ", "))
		}
		return name
	case *sdb.Type_SingleType:
		return simplifySymbol(v.SingleType.Symbol)
	default:
		return "?"
	}
}

// formatSignatureHelp produces a SignatureInformation for signatureHelp.
func formatSignatureHelp(sym *index.Symbol) *SignatureInformation {
	if sym.Signature == nil {
		return nil
	}
	ms, ok := sym.Signature.SealedValue.(*sdb.Signature_MethodSignature)
	if !ok {
		return nil
	}

	label := formatMethodSig(sym.Name, ms.MethodSignature)
	var params []ParameterInformation
	for _, paramList := range ms.MethodSignature.ParameterLists {
		for _, param := range paramList.Hardlinks {
			paramType := ""
			if vs, ok := param.Signature.SealedValue.(*sdb.Signature_ValueSignature); ok {
				paramType = formatType(vs.ValueSignature.Tpe)
			}
			paramLabel := paramType + " " + param.DisplayName
			params = append(params, ParameterInformation{Label: paramLabel})
		}
	}

	return &SignatureInformation{
		Label:      label,
		Parameters: params,
	}
}

// simplifySymbol turns "java/lang/String#" into "String".
func simplifySymbol(sym string) string {
	sym = strings.TrimSuffix(sym, "#")
	sym = strings.TrimSuffix(sym, ".")
	if idx := strings.LastIndex(sym, "/"); idx >= 0 {
		sym = sym[idx+1:]
	}
	return sym
}
