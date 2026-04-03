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

	if sym.Doc != "" {
		b.WriteString("\n\n---\n")
		b.WriteString(formatJavadoc(sym.Doc))
	}

	return b.String()
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
