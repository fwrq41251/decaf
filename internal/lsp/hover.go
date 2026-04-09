package lsp

import (
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

// formatHover produces a markdown string for hover display.
func formatHover(sym *index.Symbol, idx *index.Index) string {
	var b strings.Builder

	b.WriteString("```java\n")
	b.WriteString(hoverDeclaration(sym))
	b.WriteString("\n```")

	if metadata := hoverMetadata(sym, idx); metadata != "" {
		b.WriteString("\n\n")
		b.WriteString(metadata)
	}

	if sym.Doc != "" {
		b.WriteString("\n\n---\n")
		b.WriteString(formatJavadoc(sym.Doc))
	}

	return b.String()
}

func hoverDeclaration(sym *index.Symbol) string {
	if sym == nil {
		return ""
	}

	var parts []string
	if visibility := sym.Visibility.String(); visibility != "" {
		parts = append(parts, visibility)
	}
	if sym.IsStatic {
		parts = append(parts, "static")
	}
	if sym.IsAbstract && sym.Kind != sdb.SymbolInformation_INTERFACE {
		parts = append(parts, "abstract")
	}
	if sym.IsFinal {
		parts = append(parts, "final")
	}
	if sym.IsSealed {
		parts = append(parts, "sealed")
	}
	if kind := hoverDeclarationKind(sym.Kind); kind != "" {
		parts = append(parts, kind)
	}

	if sym.Signature != nil && sym.Signature.Label != "" {
		parts = append(parts, sym.Signature.Label)
	} else {
		parts = append(parts, sym.Name)
	}
	return strings.Join(parts, " ")
}

func hoverDeclarationKind(kind sdb.SymbolInformation_Kind) string {
	switch kind {
	case sdb.SymbolInformation_CLASS:
		return "class"
	case sdb.SymbolInformation_INTERFACE:
		return "interface"
	case sdb.SymbolInformation_PACKAGE:
		return "package"
	default:
		return ""
	}
}

func hoverMetadata(sym *index.Symbol, idx *index.Index) string {
	var lines []string

	if owner := hoverOwnerDisplay(sym); owner != "" {
		lines = append(lines, "**Owner:** `"+owner+"`")
	} else if pkg := hoverPackageDisplay(sym.Symbol); pkg != "" {
		lines = append(lines, "**Package:** `"+pkg+"`")
	}
	if typeParams := hoverTypeParamsDisplay(sym, idx); typeParams != "" {
		lines = append(lines, "**Type Parameters:** `"+typeParams+"`")
	}
	if inheritance := hoverInheritanceDisplay(sym, idx); len(inheritance) > 0 {
		lines = append(lines, inheritance...)
	}

	return strings.Join(lines, "\n")
}

func hoverOwnerDisplay(sym *index.Symbol) string {
	if sym == nil {
		return ""
	}
	owner := ownerSymbol(sym.Symbol)
	if owner == "" {
		return ""
	}
	if fqn := fqnFromSymbol(owner); fqn != "" {
		return fqn
	}
	return simpleNameFromSymbol(owner)
}

func hoverPackageDisplay(sym string) string {
	base := sym
	if owner := ownerSymbol(sym); owner != "" {
		base = owner
	}
	fqn := fqnFromSymbol(base)
	if fqn == "" {
		return ""
	}
	return packageFromFQN(fqn)
}

func hoverTypeParamsDisplay(sym *index.Symbol, idx *index.Index) string {
	if idx == nil || sym == nil {
		return ""
	}
	switch sym.Kind {
	case sdb.SymbolInformation_CLASS, sdb.SymbolInformation_INTERFACE:
	default:
		return ""
	}

	params := idx.ClassTypeParams(sym.Symbol)
	if len(params) == 0 {
		return ""
	}

	display := make([]string, 0, len(params))
	for _, param := range params {
		name := hoverTypeSymDisplay(param)
		if name == "" {
			continue
		}
		display = append(display, name)
	}
	if len(display) == 0 {
		return ""
	}
	return strings.Join(display, ", ")
}

func hoverInheritanceDisplay(sym *index.Symbol, idx *index.Index) []string {
	if idx == nil || sym == nil {
		return nil
	}
	switch sym.Kind {
	case sdb.SymbolInformation_CLASS, sdb.SymbolInformation_INTERFACE:
	default:
		return nil
	}

	parents := idx.ParentTypesOf(sym.Symbol)
	if len(parents) == 0 {
		return nil
	}

	var display []string
	for _, parent := range parents {
		text := hoverTypeExprDisplay(parent)
		if text == "" || text == "Object" {
			continue
		}
		display = append(display, text)
	}
	if len(display) == 0 {
		return nil
	}

	if sym.Kind == sdb.SymbolInformation_INTERFACE {
		return []string{"**Extends:** `" + strings.Join(display, "`, `") + "`"}
	}

	lines := []string{"**Extends:** `" + display[0] + "`"}
	if len(display) > 1 {
		lines = append(lines, "**Implements:** `"+strings.Join(display[1:], "`, `")+"`")
	}
	return lines
}

func hoverTypeExprDisplay(te *index.TypeExpr) string {
	if te == nil {
		return ""
	}
	base := hoverTypeSymDisplay(te.Sym)
	if len(te.Args) == 0 {
		return base
	}
	var sb strings.Builder
	sb.WriteString(base)
	sb.WriteByte('<')
	for i, arg := range te.Args {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(hoverTypeExprDisplay(arg))
	}
	sb.WriteByte('>')
	return sb.String()
}

func hoverTypeSymDisplay(sym string) string {
	if start := strings.LastIndexByte(sym, '['); start >= 0 {
		if end := strings.LastIndexByte(sym, ']'); end > start+1 {
			return sym[start+1 : end]
		}
	}
	sym = strings.TrimSuffix(sym, "#")
	sym = strings.TrimSuffix(sym, ".")
	if idx := strings.LastIndexAny(sym, "/."); idx >= 0 {
		return sym[idx+1:]
	}
	return sym
}

func ownerSymbol(sym string) string {
	lastHash := strings.LastIndex(sym, "#")
	if lastHash == -1 || lastHash == len(sym)-1 {
		return ""
	}
	return sym[:lastHash+1]
}

// formatJavadoc converts raw Javadoc text into readable Markdown.
// Converts @param, @return, @throws/@exception, @since, @see, @deprecated tags
// into structured Markdown sections.
func formatJavadoc(doc string) string {
	lines := strings.Split(doc, "\n")

	var description []string
	var params []string
	var returns []string
	var throws []string
	var other []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "* ")
		trimmed = strings.TrimPrefix(trimmed, "*")
		trimmed = strings.TrimSpace(trimmed)

		switch {
		case strings.HasPrefix(trimmed, "@param "):
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "@param "))
			parts := strings.SplitN(rest, " ", 2)
			if len(parts) == 2 {
				params = append(params, "- `"+parts[0]+"` — "+strings.TrimSpace(parts[1]))
			} else if len(parts) == 1 {
				params = append(params, "- `"+parts[0]+"`")
			}
		case strings.HasPrefix(trimmed, "@return "):
			returns = append(returns, strings.TrimSpace(strings.TrimPrefix(trimmed, "@return ")))
		case strings.HasPrefix(trimmed, "@returns "):
			returns = append(returns, strings.TrimSpace(strings.TrimPrefix(trimmed, "@returns ")))
		case strings.HasPrefix(trimmed, "@throws "),
			strings.HasPrefix(trimmed, "@exception "):
			rest := trimmed
			rest = strings.TrimPrefix(rest, "@throws ")
			rest = strings.TrimPrefix(rest, "@exception ")
			rest = strings.TrimSpace(rest)
			parts := strings.SplitN(rest, " ", 2)
			if len(parts) == 2 {
				throws = append(throws, "- `"+parts[0]+"` — "+strings.TrimSpace(parts[1]))
			} else if len(parts) == 1 {
				throws = append(throws, "- `"+parts[0]+"`")
			}
		case strings.HasPrefix(trimmed, "@since "):
			other = append(other, "**Since:** "+strings.TrimSpace(strings.TrimPrefix(trimmed, "@since ")))
		case strings.HasPrefix(trimmed, "@see "):
			other = append(other, "**See:** `"+strings.TrimSpace(strings.TrimPrefix(trimmed, "@see "))+"`")
		case strings.HasPrefix(trimmed, "@deprecated"):
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "@deprecated"))
			if rest != "" {
				other = append(other, "**Deprecated:** "+rest)
			} else {
				other = append(other, "**Deprecated**")
			}
		case strings.HasPrefix(trimmed, "@"):
			other = append(other, trimmed)
		default:
			description = append(description, trimmed)
		}
	}

	var out strings.Builder
	out.WriteString(strings.TrimSpace(strings.Join(description, "\n")))

	if len(params) > 0 {
		out.WriteString("\n\n**Parameters:**\n")
		out.WriteString(strings.Join(params, "\n"))
	}
	if len(returns) > 0 {
		out.WriteString("\n\n**Returns:** ")
		out.WriteString(strings.Join(returns, " "))
	}
	if len(throws) > 0 {
		out.WriteString("\n\n**Throws:**\n")
		out.WriteString(strings.Join(throws, "\n"))
	}
	for _, o := range other {
		out.WriteString("\n\n")
		out.WriteString(o)
	}

	return strings.TrimSpace(out.String())
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
	if sym.Signature == nil || !sym.Signature.HasParams {
		return nil
	}

	if len(sym.Signature.Params) == 0 {
		parsed := sym.Signature.ParseParams()
		if len(parsed) == 0 {
			return nil
		}

		params := make([]ParameterInformation, len(parsed))
		for i, p := range parsed {
			params[i] = ParameterInformation{Label: p}
		}

		return &SignatureInformation{
			Label:      sym.Signature.Label,
			Parameters: params,
		}
	}

	params := make([]ParameterInformation, 0, len(sym.Signature.Params))
	for _, p := range sym.Signature.Params {
		label := p.Label()
		if label == "" {
			continue
		}
		params = append(params, ParameterInformation{Label: label})
	}
	if len(params) == 0 {
		return nil
	}

	return &SignatureInformation{
		Label:      sym.Signature.Label,
		Parameters: params,
	}
}
