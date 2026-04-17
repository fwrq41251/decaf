package lsp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

func (h *Handler) completeLexical(cctx *CompletionCtx, fileURI string, content []byte) []CompletionItem {
	prefix := strings.ToLower(cctx.Prefix)
	if prefix == "" {
		return nil
	}

	resolver := &typeResolver{idx: h.idx, imports: cctx.Imports, pkg: cctx.Package}
	expectedType := h.resolveCurrentArgumentTypeExpr(cctx, resolver)

	seen := make(map[string]struct{})
	seenTypeSymbols := make(map[string]struct{})
	var items []CompletionItem

	items = append(items, h.completeSnippets(cctx)...)

	matchQuality := func(name string) string {
		return fmt.Sprintf("%d", index.CompletionMatchScore(name, cctx.Prefix))
	}
	contextPrefix := func(kind int) string {
		if !cctx.InTypePosition {
			return "1"
		}
		if isTypeCompletionKind(kind) || kind == CompletionKindKeyword {
			return "0"
		}
		return "2"
	}
	addItem := func(name string, kind int, detail string, candidateType *index.TypeExpr, scopeOrder string) {
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		items = append(items, CompletionItem{
			Label:      name,
			Kind:       kind,
			InsertText: name,
			Detail:     detail,
			SortText:   contextPrefix(kind) + completionTypeMatchPrefix(h, expectedType, candidateType) + matchQuality(name) + scopeOrder + completionNameSortKey(name),
			FilterText: name,
		})
	}
	matchName := func(name string) bool { return index.FuzzyMatch(name, cctx.Prefix) }

	for i := len(cctx.LambdaParams) - 1; i >= 0; i-- {
		p := cctx.LambdaParams[i]
		if matchName(p.Name) {
			addItem(p.Name, CompletionKindVariable, p.Type.String(), p.Type, "00")
		}
	}
	for i := 0; i < len(cctx.Locals); i++ {
		l := cctx.Locals[i]
		if matchName(l.Name) {
			addItem(l.Name, CompletionKindVariable, l.Type.String(), l.Type, "01")
		}
	}
	for _, p := range cctx.Params {
		if matchName(p.Name) {
			addItem(p.Name, CompletionKindVariable, p.Type.String(), p.Type, "02")
		}
	}
	for _, f := range cctx.ClassFields {
		if matchName(f.Name) {
			addItem(f.Name, CompletionKindField, f.Type.String(), f.Type, "03")
		}
	}
	for _, m := range cctx.ClassMethods {
		if !matchName(m.Name) {
			continue
		}
		key := completionItemKeyForMethodDecl(m)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		item := CompletionItem{
			Label:      formatMethodDeclDetail(m),
			Kind:       CompletionKindMethod,
			Detail:     formatMethodDeclDetail(m),
			SortText:   contextPrefix(CompletionKindMethod) + completionTypeMatchPrefix(h, expectedType, m.ReturnType) + matchQuality(m.Name) + "04" + completionNameSortKey(m.Name) + methodDeclSortSuffix(m),
			FilterText: m.Name,
		}
		if cctx.ParenFollows {
			item.InsertText = m.Name
		} else {
			item.InsertText = buildLocalMethodInsertText(m)
			item.InsertTextFormat = InsertTextFormatSnippet
		}
		items = append(items, item)
	}

	addStaticMembers := func(classSym string, targetName string) {
		for _, m := range h.idx.MembersOfType(classSym) {
			if !m.IsStatic || !matchName(m.Name) {
				continue
			}
			if targetName != "" && m.Name != targetName {
				continue
			}
			kind := sdbKindToCompletionKind(m.Kind)
			key := completionItemKeyForSymbol(m)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			retType := symbolReturnTypeExpr(m, h.idx)
			sortText := contextPrefix(kind) + completionTypeMatchPrefix(h, expectedType, retType) + matchQuality(m.Name) + "05" + completionNameSortKey(m.Name)
			if kind == CompletionKindMethod || kind == CompletionKindConstructor {
				sortText += signatureSortSuffix(m.Signature)
			}
			items = append(items, methodCompletionItem(m.Name, kind, m.Signature, sortText, m.Doc, cctx.ParenFollows))
		}
	}

	for _, imp := range cctx.Imports {
		if !imp.Static {
			continue
		}
		if imp.Wildcard {
			classFQN := strings.TrimSuffix(imp.Path, ".*")
			classSym := resolver.resolve(simpleNameFromFQN(classFQN))
			if classSym == "" {
				classSym = fqnToSymbol(classFQN)
			}
			addStaticMembers(classSym, "")
			continue
		}
		lastDot := strings.LastIndex(imp.Path, ".")
		if lastDot < 0 {
			continue
		}
		memberName := imp.Path[lastDot+1:]
		classFQN := imp.Path[:lastDot]
		classSym := resolver.resolve(simpleNameFromFQN(classFQN))
		if classSym == "" {
			classSym = fqnToSymbol(classFQN)
		}
		addStaticMembers(classSym, memberName)
	}

	for _, kw := range javaKeywords {
		if strings.HasPrefix(kw, prefix) {
			addItem(kw, CompletionKindKeyword, "keyword", nil, "07")
		}
	}
	for _, lit := range javaLiterals {
		if strings.HasPrefix(lit, prefix) {
			addItem(lit, CompletionKindKeyword, "literal", nil, "07")
		}
	}
	if len(items) >= 100 {
		return items[:100]
	}

	for _, s := range h.idx.CompletionSymbols(fileURI, cctx.Prefix) {
		if len(items) >= 100 {
			break
		}
		scopeOrder := "10"
		if s.SameFile {
			if isTypeCompletionKind(sdbKindToCompletionKind(s.Kind)) {
				scopeOrder = "06"
			} else {
				scopeOrder = "09"
			}
		} else if isTypeCompletionKind(sdbKindToCompletionKind(s.Kind)) {
			scopeOrder = "08"
		}

		kind := sdbKindToCompletionKind(s.Kind)
		if isTypeCompletionKind(kind) {
			if _, ok := seenTypeSymbols[s.Symbol]; ok {
				continue
			}
			seenTypeSymbols[s.Symbol] = struct{}{}
		} else {
			key := completionItemKeyForIndexedSymbol(s, kind)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		sortText := contextPrefix(kind) + completionTypeMatchPrefix(h, expectedType, symbolReturnTypeExpr(s, h.idx)) + matchQuality(s.Name) + scopeOrder
		if isTypeCompletionKind(kind) {
			sortText += typeCompletionPriority(cctx, s)
		}
		sortText += completionNameSortKey(s.Name)
		if kind == CompletionKindMethod || kind == CompletionKindConstructor {
			sortText += signatureSortSuffix(s.Signature)
		}
		item := methodCompletionItem(s.Name, kind, s.Signature, sortText, s.Doc, cctx.ParenFollows)
		if cctx.AfterNew && isTypeCompletionKind(kind) && !cctx.ParenFollows {
			item.InsertText = s.Name + "($0)"
			item.InsertTextFormat = InsertTextFormatSnippet
		}
		if s.Kind == sdb.SymbolInformation_CLASS || s.Kind == sdb.SymbolInformation_INTERFACE {
			if fqn := fqnFromSymbol(s.Symbol); fqn != "" {
				if edit := computeImportEdit(content, cctx.Imports, cctx.Package, fqn); edit != nil {
					item.AdditionalTextEdits = []TextEdit{*edit}
					item.Detail = fqn
				}
			}
		}
		items = append(items, item)
	}

	return items
}

func symbolReturnTypeExpr(sym index.Symbol, idx *index.Index) *index.TypeExpr {
	if te := idx.DeclTypeOf(sym.Symbol); te != nil {
		return te
	}
	if retSym := idx.TypeOfSymbol(sym.Symbol); retSym != "" {
		return &index.TypeExpr{Sym: retSym}
	}
	if sym.Signature != nil && sym.Signature.ReturnTypeSym != "" {
		return &index.TypeExpr{Sym: sym.Signature.ReturnTypeSym}
	}
	return nil
}

func typeExprMatchesExpected(candidate, expected *index.TypeExpr) bool {
	if candidate == nil || expected == nil {
		return false
	}
	if sameTypeExpr(candidate, expected) {
		return true
	}
	return candidate.Sym == expected.Sym
}

func returnTypeFromMethodLabel(label string) string {
	openParen := strings.IndexByte(label, '(')
	if openParen <= 0 {
		return ""
	}
	prefix := label[:openParen]
	lastSpace := strings.LastIndexByte(prefix, ' ')
	if lastSpace <= 0 {
		return ""
	}
	return strings.TrimSpace(prefix[:lastSpace])
}

func simplifyTypeName(name string) string {
	name = strings.TrimSuffix(name, "#")
	name = strings.TrimSuffix(name, ".")
	if idx := strings.LastIndexAny(name, "/."); idx >= 0 {
		return name[idx+1:]
	}
	return name
}

func methodCompletionItem(name string, kind int, sig *index.SignatureInfo, sortText, doc string, parenFollows bool) CompletionItem {
	item := CompletionItem{
		Label:      name,
		Kind:       kind,
		InsertText: name,
		SortText:   sortText,
		FilterText: name,
	}
	if sig != nil {
		item.Detail = sig.Label
	}
	if kind == CompletionKindMethod || kind == CompletionKindConstructor {
		item.Label = methodCompletionLabel(name, sig)
	}
	if doc != "" {
		item.Documentation = &MarkupContent{Kind: "markdown", Value: doc}
	}
	if kind == CompletionKindMethod || kind == CompletionKindConstructor {
		if parenFollows {
			item.InsertText = name
		} else if sig != nil && sig.HasParams {
			item.InsertText = buildMethodInsertText(name, sig)
		} else {
			item.InsertText = name + "()$0"
		}
		if !parenFollows {
			item.InsertTextFormat = InsertTextFormatSnippet
		}
	}
	return item
}

func buildMethodInsertText(name string, sig *index.SignatureInfo) string {
	if sig == nil || !sig.HasParams {
		return name + "()$0"
	}
	if len(sig.Params) == 0 {
		return name + "($1)$0"
	}

	var placeholders []string
	for i, p := range sig.Params {
		label := p.Name
		if label == "" {
			label = p.Type
		}
		if label == "" {
			label = "arg"
		}
		placeholders = append(placeholders, fmt.Sprintf("${%d:%s}", i+1, label))
	}
	return name + "(" + strings.Join(placeholders, ", ") + ")$0"
}

func buildLocalMethodInsertText(m MethodDecl) string {
	if len(m.Params) == 0 {
		return m.Name + "()$0"
	}
	var placeholders []string
	for i, p := range m.Params {
		label := p
		if label == "" {
			label = "arg"
		}
		placeholders = append(placeholders, fmt.Sprintf("${%d:%s}", i+1, label))
	}
	return m.Name + "(" + strings.Join(placeholders, ", ") + ")$0"
}

func methodCompletionLabel(name string, sig *index.SignatureInfo) string {
	if sig == nil {
		return name + "()"
	}
	params := sig.ParseParams()
	if len(params) == 0 {
		return name + "()"
	}
	return name + "(" + strings.Join(params, ", ") + ")"
}

func isTypeCompletionKind(kind int) bool {
	return kind == CompletionKindClass || kind == CompletionKindInterface || kind == CompletionKindEnum
}

func typeCompletionPriority(cctx *CompletionCtx, s index.Symbol) string {
	fqn := fqnFromSymbol(s.Symbol)
	if fqn == "" {
		return "9"
	}
	if isExplicitlyImportedType(cctx.Imports, fqn) {
		return "0"
	}
	if packageName(fqn) == cctx.Package && cctx.Package != "" {
		return "1"
	}
	if isImplicitlyImportedJavaLang(fqn) {
		return "2"
	}
	if isJDKType(fqn) {
		return "3"
	}
	return "4"
}

func isExplicitlyImportedType(imports []ImportSpec, fqn string) bool {
	pkg := packageName(fqn)
	for _, imp := range imports {
		if imp.Static {
			continue
		}
		if imp.Wildcard {
			if strings.TrimSuffix(imp.Path, ".*") == pkg {
				return true
			}
			continue
		}
		if imp.Path == fqn {
			return true
		}
	}
	return false
}

func isImplicitlyImportedJavaLang(fqn string) bool {
	return strings.HasPrefix(fqn, "java.lang.")
}

func isJDKType(fqn string) bool {
	return strings.HasPrefix(fqn, "java.") || strings.HasPrefix(fqn, "javax.")
}

func packageName(fqn string) string {
	if idx := strings.LastIndexByte(fqn, '.'); idx >= 0 {
		return fqn[:idx]
	}
	return ""
}

func countStaticImports(imports []ImportSpec) int {
	n := 0
	for _, imp := range imports {
		if imp.Static {
			n++
		}
	}
	return n
}

func simpleNameFromFQN(fqn string) string {
	if idx := strings.LastIndexByte(fqn, '.'); idx >= 0 {
		return fqn[idx+1:]
	}
	return fqn
}

func sortCompletionItems(items []CompletionItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].SortText != items[j].SortText {
			return items[i].SortText < items[j].SortText
		}
		if items[i].Label != items[j].Label {
			return items[i].Label < items[j].Label
		}
		return items[i].Detail < items[j].Detail
	})
}

