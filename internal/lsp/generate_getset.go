package lsp

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

// getterAction returns a "Generate getter..." CodeAction if there are fields missing a getter.
func getterAction(fileURI string, cursorLine int, candidates []fieldWithType) *CodeAction {
	hasMissing := false
	for _, c := range candidates {
		if !c.hasGetter {
			hasMissing = true
			break
		}
	}
	if !hasMissing {
		return nil
	}
	return &CodeAction{
		Title: "Generate getter...",
		Kind:  "source",
		Command: &Command{
			Title:     "Generate getter...",
			Command:   "decaf.generateGetter",
			Arguments: []any{fileURI, cursorLine},
		},
	}
}

// setterAction returns a "Generate setter..." CodeAction if there are fields missing a setter.
func setterAction(fileURI string, cursorLine int, candidates []fieldWithType) *CodeAction {
	hasMissing := false
	for _, c := range candidates {
		if !c.hasSetter {
			hasMissing = true
			break
		}
	}
	if !hasMissing {
		return nil
	}
	return &CodeAction{
		Title: "Generate setter...",
		Kind:  "source",
		Command: &Command{
			Title:     "Generate setter...",
			Command:   "decaf.generateSetter",
			Arguments: []any{fileURI, cursorLine},
		},
	}
}

// fieldWithType holds a field symbol, its resolved type name, and whether it has a getter/setter.
type fieldWithType struct {
	field     index.Symbol
	typeName  string
	hasGetter bool
	hasSetter bool
}

// collectFieldCandidates returns non-static fields within the current class context.
func collectFieldCandidates(fileURI string, idx *index.Index, overlay string, cursorLine int) []fieldWithType {
	return collectFieldCandidatesWithContext(context.Background(), fileURI, idx, overlay, overlay != "", cursorLine)
}

func collectFieldCandidatesWithContext(ctx context.Context, fileURI string, idx *index.Index, overlay string, hasOverlay bool, cursorLine int) []fieldWithType {
	content := readContent(fileURI, overlay, hasOverlay)
	if content == nil {
		return nil
	}

	tree, err := getTreeWithContext(ctx, content)
	if err != nil {
		return nil
	}
	root := tree.RootNode()

	className, _ := findClassContext(root, content, cursorLine)
	if className == "" {
		return nil
	}

	classSym, found := findClassSymbol(fileURI, className, idx)
	if !found {
		return nil
	}

	members := idx.DirectMembersOfType(classSym.Symbol)

	existingMethods := make(map[string]bool)
	for _, m := range members {
		if m.Kind == sdb.SymbolInformation_METHOD {
			existingMethods[m.Name] = true
		}
	}

	var candidates []fieldWithType
	for _, m := range members {
		if m.Kind != sdb.SymbolInformation_FIELD || m.IsStatic {
			continue
		}
		typeName := resolveFieldType(m, idx)
		candidates = append(candidates, fieldWithType{
			field:     m,
			typeName:  typeName,
			hasGetter: existingMethods[getterName(m.Name)] || (isBooleanType(typeName) && existingMethods[booleanGetterName(m.Name)]),
			hasSetter: existingMethods[setterName(m.Name)],
		})
	}
	return candidates
}

// isBooleanType returns true if the type name is boolean or java.lang.Boolean.
func isBooleanType(typeName string) bool {
	return typeName == "boolean" || typeName == "Boolean" || typeName == "java.lang.Boolean"
}

// resolveFieldType resolves the type name of a field symbol.
func resolveFieldType(f index.Symbol, idx *index.Index) string {
	if idx != nil {
		if te := idx.DeclTypeOf(f.Symbol); te != nil {
			if rendered := formatMethodStubType(te); rendered != "" {
				return rendered
			}
		}
		if typeSym := idx.TypeOfSymbol(f.Symbol); typeSym != "" {
			return formatMethodStubType(&index.TypeExpr{Sym: typeSym})
		}
	}
	if f.Signature != nil && f.Signature.Label != "" {
		if labelIdx := strings.Index(f.Signature.Label, ": "); labelIdx >= 0 {
			return f.Signature.Label[labelIdx+2:]
		}
	}
	return "Object"
}

// generateGetter generates a getter method for a field.
func generateGetter(f fieldWithType) string {
	name := getterName(f.field.Name)
	if isBooleanType(f.typeName) {
		name = booleanGetterName(f.field.Name)
	}
	return fmt.Sprintf("\n    public %s %s() {\n        return %s;\n    }\n",
		f.typeName, name, f.field.Name)
}

// generateSetter generates a setter method for a field.
func generateSetter(f fieldWithType) string {
	return fmt.Sprintf("\n    public void %s(%s %s) {\n        this.%s = %s;\n    }\n",
		setterName(f.field.Name), f.typeName, f.field.Name, f.field.Name, f.field.Name)
}

// getterName returns the conventional getter name for a field (e.g., "name" -> "getName").
func getterName(fieldName string) string {
	return "get" + capitalize(fieldName)
}

// booleanGetterName returns the conventional boolean getter name (e.g., "active" -> "isActive").
func booleanGetterName(fieldName string) string {
	if strings.HasPrefix(fieldName, "is") && len(fieldName) > 2 && unicode.IsUpper(rune(fieldName[2])) {
		return fieldName
	}
	return "is" + capitalize(fieldName)
}

// setterName returns the conventional setter name for a field (e.g., "name" -> "setName").
func setterName(fieldName string) string {
	return "set" + capitalize(fieldName)
}

// capitalize returns the string with its first letter uppercased.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
