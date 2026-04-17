package lsp

import (
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

func (h *Handler) completeDot(cctx *CompletionCtx, fileURI string) []CompletionItem {
	resolver := &typeResolver{idx: h.idx, imports: cctx.Imports, pkg: cctx.Package}
	expectedType := h.resolveCurrentArgumentTypeExpr(cctx, resolver)

	typeExpr, staticAccess := h.resolveReceiverTypeExpr(cctx, resolver)
	if typeExpr == nil {
		h.logger.Printf("completeDot: receiver=%q resolved to nil, falling back", cctx.Receiver)
		return h.completeDotFallback(cctx)
	}
	h.logger.Printf("completeDot: receiver=%q resolved to type=%s static=%v", cctx.Receiver, typeExpr.Sym, staticAccess)

	members := h.idx.MembersOfType(typeExpr.Sym)
	lowerQuery := strings.ToLower(cctx.Prefix)

	var items []CompletionItem
	seen := make(map[string]struct{})
	for _, m := range members {
		if m.Kind == sdb.SymbolInformation_CONSTRUCTOR {
			continue
		}
		if !index.FuzzyMatchLower(m.Name, lowerQuery) {
			continue
		}
		if staticAccess && !m.IsStatic {
			continue
		}
		if !staticAccess && m.IsStatic {
			continue
		}
		candidateType := h.memberCompletionTypeExpr(typeExpr, m)
		matchScore := completionMatchScorePrefix(m.Name, lowerQuery)
		kind := sdbKindToCompletionKind(m.Kind)
		kindOrder := "1"
		if kind == CompletionKindField || kind == CompletionKindProperty {
			kindOrder = "0"
		}
		key := completionItemKeyForSymbol(m)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		sortText := completionTypeMatchPrefix(h, expectedType, candidateType) + matchScore + kindOrder + completionNameSortKey(m.Name)
		if kind == CompletionKindMethod || kind == CompletionKindConstructor {
			sortText += signatureSortSuffix(m.Signature)
		}
		items = append(items, methodCompletionItem(m.Name, kind, m.Signature, sortText, m.Doc, cctx.ParenFollows))
	}
	sortCompletionItems(items)
	if len(items) > 100 {
		items = items[:100]
	}
	return items
}

func (h *Handler) memberCompletionTypeExpr(owner *index.TypeExpr, sym index.Symbol) *index.TypeExpr {
	if owner == nil {
		return nil
	}
	te := symbolReturnTypeExpr(sym, h.idx)
	if te == nil {
		return nil
	}
	te = substituteTypeParams(te, owner, h.idx)
	return substituteNamedTypeParams(te, owner, h.idx)
}

func (h *Handler) completeDotFallback(cctx *CompletionCtx) []CompletionItem {
	lowerQuery := strings.ToLower(cctx.Prefix)
	seen := make(map[string]struct{})
	var items []CompletionItem

	addField := func(name, detail, sortBucket string) {
		if !index.FuzzyMatchLower(name, lowerQuery) {
			return
		}
		if _, ok := seen["field|"+name]; ok {
			return
		}
		seen["field|"+name] = struct{}{}

		sortPrefix := completionMatchScorePrefix(name, lowerQuery)
		items = append(items, CompletionItem{
			Label:      name,
			Kind:       CompletionKindField,
			InsertText: name,
			Detail:     detail,
			SortText:   sortPrefix + sortBucket + completionNameSortKey(name),
			FilterText: name,
		})
	}

	addMethodDecl := func(m MethodDecl, sortBucket string) {
		if !index.FuzzyMatchLower(m.Name, lowerQuery) {
			return
		}
		key := "methoddecl|" + completionItemKeyForMethodDecl(m)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}

		sortPrefix := completionMatchScorePrefix(m.Name, lowerQuery)
		item := CompletionItem{
			Label:      formatMethodDeclDetail(m),
			Kind:       CompletionKindMethod,
			Detail:     formatMethodDeclDetail(m),
			SortText:   sortPrefix + sortBucket + completionNameSortKey(m.Name) + methodDeclSortSuffix(m),
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

	addIndexedMethod := func(sym index.Symbol, sortBucket string) {
		if !index.FuzzyMatchLower(sym.Name, lowerQuery) {
			return
		}
		key := "indexed|" + completionItemKeyForSymbol(sym)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}

		sortPrefix := completionMatchScorePrefix(sym.Name, lowerQuery)
		sortText := sortPrefix + sortBucket + completionNameSortKey(sym.Name)
		if sym.Signature != nil {
			sortText += signatureSortSuffix(sym.Signature)
		}
		items = append(items, methodCompletionItem(sym.Name, sdbKindToCompletionKind(sym.Kind), sym.Signature, sortText, sym.Doc, cctx.ParenFollows))
	}

	for _, f := range cctx.ClassFields {
		addField(f.Name, f.Type.String(), "0")
	}
	for _, m := range cctx.ClassMethods {
		addMethodDecl(m, "1")
	}
	for _, m := range h.idx.MembersOfType("java/lang/Object#") {
		if m.IsStatic {
			continue
		}
		addIndexedMethod(m, "2")
	}

	sortCompletionItems(items)
	if len(items) > 100 {
		items = items[:100]
	}
	return items
}

func (h *Handler) resolveReceiverTypeExpr(cctx *CompletionCtx, resolver *typeResolver) (*index.TypeExpr, bool) {
	recv := cctx.Receiver
	if recv == "this" {
		if sym := resolver.resolve(cctx.EnclosingClass); sym != "" {
			return &index.TypeExpr{Sym: sym}, false
		}
		return nil, false
	}
	if recv == "super" {
		classSym := resolver.resolve(cctx.EnclosingClass)
		if classSym != "" {
			if pts := h.idx.ParentTypesOf(classSym); len(pts) > 0 {
				return pts[0], false
			}
			if parents := h.idx.ParentsOf(classSym); len(parents) > 0 {
				return &index.TypeExpr{Sym: parents[0]}, false
			}
		}
		return nil, false
	}

	parts := strings.Split(recv, ".")
	if len(parts) == 0 {
		return nil, false
	}

	typeExpr, staticAccess := h.resolveIdentifierTypeExpr(parts[0], cctx, resolver)
	for i := 1; i < len(parts) && typeExpr != nil; i++ {
		typeExpr = h.resolveMemberTypeExpr(typeExpr, parts[i])
		staticAccess = false
	}
	return typeExpr, staticAccess
}

func (h *Handler) resolveIdentifierTypeExpr(name string, cctx *CompletionCtx, resolver *typeResolver) (*index.TypeExpr, bool) {
	for i := len(cctx.LambdaParams) - 1; i >= 0; i-- {
		if cctx.LambdaParams[i].Name != name {
			continue
		}
		if te := resolver.resolveParameterized(cctx.LambdaParams[i].Type); te != nil {
			return te, false
		}
		if te := h.resolveInferredLambdaParamType(i, cctx, resolver); te != nil {
			return te, false
		}
	}
	for i := 0; i < len(cctx.Locals); i++ {
		if cctx.Locals[i].Name == name {
			if te := resolver.resolveParameterized(cctx.Locals[i].Type); te != nil {
				return te, false
			}
			if cctx.Locals[i].Initializer != nil {
				if te := h.resolveVarInitializer(cctx.Locals[i].Initializer, cctx, resolver); te != nil {
					return te, false
				}
			}
		}
	}
	for _, p := range cctx.Params {
		if p.Name == name {
			return resolver.resolveParameterized(p.Type), false
		}
	}
	for _, f := range cctx.ClassFields {
		if f.Name == name {
			return resolver.resolveParameterized(f.Type), false
		}
	}
	if sym := resolver.resolve(name); sym != "" {
		return &index.TypeExpr{Sym: sym}, true
	}
	if te := h.resolveStaticImportType(name, cctx.Imports, resolver); te != nil {
		return te, false
	}
	return nil, false
}

func (h *Handler) resolveStaticImportType(name string, imports []ImportSpec, resolver *typeResolver) *index.TypeExpr {
	h.logger.Printf("resolveStaticImportType: looking for %q in %d imports", name, len(imports))
	for _, imp := range imports {
		if !imp.Static {
			continue
		}
		if imp.Wildcard {
			classFQN := strings.TrimSuffix(imp.Path, ".*")
			classSym := resolver.resolve(simpleNameFromFQN(classFQN))
			if classSym == "" {
				classSym = fqnToSymbol(classFQN)
			}
			h.logger.Printf("resolveStaticImportType: wildcard import %q -> classSym=%s", imp.Path, classSym)
			if te := h.resolveStaticMemberType(classSym, name, resolver); te != nil {
				h.logger.Printf("resolveStaticImportType: resolved %q -> %s", name, te.Sym)
				return te
			}
			continue
		}

		lastDot := strings.LastIndex(imp.Path, ".")
		if lastDot < 0 || imp.Path[lastDot+1:] != name {
			continue
		}
		classFQN := imp.Path[:lastDot]
		classSym := resolver.resolve(simpleNameFromFQN(classFQN))
		if classSym == "" {
			classSym = fqnToSymbol(classFQN)
		}
		h.logger.Printf("resolveStaticImportType: specific import %q -> classSym=%s", imp.Path, classSym)
		if te := h.resolveStaticMemberType(classSym, name, resolver); te != nil {
			h.logger.Printf("resolveStaticImportType: resolved %q -> %s", name, te.Sym)
			return te
		}
	}
	h.logger.Printf("resolveStaticImportType: %q not resolved", name)
	return nil
}

func (h *Handler) resolveStaticMemberType(classSym, memberName string, resolver *typeResolver) *index.TypeExpr {
	members := h.idx.MembersOfType(classSym)
	h.logger.Printf("resolveStaticMemberType: classSym=%s memberName=%q totalMembers=%d", classSym, memberName, len(members))

	var firstCandidate *index.TypeExpr
	for _, m := range members {
		if m.Name != memberName || !m.IsStatic {
			continue
		}

		var candidates []*index.TypeExpr
		if retType := h.idx.DeclTypeOf(m.Symbol); retType != nil && !isTypeParamSymbol(retType.Sym) {
			candidates = append(candidates, retType)
		}
		if sym := h.idx.TypeOfSymbol(m.Symbol); sym != "" && !isTypeParamSymbol(sym) {
			candidates = append(candidates, &index.TypeExpr{Sym: sym})
		}
		if m.Signature != nil {
			if m.Signature.ReturnTypeSym != "" && !isTypeParamSymbol(m.Signature.ReturnTypeSym) {
				candidates = append(candidates, &index.TypeExpr{Sym: m.Signature.ReturnTypeSym})
			}
			retTypeName := returnTypeFromMethodLabel(m.Signature.Label)
			if retTypeName != "" {
				baseName := retTypeName
				if ltIdx := strings.IndexByte(baseName, '<'); ltIdx > 0 {
					baseName = baseName[:ltIdx]
				}
				if sym := resolver.resolve(baseName); sym != "" {
					candidates = append(candidates, &index.TypeExpr{Sym: sym})
				}
			}
		}

		for _, te := range candidates {
			if len(h.idx.MembersOfType(te.Sym)) > 0 {
				h.logger.Printf("resolveStaticMemberType: %s -> %s (has members)", m.Symbol, te.Sym)
				return te
			}
			if firstCandidate == nil {
				firstCandidate = te
				h.logger.Printf("resolveStaticMemberType: %s -> %s (no members yet, saved as fallback)", m.Symbol, te.Sym)
			}
		}
	}
	return firstCandidate
}

func isTypeParamSymbol(sym string) bool {
	return strings.Contains(sym, "#[")
}

func (h *Handler) resolveMemberTypeExpr(owner *index.TypeExpr, memberName string) *index.TypeExpr {
	members := h.idx.MembersOfType(owner.Sym)
	for _, m := range members {
		if m.Name != memberName {
			continue
		}
		if retType := h.idx.DeclTypeOf(m.Symbol); retType != nil {
			return substituteTypeParams(retType, owner, h.idx)
		}
		if sym := h.idx.TypeOfSymbol(m.Symbol); sym != "" {
			return substituteTypeParams(&index.TypeExpr{Sym: sym}, owner, h.idx)
		}
		return nil
	}
	return nil
}
