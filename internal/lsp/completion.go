package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	slog "github.com/smacker/go-tree-sitter"
)

var javaKeywords = []string{
	"abstract", "assert", "boolean", "break", "byte", "case", "catch", "char", "class", "const",
	"continue", "default", "do", "double", "else", "enum", "extends", "final", "finally", "float",
	"for", "goto", "if", "implements", "import", "instanceof", "int", "interface", "long", "native",
	"new", "package", "private", "protected", "public", "return", "short", "static", "strictfp",
	"super", "switch", "synchronized", "this", "throw", "throws", "transient", "try", "void",
	"volatile", "while",
}

var javaLiterals = []string{
	"true", "false", "null",
}

func (h *Handler) handleCompletion(ctx context.Context, params json.RawMessage) (any, error) {
	var p CompletionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return CompletionList{}, nil
	}

	// Get the current buffer content (overlay or disk).
	content := h.getFileContent(p.TextDocument.URI)
	if content == "" {
		return CompletionList{}, nil
	}

	// Parse completion context using Tree-sitter.
	cctx := parseCompletionCtx(h.logger, []byte(content), p.Position.Line, p.Position.Character)

	var items []CompletionItem

	contentBytes := []byte(content)
	if cctx.Kind == CompletionDot {
		items = h.completeDot(cctx, p.TextDocument.URI)
		// Append postfix completion items (e.g. expr.var, expr.if, expr.for).
		items = append(items, completePostfix(cctx, contentBytes)...)
	} else {
		items = h.completeLexical(cctx, p.TextDocument.URI, contentBytes)
	}
	sortCompletionItems(items)

	h.logger.Printf("completion at %s:%d:%d kind=%d prefix=%q receiver=%q imports=%d staticImports=%d -> %d items",
		p.TextDocument.URI, p.Position.Line, p.Position.Character,
		cctx.Kind, cctx.Prefix, cctx.Receiver, len(cctx.Imports), countStaticImports(cctx.Imports), len(items))
	return CompletionList{IsIncomplete: len(items) >= 100, Items: items}, nil
}