func completionItemKeyForSymbol(sym index.Symbol) string {
	if sym.Signature != nil && sym.Signature.Label != "" {
		return sym.Symbol + "|" + sym.Signature.Label
	}
	return sym.Symbol + "|" + sym.Name
}

func completionItemKeyForIndexedSymbol(sym index.Symbol, kind int) string {
	if kind == CompletionKindMethod || kind == CompletionKindConstructor {
		return completionItemKeyForSymbol(sym)
	}
	if isTypeCompletionKind(kind) {
		return sym.Symbol
	}
	return sym.Name
}

func completionItemKeyForMethodDecl(m MethodDecl) string {
	return m.Name + "|" + strings.Join(m.Params, ",")
}

func signatureSortSuffix(sig *index.SignatureInfo) string {
	if sig == nil || sig.Label == "" {
		return ""
	}
	return "|" + sig.Label
}

func methodDeclSortSuffix(m MethodDecl) string {
	if len(m.Params) == 0 {
		return "|()"
	}
	return "|(" + strings.Join(m.Params, ",") + ")"
}

func completionNameSortKey(name string) string {
	return strings.ToLower(name) + "|" + name
}

func formatMethodDeclDetail(m MethodDecl) string {
	if len(m.Params) == 0 {
		return m.Name + "()"
	}
	return fmt.Sprintf("%s(%s)", m.Name, strings.Join(m.Params, ", "))
}
