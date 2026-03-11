package index

import (
	"fmt"
	"strings"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

// buildSignatureInfo converts a protobuf Signature into a lightweight SignatureInfo.
func buildSignatureInfo(name string, sig *sdb.Signature) *SignatureInfo {
	if sig == nil {
		return nil
	}

	switch s := sig.SealedValue.(type) {
	case *sdb.Signature_MethodSignature:
		return buildMethodSignatureInfo(name, s.MethodSignature)
	case *sdb.Signature_ClassSignature:
		return &SignatureInfo{Label: formatClassSig(name, s.ClassSignature)}
	case *sdb.Signature_ValueSignature:
		return &SignatureInfo{Label: fmt.Sprintf("%s: %s", name, formatType(s.ValueSignature.Tpe))}
	case *sdb.Signature_TypeSignature:
		return &SignatureInfo{Label: name}
	default:
		return nil
	}
}

func buildMethodSignatureInfo(name string, sig *sdb.MethodSignature) *SignatureInfo {
	var params []string
	for _, paramList := range sig.ParameterLists {
		for _, param := range paramList.Hardlinks {
			paramType := ""
			if vs, ok := param.Signature.SealedValue.(*sdb.Signature_ValueSignature); ok {
				paramType = formatType(vs.ValueSignature.Tpe)
			}
			params = append(params, paramType+" "+param.DisplayName)
		}
	}

	retType := formatType(sig.ReturnType)
	var b strings.Builder
	b.WriteString(retType)
	b.WriteString(" ")
	b.WriteString(name)
	b.WriteString("(")
	b.WriteString(strings.Join(params, ", "))
	b.WriteString(")")

	return &SignatureInfo{
		Label:  b.String(),
		Params: params,
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

func simplifySymbol(sym string) string {
	sym = strings.TrimSuffix(sym, "#")
	sym = strings.TrimSuffix(sym, ".")
	if idx := strings.LastIndex(sym, "/"); idx >= 0 {
		sym = sym[idx+1:]
	}
	return sym
}