// completeDot handles member completion after a dot (e.g. "foo.ba").
func (h *Handler) completeDot(cctx *CompletionCtx, fileURI string) []CompletionItem {
	resolver := &typeResolver{idx: h.idx, imports: cctx.Imports, pkg: cctx.Package}
	expectedType := h.resolveCurrentArgumentTypeExpr(cctx, resolver)

	// Resolve the receiver expression to a type with generic arguments.
	// Also track whether this is a static access (e.g. "Objects.") vs instance access (e.g. "obj.").
	typeExpr, staticAccess := h.resolveReceiverTypeExpr(cctx, resolver)
	if typeExpr == nil {
		h.logger.Printf("completeDot: receiver=%q resolved to nil, falling back", cctx.Receiver)
		return h.completeDotFallback(cctx)
	}
	h.logger.Printf("completeDot: receiver=%q resolved to type=%s static=%v", cctx.Receiver, typeExpr.Sym, staticAccess)

	// Get members of the resolved type.
	members := h.idx.MembersOfType(typeExpr.Sym)
	query := cctx.Prefix
	lowerQuery := strings.ToLower(query)

	var items []CompletionItem
	seen := make(map[string]struct{})
	for _, m := range members {
		// Skip constructors: they should only appear after 'new', not in member access.
		if m.Kind == sdb.SymbolInformation_CONSTRUCTOR {
			continue
		}
		if !index.FuzzyMatchLower(m.Name, lowerQuery) {
			continue
		}
		// Filter by static/instance context.
		if staticAccess && !m.IsStatic {
			continue
		}
		if !staticAccess && m.IsStatic {
			continue
		}
		// Build sortText using full match quality scoring.
		candidateType := h.memberCompletionTypeExpr(typeExpr, m)
		matchScore := fmt.Sprintf("%d", index.CompletionMatchScoreLower(m.Name, lowerQuery))
		kind := sdbKindToCompletionKind(m.Kind)
		kindOrder := "1" // methods
		if kind == CompletionKindField || kind == CompletionKindProperty {
			kindOrder = "0" // fields first
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

		item := methodCompletionItem(m.Name, kind, m.Signature, sortText, m.Doc, cctx.ParenFollows)
		items = append(items, item)
	}
	sortCompletionItems(items)
	if len(items) > 100 {
		items = items[:100]
	}
	return items
}

func completionTypeMatchPrefix(h *Handler, expectedType, candidateType *index.TypeExpr) string {
	if candidateType == nil || expectedType == nil {
		return "2"
	}
	if sameTypeExpr(candidateType, expectedType) {
		return "0"
	}
	if h.idx.IsAssignableTo(candidateType.Sym, expectedType.Sym) {
		return "1"
	}
	return "2"
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
	query := cctx.Prefix
	lowerQuery := strings.ToLower(query)
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

		sortPrefix := fmt.Sprintf("%d", index.CompletionMatchScoreLower(name, lowerQuery))
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

		sortPrefix := fmt.Sprintf("%d", index.CompletionMatchScoreLower(m.Name, lowerQuery))
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

		sortPrefix := fmt.Sprintf("%d", index.CompletionMatchScoreLower(sym.Name, lowerQuery))
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

// resolveReceiverTypeExpr resolves the type of a receiver expression, preserving generic arguments.
// Returns the resolved type and whether this is a static access (class name before the dot).
func (h *Handler) resolveReceiverTypeExpr(cctx *CompletionCtx, resolver *typeResolver) (*index.TypeExpr, bool) {
	recv := cctx.Receiver

	// Handle "this" → enclosing class.
	if recv == "this" {
		if sym := resolver.resolve(cctx.EnclosingClass); sym != "" {
			return &index.TypeExpr{Sym: sym}, false
		}
		return nil, false
	}
	// Handle "super" → parent of enclosing class.
	if recv == "super" {
		classSym := resolver.resolve(cctx.EnclosingClass)
		if classSym != "" {
			if pts := h.idx.ParentTypesOf(classSym); len(pts) > 0 {
				return pts[0], false
			}
			// Fallback to old ParentsOf.
			if parents := h.idx.ParentsOf(classSym); len(parents) > 0 {
				return &index.TypeExpr{Sym: parents[0]}, false
			}
		}
		return nil, false
	}

	// Handle chained dot access (e.g. "foo.bar"): resolve left-to-right.
	parts := strings.Split(recv, ".")
	if len(parts) == 0 {
		return nil, false
	}

	// Resolve the first part.
	typeExpr, staticAccess := h.resolveIdentifierTypeExpr(parts[0], cctx, resolver)

	// Resolve each subsequent part as a field/method access.
	// Once we resolve through a member, it's no longer a static access.
	for i := 1; i < len(parts) && typeExpr != nil; i++ {
		typeExpr = h.resolveMemberTypeExpr(typeExpr, parts[i])
		staticAccess = false
	}

	return typeExpr, staticAccess
}

// resolveIdentifierTypeExpr resolves the type of a simple identifier, preserving generic arguments.
// Searches locals → params → fields (Tree-sitter source, may have "List<String>"),
// then falls back to class name resolution (static access).
// Returns the resolved type and whether this is a static access (class name, not a variable).
func (h *Handler) resolveIdentifierTypeExpr(name string, cctx *CompletionCtx, resolver *typeResolver) (*index.TypeExpr, bool) {
	// Search lambda parameters from innermost to outermost so lambda scope
	// correctly shadows surrounding locals and method parameters.
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
	// Search locals (innermost first).
	for i := 0; i < len(cctx.Locals); i++ {
		if cctx.Locals[i].Name == name {
			if te := resolver.resolveParameterized(cctx.Locals[i].Type); te != nil {
				return te, false
			}
			// Deferred var inference: resolve method return type via index.
			if cctx.Locals[i].Initializer != nil {
				if te := h.resolveVarInitializer(cctx.Locals[i].Initializer, cctx, resolver); te != nil {
					return te, false
				}
			}
		}
	}
	// Search params.
	for _, p := range cctx.Params {
		if p.Name == name {
			te := resolver.resolveParameterized(p.Type)
			return te, false
		}
	}
	// Search class fields.
	for _, f := range cctx.ClassFields {
		if f.Name == name {
			te := resolver.resolveParameterized(f.Type)
			return te, false
		}
	}
	// Maybe it's a class name (static access).
	if sym := resolver.resolve(name); sym != "" {
		return &index.TypeExpr{Sym: sym}, true
	}
	// Maybe it's a statically imported method/field.
	if te := h.resolveStaticImportType(name, cctx.Imports, resolver); te != nil {
		return te, false
	}
	return nil, false
}

// resolveStaticImportType resolves the return type of a statically imported method or
// the type of a statically imported field. For example, with
// "import static org.assertj.core.api.Assertions.assertThat", resolving "assertThat"
// returns the return type of Assertions.assertThat().
func (h *Handler) resolveStaticImportType(name string, imports []ImportSpec, resolver *typeResolver) *index.TypeExpr {
	h.logger.Printf("resolveStaticImportType: looking for %q in %d imports", name, len(imports))
	for _, imp := range imports {
		if !imp.Static {
			continue
		}
		if imp.Wildcard {
			// Wildcard: resolve the class and search all static members.
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
		} else {
			// Specific: check if the last segment matches the name.
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
	}
	h.logger.Printf("resolveStaticImportType: %q not resolved", name)
	return nil
}

// resolveStaticMemberType finds a static member by name on the given class and returns its type.
// Because a method may have many overloads (e.g. assertThat has 30+), and the first overload's
// return type may not have useful members, this tries all overloads and prefers the one whose
// return type actually has indexed members.
func (h *Handler) resolveStaticMemberType(classSym, memberName string, resolver *typeResolver) *index.TypeExpr {
	members := h.idx.MembersOfType(classSym)
	h.logger.Printf("resolveStaticMemberType: classSym=%s memberName=%q totalMembers=%d", classSym, memberName, len(members))

	var firstCandidate *index.TypeExpr
	for _, m := range members {
		if m.Name != memberName || !m.IsStatic {
			continue
		}

		// Collect all candidate return types from different sources for this overload.
		var candidates []*index.TypeExpr
		if retType := h.idx.DeclTypeOf(m.Symbol); retType != nil && !isTypeParamSymbol(retType.Sym) {
			candidates = append(candidates, retType)
		}
		if sym := h.idx.TypeOfSymbol(m.Symbol); sym != "" && !isTypeParamSymbol(sym) {
			candidates = append(candidates, &index.TypeExpr{Sym: sym})
		}
		if m.Signature != nil {
			// 1. Structured symbol (most accurate)
			if m.Signature.ReturnTypeSym != "" && !isTypeParamSymbol(m.Signature.ReturnTypeSym) {
				candidates = append(candidates, &index.TypeExpr{Sym: m.Signature.ReturnTypeSym})
			}
			// 2. Heuristic from label (fallback)
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

		// Prefer the candidate whose return type has indexed members.
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

// isTypeParamSymbol returns true if sym is a type parameter symbol (e.g. "com/example/Foo#[T]").
func isTypeParamSymbol(sym string) bool {
	return strings.Contains(sym, "#[")
}

func (h *Handler) resolveInferredLambdaParamType(paramIndex int, cctx *CompletionCtx, resolver *typeResolver) *index.TypeExpr {
	targetType := h.resolveCurrentArgumentTypeExpr(cctx, resolver)
	if targetType == nil {
		return nil
	}
	if te := lambdaParamTypeFromTargetType(targetType, paramIndex); te != nil {
		return te
	}
	return samParamTypeFromTargetType(targetType, paramIndex, h.idx, resolver)
}

func (h *Handler) resolveCurrentArgumentTypeExpr(cctx *CompletionCtx, resolver *typeResolver) *index.TypeExpr {
	if cctx.Call == nil {
		return nil
	}

	var ownerType *index.TypeExpr
	var syms []index.Symbol

	if cctx.Call.IsNewExpr {
		if sym := resolver.resolve(cctx.Call.Constructor); sym != "" {
			syms = h.findMembersByName(sym, cctx.Call.Constructor)
		}
	} else if cctx.Call.Receiver != "" {
		fakeCctx := &CompletionCtx{
			Receiver:       cctx.Call.Receiver,
			Locals:         cctx.Locals,
			LambdaParams:   cctx.LambdaParams,
			Params:         cctx.Params,
			ClassFields:    cctx.ClassFields,
			Imports:        cctx.Imports,
			Package:        cctx.Package,
			EnclosingClass: cctx.EnclosingClass,
		}
		ownerType, _ = h.resolveReceiverTypeExpr(fakeCctx, resolver)
		if ownerType != nil {
			syms = h.findMembersByName(ownerType.Sym, cctx.Call.MethodName)
		}
	} else if cctx.EnclosingClass != "" {
		classSym := resolver.resolve(cctx.EnclosingClass)
		if classSym != "" {
			syms = h.findMembersByName(classSym, cctx.Call.MethodName)
		}
	}

	paramIdx := cctx.Call.ParamIndex
	best := h.selectBestMethodOverload(syms, paramIdx+1, nil, resolver)
	if best == nil {
		return nil
	}

	te := h.methodParamTypeExpr(*best, paramIdx, resolver)
	if te == nil {
		return nil
	}
	if ownerType != nil {
		te = substituteTypeParams(te, ownerType, h.idx)
		te = substituteNamedTypeParams(te, ownerType, h.idx)
	}
	return te
}

func signatureParamTypeExpr(typeName string, resolver *typeResolver) *index.TypeExpr {
	typeName = normalizeTypeArgName(typeName)
	if typeName == "" {
		return nil
	}

	baseName, argNames := splitGenericName(typeName)
	baseName = normalizeTypeArgName(baseName)
	if baseName == "" {
		return nil
	}

	sym := resolver.resolve(baseName)
	if sym == "" {
		sym = baseName
	}

	te := &index.TypeExpr{Sym: sym}
	for _, arg := range argNames {
		argTE := signatureParamTypeExpr(arg, resolver)
		if argTE != nil {
			te.Args = append(te.Args, argTE)
		}
	}
	return te
}

func extractParamTypeName(paramLabel string) string {
	lastSpace := strings.LastIndex(paramLabel, " ")
	if lastSpace <= 0 {
		return ""
	}
	return strings.TrimSpace(paramLabel[:lastSpace])
}

func normalizeTypeArgName(name string) string {
	name = strings.TrimSpace(name)
	switch {
	case strings.HasPrefix(name, "? extends "):
		return strings.TrimSpace(strings.TrimPrefix(name, "? extends "))
	case strings.HasPrefix(name, "? super "):
		return strings.TrimSpace(strings.TrimPrefix(name, "? super "))
	case name == "?":
		return ""
	default:
		return name
	}
}

func substituteNamedTypeParams(te *index.TypeExpr, owner *index.TypeExpr, idx *index.Index) *index.TypeExpr {
	if te == nil || owner == nil || len(owner.Args) == 0 {
		return te
	}

	typeParams := idx.ClassTypeParams(owner.Sym)
	if len(typeParams) == 0 {
		return te
	}

	subst := make(map[string]*index.TypeExpr)
	for i, tp := range typeParams {
		if i >= len(owner.Args) {
			break
		}
		name := typeParamSimpleName(tp)
		if name != "" {
			subst[name] = owner.Args[i]
		}
	}
	if len(subst) == 0 {
		return te
	}
	return applyNamedSubstitution(te, subst)
}

func typeParamSimpleName(sym string) string {
	start := strings.LastIndexByte(sym, '[')
	end := strings.LastIndexByte(sym, ']')
	if start < 0 || end <= start+1 {
		return ""
	}
	return sym[start+1 : end]
}

func applyNamedSubstitution(te *index.TypeExpr, subst map[string]*index.TypeExpr) *index.TypeExpr {
	if te == nil {
		return nil
	}
	if resolved, ok := subst[te.Sym]; ok {
		return resolved
	}
	if len(te.Args) == 0 {
		return te
	}
	result := &index.TypeExpr{Sym: te.Sym, Args: make([]*index.TypeExpr, len(te.Args))}
	for i, arg := range te.Args {
		result.Args[i] = applyNamedSubstitution(arg, subst)
	}
	return result
}

func lambdaParamTypeFromTargetType(targetType *index.TypeExpr, paramIndex int) *index.TypeExpr {
	if targetType == nil {
		return nil
	}

	switch simplifyTypeName(targetType.Sym) {
	case "Function", "Consumer", "Predicate", "UnaryOperator", "ToIntFunction", "ToLongFunction", "ToDoubleFunction":
		if paramIndex == 0 && len(targetType.Args) >= 1 {
			return targetType.Args[0]
		}
	case "BiFunction", "BiConsumer", "BiPredicate":
		if paramIndex < 2 && len(targetType.Args) >= paramIndex+1 {
			return targetType.Args[paramIndex]
		}
	case "BinaryOperator":
		if paramIndex < 2 && len(targetType.Args) >= 1 {
			return targetType.Args[0]
		}
	case "Comparator":
		if paramIndex < 2 && len(targetType.Args) >= 1 {
			return targetType.Args[0]
		}
	}

	return nil
}

var objectMethodNames = map[string]struct{}{
	"clone":     {},
	"equals":    {},
	"finalize":  {},
	"getClass":  {},
	"hashCode":  {},
	"notify":    {},
	"notifyAll": {},
	"toString":  {},
	"wait":      {},
}

func samParamTypeFromTargetType(targetType *index.TypeExpr, paramIndex int, idx *index.Index, resolver *typeResolver) *index.TypeExpr {
	if targetType == nil || idx == nil {
		return nil
	}

	var candidates []index.Symbol
	seen := make(map[string]struct{})
	for _, m := range idx.MembersOfType(targetType.Sym) {
		if m.Kind != sdb.SymbolInformation_METHOD || m.IsStatic {
			continue
		}
		if _, skip := objectMethodNames[m.Name]; skip {
			continue
		}
		key := completionItemKeyForSymbol(m)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		candidates = append(candidates, m)
	}

	if len(candidates) != 1 {
		return nil
	}

	sam := candidates[0]
	if sam.Signature == nil || paramIndex >= len(sam.Signature.Params) {
		return nil
	}
	param := sam.Signature.Params[paramIndex]
	if te := signatureParamTypeExpr(param.Type, resolver); te != nil {
		return substituteNamedTypeParams(te, targetType, idx)
	}
	if param.TypeSym != "" {
		return &index.TypeExpr{Sym: param.TypeSym}
	}
	return nil
}

// resolveVarInitializer resolves the return type of a method call
// used as an initializer for a "var" declaration.
// Supports static calls (List.of(...)), instance calls (list.get(0)),
// and chained calls (builder.name("a").build()).
func (h *Handler) resolveVarInitializer(vi *VarInitializer, cctx *CompletionCtx, resolver *typeResolver) *index.TypeExpr {
	ownerType, result := h.resolveVarInitializerMethodCall(vi, cctx, resolver)
	if result == nil {
		return nil
	}
	return h.refineVarInitializerType(result, ownerType, vi, resolver)
}

func (h *Handler) resolveVarInitializerMethodCall(vi *VarInitializer, cctx *CompletionCtx, resolver *typeResolver) (*index.TypeExpr, *index.TypeExpr) {
	if vi == nil {
		return nil, nil
	}
	if vi.Receiver == "" {
		return h.resolveUnqualifiedVarInitializerMethodCall(vi, cctx, resolver)
	}
	fakeCctx := &CompletionCtx{
		Receiver:       vi.Receiver,
		Locals:         cctx.Locals,
		LambdaParams:   cctx.LambdaParams,
		Params:         cctx.Params,
		ClassFields:    cctx.ClassFields,
		Imports:        cctx.Imports,
		Package:        cctx.Package,
		EnclosingClass: cctx.EnclosingClass,
	}
	ownerType, _ := h.resolveReceiverTypeExpr(fakeCctx, resolver)
	if ownerType == nil {
		return nil, nil
	}
	return ownerType, h.resolveMemberTypeExpr(ownerType, vi.MethodName)
}

func (h *Handler) resolveUnqualifiedVarInitializerMethodCall(vi *VarInitializer, cctx *CompletionCtx, resolver *typeResolver) (*index.TypeExpr, *index.TypeExpr) {
	var ownerType *index.TypeExpr
	var candidates []index.Symbol

	if cctx.EnclosingClass != "" {
		if classSym := resolver.resolve(cctx.EnclosingClass); classSym != "" {
			ownerType = &index.TypeExpr{Sym: classSym}
			candidates = append(candidates, h.findMembersByName(classSym, vi.MethodName)...)
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
			candidates = append(candidates, h.findMembersByName(classSym, vi.MethodName)...)
			continue
		}
		lastDot := strings.LastIndex(imp.Path, ".")
		if lastDot < 0 || imp.Path[lastDot+1:] != vi.MethodName {
			continue
		}
		classFQN := imp.Path[:lastDot]
		classSym := resolver.resolve(simpleNameFromFQN(classFQN))
		if classSym == "" {
			classSym = fqnToSymbol(classFQN)
		}
		candidates = append(candidates, h.findMembersByName(classSym, vi.MethodName)...)
	}

	best := h.selectBestMethodOverload(candidates, len(vi.ArgTypes), vi.ArgTypes, resolver)
	if best == nil {
		return ownerType, nil
	}
	if te := h.idx.DeclTypeOf(best.Symbol); te != nil {
		if ownerType != nil {
			te = substituteTypeParams(te, ownerType, h.idx)
			te = substituteNamedTypeParams(te, ownerType, h.idx)
		}
		return ownerType, te
	}
	if sym := h.idx.TypeOfSymbol(best.Symbol); sym != "" {
		te := &index.TypeExpr{Sym: sym}
		if ownerType != nil {
			te = substituteTypeParams(te, ownerType, h.idx)
			te = substituteNamedTypeParams(te, ownerType, h.idx)
		}
		return ownerType, te
	}
	if best.Signature != nil && best.Signature.ReturnTypeSym != "" {
		return ownerType, &index.TypeExpr{Sym: best.Signature.ReturnTypeSym}
	}
	return ownerType, nil
}

func (h *Handler) refineVarInitializerType(te, ownerType *index.TypeExpr, vi *VarInitializer, resolver *typeResolver) *index.TypeExpr {
	if te == nil || vi == nil || len(te.Args) == 0 {
		return te
	}

	// Check if any type arg is unresolved.
	hasUnresolved := false
	for _, arg := range te.Args {
		if isUnresolvedTypeParam(arg) {
			hasUnresolved = true
			break
		}
	}
	if !hasUnresolved {
		return te
	}

	// Try generic inference: match actual arg types to formal param type parameters.
	if inferred := h.inferTypeParamsFromArgs(te, ownerType, vi, resolver); inferred != nil {
		return inferred
	}

	return te
}

// inferTypeParamsFromArgs infers type parameter bindings by matching actual argument
// types against formal parameter types, then substitutes them in the return type.
func (h *Handler) inferTypeParamsFromArgs(te, ownerType *index.TypeExpr, vi *VarInitializer, resolver *typeResolver) *index.TypeExpr {
	if len(vi.ArgTypes) == 0 {
		return nil
	}
	if ownerType == nil {
		return nil
	}

	method := h.selectBestMethodOverload(h.findMembersByName(ownerType.Sym, vi.MethodName), len(vi.ArgTypes), vi.ArgTypes, resolver)
	if method == nil {
		return nil
	}
	formalParams := h.methodParamTypes(*method, resolver)
	if len(formalParams) == 0 {
		return nil
	}

	// Build type parameter bindings: e.g. "java/util/List#[E]" → String#
	bindings := make(map[string]*index.TypeExpr)
	for i, formal := range formalParams {
		if i >= len(vi.ArgTypes) {
			break
		}
		actual := vi.ArgTypes[i]
		if actual == nil {
			continue
		}
		resolved := resolver.resolveParameterized(actual)
		if resolved == nil {
			resolved = actual
		}
		formal = substituteTypeParams(formal, ownerType, h.idx)
		formal = substituteNamedTypeParams(formal, ownerType, h.idx)
		collectTypeParamBindings(formal, resolved, bindings)
	}
	if len(bindings) == 0 {
		return nil
	}

	// Substitute unresolved type args using the inferred bindings.
	result := &index.TypeExpr{Sym: te.Sym, Args: make([]*index.TypeExpr, len(te.Args))}
	changed := false
	for i, arg := range te.Args {
		if isUnresolvedTypeParam(arg) {
			if bound, ok := bindings[arg.Sym]; ok {
				result.Args[i] = bound
				changed = true
				continue
			}
		}
		result.Args[i] = arg
	}
	if !changed {
		return nil
	}
	return result
}

// collectTypeParamBindings recursively matches a formal type against an actual type,
// collecting type parameter → concrete type mappings.
// e.g. formal={Sym:"java/util/List#[E]"} actual={Sym:"java/lang/String#"}
//
//	→ bindings["java/util/List#[E]"] = {Sym:"java/lang/String#"}
//
// e.g. formal={Sym:"java/util/List#", Args:[{Sym:"java/util/List#[K]"}]}
//
//	actual={Sym:"java/util/List#", Args:[{Sym:"java/lang/String#"}]}
//	→ bindings["java/util/List#[K]"] = {Sym:"java/lang/String#"}
func collectTypeParamBindings(formal, actual *index.TypeExpr, bindings map[string]*index.TypeExpr) {
	if formal == nil || actual == nil {
		return
	}
	if isUnresolvedTypeParam(formal) {
		if existing, ok := bindings[formal.Sym]; ok {
			// Consistency check: must unify to the same type.
			if !sameTypeExpr(existing, actual) {
				return
			}
		}
		bindings[formal.Sym] = actual
		return
	}
	// Recurse into type arguments if base types match.
	if formal.Sym == actual.Sym && len(formal.Args) > 0 && len(formal.Args) == len(actual.Args) {
		for i := range formal.Args {
			collectTypeParamBindings(formal.Args[i], actual.Args[i], bindings)
		}
	}
}

func isUnresolvedTypeParam(te *index.TypeExpr) bool {
	if te == nil {
		return false
	}
	if strings.Contains(te.Sym, "[") {
		return true
	}
	return len(te.Args) == 0 && len(te.Sym) == 1 && te.Sym[0] >= 'A' && te.Sym[0] <= 'Z'
}

func sameTypeExpr(a, b *index.TypeExpr) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Sym != b.Sym || len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Args {
		if !sameTypeExpr(a.Args[i], b.Args[i]) {
			return false
		}
	}
	return true
}

func (h *Handler) selectBestMethodOverload(candidates []index.Symbol, desiredArity int, actualArgTypes []*index.TypeExpr, resolver *typeResolver) *index.Symbol {
	var best *index.Symbol
	bestScore := -1 << 30

	for i := range candidates {
		candidate := candidates[i]
		score := 0

		paramTypes := h.methodParamTypes(candidate, resolver)
		paramCount, countKnown := h.methodParamCount(candidate, len(paramTypes))
		if desiredArity >= 0 && countKnown {
			if paramCount == desiredArity {
				score += 1000
			} else if paramCount < desiredArity {
				continue
			} else {
				score -= (paramCount - desiredArity) * 10
			}
		}
		if len(paramTypes) > 0 {
			score += 100
		}
		if actualArgTypes != nil && len(paramTypes) > 0 {
			for j := 0; j < len(actualArgTypes) && j < len(paramTypes); j++ {
				actual := actualArgTypes[j]
				if actual == nil {
					continue
				}
				resolved := resolver.resolveParameterized(actual)
				if resolved == nil {
					resolved = actual
				}
				switch {
				case sameTypeExpr(paramTypes[j], resolved):
					score += 20
				case isUnresolvedTypeParam(paramTypes[j]):
					score += 5
				case typeExprMatchesExpected(resolved, paramTypes[j]):
					score += 10
				}
			}
		}

		if best == nil || score > bestScore {
			best = &candidates[i]
			bestScore = score
		}
	}

	return best
}

func (h *Handler) methodParamCount(sym index.Symbol, knownTypes int) (int, bool) {
	if knownTypes > 0 {
		return knownTypes, true
	}
	if sym.Signature == nil {
		return 0, false
	}
	if n := len(sym.Signature.ParseParams()); n > 0 {
		return n, true
	}
	if !sym.Signature.HasParams {
		return 0, true
	}
	return 0, false
}

func (h *Handler) methodParamTypes(sym index.Symbol, resolver *typeResolver) []*index.TypeExpr {
	if pts := h.idx.DeclParamTypesOf(sym.Symbol); len(pts) > 0 {
		return pts
	}
	if sym.Signature == nil {
		return nil
	}

	var result []*index.TypeExpr
	for _, param := range sym.Signature.Params {
		if te := signatureParamTypeExpr(param.Type, resolver); te != nil {
			result = append(result, te)
			continue
		}
		if param.TypeSym != "" {
			result = append(result, &index.TypeExpr{Sym: param.TypeSym})
			continue
		}
		return nil
	}
	if len(result) > 0 {
		return result
	}

	for _, raw := range sym.Signature.ParseParams() {
		typeName := extractParamTypeName(raw)
		if typeName == "" {
			return nil
		}
		te := signatureParamTypeExpr(typeName, resolver)
		if te == nil {
			return nil
		}
		result = append(result, te)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func (h *Handler) methodParamTypeExpr(sym index.Symbol, paramIdx int, resolver *typeResolver) *index.TypeExpr {
	params := h.methodParamTypes(sym, resolver)
	if paramIdx < len(params) {
		return params[paramIdx]
	}
	return nil
}

// resolveMemberTypeExpr resolves the type of a member on a given type,
// performing generic type parameter substitution.
func (h *Handler) resolveMemberTypeExpr(owner *index.TypeExpr, memberName string) *index.TypeExpr {
	members := h.idx.MembersOfType(owner.Sym)
	for _, m := range members {
		if m.Name != memberName {
			continue
		}
		// Try structured type first (preserves generics).
		if retType := h.idx.DeclTypeOf(m.Symbol); retType != nil {
			return substituteTypeParams(retType, owner, h.idx)
		}
		// Fallback to flat symbolType.
		if sym := h.idx.TypeOfSymbol(m.Symbol); sym != "" {
			result := &index.TypeExpr{Sym: sym}
			return substituteTypeParams(result, owner, h.idx)
		}
		return nil
	}
	return nil
}

// substituteTypeParams replaces type parameter references in retType with
// actual type arguments from owner, traversing the inheritance chain to
// resolve type parameters declared on parent types.
//
// Example: owner = {Sym:"ArrayList#", Args:[{Sym:"String#"}]}
//
//	ArrayList# extends AbstractList<E>, AbstractList<E> extends List<E>
//	retType = {Sym: "List#[E]"}  (return type of List.get())
//	→ builds chain: ArrayList#[E]→String#, AbstractList#[E]→String#, List#[E]→String#
//	→ returns {Sym: "String#"}
func substituteTypeParams(retType *index.TypeExpr, owner *index.TypeExpr, idx *index.Index) *index.TypeExpr {
	if retType == nil || owner == nil || len(owner.Args) == 0 {
		return retType
	}

	subst := buildSubstitutionMap(owner, idx)
	if len(subst) == 0 {
		return retType
	}

	return applySubstitution(retType, subst)
}

// buildSubstitutionMap builds a map from type parameter symbols to concrete
// TypeExprs by starting with the owner's type params and walking the
// inheritance chain via ParentTypesOf.
func buildSubstitutionMap(owner *index.TypeExpr, idx *index.Index) map[string]*index.TypeExpr {
	subst := make(map[string]*index.TypeExpr)

	// Seed with the owner's own type params → args.
	typeParams := idx.ClassTypeParams(owner.Sym)
	for i, tp := range typeParams {
		if i < len(owner.Args) {
			subst[tp] = owner.Args[i]
		}
	}

	// BFS through parent types to propagate substitutions up the hierarchy.
	visited := make(map[string]bool)
	queue := []string{owner.Sym}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if visited[current] {
			continue
		}
		visited[current] = true

		for _, parentType := range idx.ParentTypesOf(current) {
			parentParams := idx.ClassTypeParams(parentType.Sym)
			for i, pp := range parentParams {
				if i < len(parentType.Args) {
					subst[pp] = applySubstitution(parentType.Args[i], subst)
				}
			}
			queue = append(queue, parentType.Sym)
		}
	}

	return subst
}

// applySubstitution replaces type parameter references in te using the
// substitution map, recursing into generic type arguments.
func applySubstitution(te *index.TypeExpr, subst map[string]*index.TypeExpr) *index.TypeExpr {
	if te == nil {
		return nil
	}

	if resolved, ok := subst[te.Sym]; ok {
		return resolved
	}

	if len(te.Args) > 0 {
		result := &index.TypeExpr{Sym: te.Sym, Args: make([]*index.TypeExpr, len(te.Args))}
		for i, arg := range te.Args {
			result.Args[i] = applySubstitution(arg, subst)
		}
		return result
	}

	return te
}

// completeLexical handles bare identifier completion (e.g. "pri").
func (h *Handler) completeLexical(cctx *CompletionCtx, fileURI string, content []byte) []CompletionItem {
	prefix := strings.ToLower(cctx.Prefix)
	if prefix == "" {
		return nil
	}

	// Resolve expected parameter type if cursor is inside a method call.
	resolver := &typeResolver{idx: h.idx, imports: cctx.Imports, pkg: cctx.Package}
	expectedType := h.resolveCurrentArgumentTypeExpr(cctx, resolver)

	seen := make(map[string]struct{})
	seenTypeSymbols := make(map[string]struct{})
	var items []CompletionItem

	// Add static snippets first.
	items = append(items, h.completeSnippets(cctx)...)

	// matchQuality returns a single-char score for how well name matches the query.
	// "0" = exact, "1" = prefix, "2" = word-start/camelCase, "3" = substring, "4" = fuzzy.
	matchQuality := func(name string) string {
		score := index.CompletionMatchScore(name, cctx.Prefix)
		return fmt.Sprintf("%d", score)
	}

	contextPrefix := func(kind int) string {
		if !cctx.InTypePosition {
			return "1"
		}
		if isTypeCompletionKind(kind) {
			return "0"
		}
		// Declaration-head completions often mix modifiers and types
		// ("public sta", "final Str"). Keep keywords in the same top bucket as
		// types so prefix quality and scope order can decide between them.
		if kind == CompletionKindKeyword {
			return "0"
		}
		return "2"
	}

	addItem := func(name string, kind int, detail string, candidateType *index.TypeExpr, scopeOrder string) {
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		item := CompletionItem{
			Label:      name,
			Kind:       kind,
			InsertText: name,
			Detail:     detail,
			SortText:   contextPrefix(kind) + completionTypeMatchPrefix(h, expectedType, candidateType) + matchQuality(name) + scopeOrder + completionNameSortKey(name),
			FilterText: name,
		}
		items = append(items, item)
	}

	matchName := func(name string) bool {
		return index.FuzzyMatch(name, cctx.Prefix)
	}

	// 1. Lambda parameters (scope "00").
	for i := len(cctx.LambdaParams) - 1; i >= 0; i-- {
		p := cctx.LambdaParams[i]
		if matchName(p.Name) {
			addItem(p.Name, CompletionKindVariable, p.Type.String(), p.Type, "00")
		}
	}

	// 2. Local variables (scope "01").
	for i := len(cctx.Locals) - 1; i >= 0; i-- {
		l := cctx.Locals[i]
		if matchName(l.Name) {
			addItem(l.Name, CompletionKindVariable, l.Type.String(), l.Type, "01")
		}
	}

	// 3. Method parameters (scope "02").
	for _, p := range cctx.Params {
		if matchName(p.Name) {
			addItem(p.Name, CompletionKindVariable, p.Type.String(), p.Type, "02")
		}
	}

	// 4. Class fields (scope "03").
	for _, f := range cctx.ClassFields {
		if matchName(f.Name) {
			addItem(f.Name, CompletionKindField, f.Type.String(), f.Type, "03")
		}
	}

	// 5. Class methods (scope "04").
	for _, m := range cctx.ClassMethods {
		if matchName(m.Name) {
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
	}

	// 5b. Static imports (scope "5").
	// For "import static org.assertj.core.api.Assertions.assertThat" → offer assertThat().
	// For "import static org.assertj.core.api.Assertions.*" → offer all static members.
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
			// Wildcard static import: resolve the class and offer all static members.
			classFQN := strings.TrimSuffix(imp.Path, ".*")
			classSym := resolver.resolve(simpleNameFromFQN(classFQN))
			if classSym == "" {
				classSym = fqnToSymbol(classFQN)
			}
			addStaticMembers(classSym, "")
		} else {
			// Specific static import: extract the member name from the FQN.
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
	}

	// 6. Keywords and Literals (scope "07").
	// Keywords use prefix-only matching to avoid noise from fuzzy matches.
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

	// 6. Global type and symbol completion from the index.
	symbols := h.idx.CompletionSymbols(fileURI, cctx.Prefix)
	for _, s := range symbols {
		if len(items) >= 100 {
			break
		}
		scopeOrder := "10" // default: other symbol
		if s.SameFile {
			if isTypeCompletionKind(sdbKindToCompletionKind(s.Kind)) {
				scopeOrder = "06" // same-file type
			} else {
				scopeOrder = "09" // same-file other
			}
		} else {
			if isTypeCompletionKind(sdbKindToCompletionKind(s.Kind)) {
				scopeOrder = "08" // other type
			}
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

		// When completing after "new", add parentheses for type completions.
		if cctx.AfterNew && isTypeCompletionKind(kind) && !cctx.ParenFollows {
			item.InsertText = s.Name + "($0)"
			item.InsertTextFormat = InsertTextFormatSnippet
		}

		// Auto-import for type symbols from other packages.
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

// symbolReturnTypeExpr returns the structured return/field type for a symbol.
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

// typeExprMatchesExpected checks if a candidate TypeExpr matches the expected TypeExpr
// using exact match or base symbol comparison.
func typeExprMatchesExpected(candidate, expected *index.TypeExpr) bool {
	if candidate == nil || expected == nil {
		return false
	}
	if sameTypeExpr(candidate, expected) {
		return true
	}
	// Compare base symbol only (ignoring generic args) as a weak match.
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

// simplifyTypeName extracts the simple class name from a potentially qualified name.
// e.g. "java/lang/String#" → "String", "java.util.List" → "List"
func simplifyTypeName(name string) string {
	name = strings.TrimSuffix(name, "#")
	name = strings.TrimSuffix(name, ".")
	if idx := strings.LastIndexAny(name, "/."); idx >= 0 {
		return name[idx+1:]
	}
	return name
}

// methodCompletionItem builds a CompletionItem for a member (method or field).
// Methods get snippet format with parentheses; fields get plain text.
// When parenFollows is true, parentheses are already present after the cursor
// and will not be inserted.
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

// overloadDetail increments the overload count in a detail string like "void foo(int) (+1 overload)".
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

func (h *Handler) handleSignatureHelp(ctx context.Context, params json.RawMessage) (any, error) {
	var p SignatureHelpParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if !h.waitIndexReady(ctx) {
		return nil, nil
	}

	syms := h.idx.SymbolSignatures(p.TextDocument.URI, p.Position.Line, p.Position.Character)

	// Fallback: when SemanticDB has no occurrence (uncompiled code),
	// use Tree-sitter to find the enclosing method call and resolve signatures.
	if len(syms) == 0 {
		syms = h.resolveSignatureFromAST(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	}
	if len(syms) == 0 {
		return nil, nil
	}

	var sigs []SignatureInformation
	for i := range syms {
		si := formatSignatureHelp(&syms[i])
		if si != nil {
			sigs = append(sigs, *si)
		}
	}
	if len(sigs) == 0 {
		return nil, nil
	}

	activeParam := h.countActiveParameter(p.TextDocument.URI, p.Position.Line, p.Position.Character)

	// Pick the best overload: first signature whose param count >= activeParam+1.
	activeSig := 0
	for i, sig := range sigs {
		if len(sig.Parameters) >= activeParam+1 {
			activeSig = i
			break
		}
	}

	return SignatureHelp{
		Signatures:      sigs,
		ActiveSignature: activeSig,
		ActiveParameter: activeParam,
	}, nil
}

// wordPrefixAt extracts the Java identifier prefix ending at the given cursor
// position from the overlay (or disk). E.g. if the line is "  ArrayLi|" with
// cursor at position 9, it returns "ArrayLi".
func (h *Handler) wordPrefixAt(fileURI string, line, character int) string {
	content := h.getFileContent(fileURI)
	if content == "" {
		return ""
	}

	contentBytes := []byte(content)
	byteOff := PositionToByteOffset(contentBytes, line, character)

	// Walk backwards from cursor to find the start of the identifier.
	start := byteOff
	for start > 0 {
		ch := contentBytes[start-1]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '$' {
			start--
		} else {
			break
		}
	}

	return string(contentBytes[start:byteOff])
}

// resolveSignatureFromAST uses Tree-sitter to find the enclosing method call
// and resolves its signatures via the index, enabling signature help for
// uncompiled code that lacks SemanticDB occurrences.
func (h *Handler) resolveSignatureFromAST(fileURI string, line, character int) []index.Symbol {
	content := h.getFileContent(fileURI)
	if content == "" {
		return nil
	}

	src := []byte(content)
	tree, err := getTree(src)
	if err != nil {
		return nil
	}

	node := nodeAtPosition(tree.RootNode(), line, character)
	if node == nil {
		return nil
	}

	// Walk up to find the enclosing method_invocation or object_creation_expression.
	callNode := node
	for callNode != nil {
		switch callNode.Type() {
		case "method_invocation", "object_creation_expression":
			goto found
		}
		callNode = callNode.Parent()
	}
	return nil

found:
	// Parse completion context at the call site for type resolution.
	cctx := parseCompletionCtx(h.logger, src, line, character)
	resolver := &typeResolver{idx: h.idx, imports: cctx.Imports, pkg: cctx.Package}

	if callNode.Type() == "object_creation_expression" {
		// new Foo(...) → look up constructors of Foo.
		te := extractTypeFromNewExpr(callNode, src)
		if te == nil {
			return nil
		}
		if sym := resolver.resolve(te.Sym); sym != "" {
			return h.findMembersByName(sym, te.Sym)
		}
		return nil
	}

	// method_invocation: extract receiver (object) and method name.
	nameNode := callNode.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	methodName := nameNode.Content(src)

	objNode := callNode.ChildByFieldName("object")
	if objNode == nil {
		// Simple call without receiver (e.g. "doWork(...)") → search enclosing class.
		if cctx.EnclosingClass == "" {
			return nil
		}
		classSym := resolver.resolve(cctx.EnclosingClass)
		if classSym == "" {
			return nil
		}
		return h.findMembersByName(classSym, methodName)
	}

	// Has a receiver: build a fake CompletionCtx to reuse receiver resolution.
	recvText := exprToReceiver(objNode, src)
	if recvText == "" {
		return nil
	}
	cctx.Receiver = recvText
	typeExpr, _ := h.resolveReceiverTypeExpr(cctx, resolver)
	if typeExpr == nil {
		return nil
	}
	return h.findMembersByName(typeExpr.Sym, methodName)
}

// findMembersByName returns all members of a type matching the given name.
func (h *Handler) findMembersByName(typeSym, name string) []index.Symbol {
	members := h.idx.MembersOfType(typeSym)
	var result []index.Symbol
	for _, m := range members {
		if m.Name == name {
			result = append(result, m)
		}
	}
	return result
}

// countActiveParameter uses the Tree-sitter AST to find the enclosing
// argument_list and counts which argument the cursor is in.
func (h *Handler) countActiveParameter(fileURI string, line, character int) int {
	content := h.getFileContent(fileURI)
	if content == "" {
		return 0
	}

	src := []byte(content)
	parser := javaParserPool.Get().(*slog.Parser)
	defer javaParserPool.Put(parser)

	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return 0
	}

	node := nodeAtPosition(tree.RootNode(), line, character)
	if node == nil {
		return 0
	}

	// Find the enclosing argument_list node.
	argList := node
	for argList != nil && argList.Type() != "argument_list" {
		argList = argList.Parent()
	}
	if argList == nil {
		return 0
	}

	// Count commas before the cursor to determine the active parameter index.
	cursorByte := PositionToByteOffset(src, line, character)
	active := 0
	for i := 0; i < int(argList.ChildCount()); i++ {
		child := argList.Child(i)
		if int(child.StartByte()) >= cursorByte {
			break
		}
		if child.Type() == "," {
			active++
		}
	}
	return active
}
