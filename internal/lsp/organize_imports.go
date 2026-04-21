package lsp

import (
	"sort"
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	slog "github.com/smacker/go-tree-sitter"
)

// importBlock records the line range of the import section in a Java file.
type importBlock struct {
	startLine     int      // first import line (0-indexed)
	endLine       int      // line after the last import (0-indexed, exclusive)
	imports       []string // fully-qualified import strings (e.g. "java.util.List")
	staticImports []string // fully-qualified static import strings (e.g. "org.junit.Assert.assertEquals")
}

// organizeImports computes a WorkspaceEdit that removes unused imports,
// adds missing ones, and sorts the remainder.
// If overlay is non-empty it is used as the file content instead of reading from disk.
func organizeImports(fileURI string, idx *index.Index, overlay string) *WorkspaceEdit {
	return organizeImportsWithOverlay(fileURI, idx, overlay, overlay != "")
}

func organizeImportsWithOverlay(fileURI string, idx *index.Index, overlay string, hasOverlay bool) *WorkspaceEdit {
	content := readContent(fileURI, overlay, hasOverlay)
	if content == nil {
		return nil
	}

	tree, err := getTree(content)
	if err != nil {
		return nil
	}
	root := tree.RootNode()

	block := parseImportBlock(root, content)
	filePackage := detectPackage(root, content)

	// Gather all SemanticDB symbols referenced in this file.
	occs := idx.AllFileOccurrences(fileURI)
	usedSymbols := make(map[string]bool, len(occs))
	for _, occ := range occs {
		usedSymbols[occ.Symbol] = true
	}
	collectOverrideMethodTypeSymbols(root, content, fileURI, idx, usedSymbols)

	// Determine which simple names are actually used in the file.
	usedSimpleNames := make(map[string]bool)
	resolvedTypeNames := make(map[string]bool)
	for sym := range usedSymbols {
		if name := simpleNameFromSymbol(sym); name != "" {
			usedSimpleNames[name] = true
		}
		if fqn := fqnFromSymbol(sym); fqn != "" {
			resolvedTypeNames[simpleNameFromImport(fqn)] = true
		}
	}
	for _, imp := range block.imports {
		simple := simpleNameFromImport(imp)
		if simple == "" || simple == "*" {
			continue
		}
		if !isKnownImportedType(idx, simple, imp) {
			resolvedTypeNames[simple] = true
		}
	}
	preferredPkgs := collectPreferredImportPackages(block, usedSymbols)
	fallbackResolvedSymbols := make(map[string]bool)

	// Supplement SemanticDB data with tree-sitter type identifiers. This keeps
	// organize-imports working when the file has compile errors or when SemanticDB
	// was produced only partially and misses some unresolved type references.
	typeNames := collectTypeIdentifiers(root, content)

	for {
		progress := false
		for name := range typeNames {
			if resolvedTypeNames[name] {
				continue
			}
			sym := resolveTypeNameForImport(name, idx, filePackage, preferredPkgs)
			if sym == "" {
				continue
			}
			usedSymbols[sym] = true
			fallbackResolvedSymbols[sym] = true
			usedSimpleNames[name] = true
			resolvedTypeNames[name] = true
			if pkg := packageFromFQN(fqnFromSymbol(sym)); pkg != "" {
				preferredPkgs[pkg] = true
			}
			progress = true
		}
		if !progress {
			break
		}
	}

	// Collect wildcard-imported packages (e.g. "java.util" from "java.util.*").
	wildcardPkgs := make(map[string]bool)
	for _, imp := range block.imports {
		if simpleNameFromImport(imp) == "*" {
			wildcardPkgs[packageFromFQN(imp)] = true
		}
	}

	// Filter existing imports: keep wildcards and used specific imports.
	// Drop specific imports whose package is already covered by a wildcard.
	var kept []string
	seenImports := make(map[string]bool, len(block.imports))
	resolvedBySimpleName := make(map[string]map[string]bool)
	for sym := range usedSymbols {
		if !isImportVisibleSymbol(idx, sym, filePackage) {
			continue
		}
		fqn := fqnFromSymbol(sym)
		if fqn == "" {
			continue
		}
		simple := simpleNameFromImport(fqn)
		if resolvedBySimpleName[simple] == nil {
			resolvedBySimpleName[simple] = make(map[string]bool)
		}
		resolvedBySimpleName[simple][fqn] = true
	}
	for _, imp := range block.imports {
		if seenImports[imp] {
			continue
		}
		simple := simpleNameFromImport(imp)
		if simple == "*" {
			kept = append(kept, imp)
			seenImports[imp] = true
			continue
		}
		if wildcardPkgs[packageFromFQN(imp)] {
			// Already covered by a wildcard import — skip.
			continue
		}
		if resolved := resolvedBySimpleName[simple]; len(resolved) > 0 {
			if resolved[imp] {
				kept = append(kept, imp)
				seenImports[imp] = true
			}
			continue
		}
		if typeNames[simple] && !isKnownImportedType(idx, simple, imp) {
			kept = append(kept, imp)
			seenImports[imp] = true
			continue
		}
		if usedSimpleNames[simple] {
			kept = append(kept, imp)
			seenImports[imp] = true
		}
	}

	// Find missing imports: symbols used that have a package prefix,
	// are not in the current file's package, and have no matching import.
	importedSet := make(map[string]bool, len(kept))
	for _, imp := range kept {
		importedSet[imp] = true
	}

	for sym := range usedSymbols {
		if !isImportVisibleSymbol(idx, sym, filePackage) {
			continue
		}
		fqn := fqnFromSymbol(sym)
		if fqn == "" {
			continue
		}
		pkg := packageFromFQN(fqn)
		if pkg == "" || pkg == "java.lang" || pkg == filePackage {
			continue
		}
		// Skip if a wildcard already covers this package.
		if wildcardPkgs[pkg] {
			continue
		}
		if !importedSet[fqn] {
			// Only add if the symbol has a known definition.
			if def := idx.SymbolDefinition(sym); def != nil || fallbackResolvedSymbols[sym] {
				kept = append(kept, fqn)
				importedSet[fqn] = true
			}
		}
	}

	// Sort imports: java.* first, javax.* second, then everything else alphabetically.
	sort.Slice(kept, func(i, j int) bool {
		return importSortKey(kept[i]) < importSortKey(kept[j])
	})

	// Filter static imports: keep only those whose member name appears in the source.
	usedIdents := collectIdentifiers(root, content)
	var keptStatic []string
	seenStaticImports := make(map[string]bool, len(block.staticImports))
	for _, imp := range block.staticImports {
		if seenStaticImports[imp] {
			continue
		}
		member := simpleNameFromImport(imp)
		if member == "*" || usedIdents[member] {
			keptStatic = append(keptStatic, imp)
			seenStaticImports[imp] = true
		}
	}
	for _, imp := range collectMissingStaticMethodImports(root, content, fileURI, idx, block, filePackage) {
		if seenStaticImports[imp] {
			continue
		}
		keptStatic = append(keptStatic, imp)
		seenStaticImports[imp] = true
	}
	for _, imp := range collectMissingStaticFieldImports(root, content, fileURI, idx, block, filePackage) {
		if seenStaticImports[imp] {
			continue
		}
		keptStatic = append(keptStatic, imp)
		seenStaticImports[imp] = true
	}

	// Build the replacement text.
	if len(kept) == 0 && len(block.imports) == 0 && len(block.staticImports) == 0 && len(keptStatic) == 0 {
		return nil
	}

	var sb strings.Builder

	// Emit static imports first (filtered and sorted).
	if len(keptStatic) > 0 {
		sorted := make([]string, len(keptStatic))
		copy(sorted, keptStatic)
		sort.Strings(sorted)
		for _, imp := range sorted {
			sb.WriteString("import static ")
			sb.WriteString(imp)
			sb.WriteString(";\n")
		}
		if len(kept) > 0 {
			sb.WriteByte('\n')
		}
	}

	prevGroup := -1
	for _, imp := range kept {
		group := importGroup(imp)
		if prevGroup != -1 && group != prevGroup {
			sb.WriteByte('\n')
		}
		sb.WriteString("import ")
		sb.WriteString(imp)
		sb.WriteString(";\n")
		prevGroup = group
	}

	// If there were no imports before and we're adding some, ensure a blank line before.
	newText := sb.String()
	if len(block.imports) == 0 && len(block.staticImports) == 0 && (len(kept) > 0 || len(keptStatic) > 0) {
		// If we are at the very beginning of the file (no package), no need for leading newline.
		if block.startLine > 0 {
			newText = "\n" + newText
		}
	}

	return singleFileEdit(fileURI, Range{
		Start: Position{Line: block.startLine, Character: 0},
		End:   Position{Line: block.endLine, Character: 0},
	}, newText)
}

func detectPackageForFile(fileURI string, overlay string, hasOverlay bool) string {
	content := readContent(fileURI, overlay, hasOverlay)
	if content == nil {
		return ""
	}
	tree, err := getTree(content)
	if err != nil {
		return ""
	}
	return detectPackage(tree.RootNode(), content)
}

func collectOverrideMethodTypeSymbols(root *slog.Node, content []byte, fileURI string, idx *index.Index, usedSymbols map[string]bool) {
	for _, classInfo := range collectClassMethodDecls(root, content) {
		classSym, found := findClassSymbol(fileURI, classInfo.name, idx)
		if !found {
			continue
		}
		collectOverrideMethodTypeSymbolsForClass(classSym.Symbol, classInfo.methods, idx, usedSymbols)
	}
}

type classMethodDecls struct {
	name    string
	methods []methodDeclInfo
}

type methodDeclInfo struct {
	name       string
	paramCount int
}

func collectClassMethodDecls(root *slog.Node, content []byte) []classMethodDecls {
	var result []classMethodDecls
	var walk func(*slog.Node)
	walk = func(n *slog.Node) {
		if n == nil {
			return
		}
		if n.Type() == "class_declaration" {
			if info := collectClassMethodDeclsFromNode(n, content); info.name != "" {
				result = append(result, info)
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return result
}

func collectClassMethodDeclsFromNode(classNode *slog.Node, content []byte) classMethodDecls {
	var info classMethodDecls
	for i := 0; i < int(classNode.NamedChildCount()); i++ {
		child := classNode.NamedChild(i)
		switch child.Type() {
		case "identifier":
			if info.name == "" {
				info.name = child.Content(content)
			}
		case "class_body":
			info.methods = collectMethodDeclsFromBody(child, content)
		}
	}
	return info
}

func collectMethodDeclsFromBody(body *slog.Node, content []byte) []methodDeclInfo {
	var methods []methodDeclInfo
	for i := 0; i < int(body.NamedChildCount()); i++ {
		child := body.NamedChild(i)
		if child.Type() != "method_declaration" {
			continue
		}
		if method := collectMethodDeclInfo(child, content); method.name != "" {
			methods = append(methods, method)
		}
	}
	return methods
}

func collectMethodDeclInfo(methodNode *slog.Node, content []byte) methodDeclInfo {
	var info methodDeclInfo
	for i := 0; i < int(methodNode.NamedChildCount()); i++ {
		child := methodNode.NamedChild(i)
		switch child.Type() {
		case "identifier":
			if info.name == "" {
				info.name = child.Content(content)
			}
		case "formal_parameters":
			info.paramCount = countMethodParameters(child)
		}
	}
	return info
}

func countMethodParameters(paramsNode *slog.Node) int {
	count := 0
	for i := 0; i < int(paramsNode.NamedChildCount()); i++ {
		child := paramsNode.NamedChild(i)
		if child.Type() == "formal_parameter" || child.Type() == "spread_parameter" {
			count++
		}
	}
	return count
}

func collectOverrideMethodTypeSymbolsForClass(classSym string, methods []methodDeclInfo, idx *index.Index, usedSymbols map[string]bool) {
	if classSym == "" || len(methods) == 0 {
		return
	}
	visitedParents := make(map[string]bool)
	for _, parentType := range parentTypesForStub(classSym, idx) {
		collectOverrideMethodTypeSymbolsFromParent(parentType, methods, idx, usedSymbols, visitedParents)
	}
}

func collectOverrideMethodTypeSymbolsFromParent(parentType *index.TypeExpr, methods []methodDeclInfo, idx *index.Index, usedSymbols map[string]bool, visited map[string]bool) {
	if parentType == nil || parentType.Sym == "" || visited[parentType.Sym] {
		return
	}
	visited[parentType.Sym] = true

	for _, method := range idx.DirectMembersOfType(parentType.Sym) {
		if method.Kind != sdb.SymbolInformation_METHOD || method.IsStatic {
			continue
		}
		if !matchesOverrideCandidate(method, methods, idx) {
			continue
		}
		if ret := idx.DeclTypeOf(method.Symbol); ret != nil {
			addTypeExprSymbols(usedSymbols, substituteTypeParams(ret, parentType, idx))
		} else if method.Signature != nil && method.Signature.ReturnTypeSym != "" {
			usedSymbols[method.Signature.ReturnTypeSym] = true
		}
		if pts := idx.DeclParamTypesOf(method.Symbol); len(pts) > 0 {
			for _, pt := range pts {
				addTypeExprSymbols(usedSymbols, substituteTypeParams(pt, parentType, idx))
			}
		} else if method.Signature != nil {
			for _, p := range method.Signature.Params {
				if p.TypeSym != "" {
					usedSymbols[p.TypeSym] = true
				}
			}
		}
	}

	for _, next := range parentTypesForStub(parentType.Sym, idx) {
		collectOverrideMethodTypeSymbolsFromParent(next, methods, idx, usedSymbols, visited)
	}
}

func matchesOverrideCandidate(method index.Symbol, methods []methodDeclInfo, idx *index.Index) bool {
	methodArity := -1
	if pts := idx.DeclParamTypesOf(method.Symbol); len(pts) > 0 {
		methodArity = len(pts)
	} else if method.Signature != nil {
		methodArity = len(method.Signature.Params)
		if methodArity == 0 && method.Signature.HasParams {
			methodArity = len(method.Signature.ParseParams())
		}
	}

	for _, decl := range methods {
		if decl.name != method.Name {
			continue
		}
		if methodArity >= 0 && decl.paramCount != methodArity {
			continue
		}
		return true
	}
	return false
}

func addTypeExprSymbols(usedSymbols map[string]bool, te *index.TypeExpr) {
	if te == nil {
		return
	}
	if te.Sym != "" {
		usedSymbols[te.Sym] = true
	}
	for _, arg := range te.Args {
		addTypeExprSymbols(usedSymbols, arg)
	}
}

func collectPreferredImportPackages(block importBlock, usedSymbols map[string]bool) map[string]bool {
	preferred := make(map[string]bool)
	for _, imp := range block.imports {
		if imp == "" {
			continue
		}
		pkg := packageFromFQN(strings.TrimSuffix(imp, ".*"))
		if strings.HasSuffix(imp, ".*") {
			pkg = strings.TrimSuffix(imp, ".*")
		}
		if pkg != "" {
			preferred[pkg] = true
		}
	}
	for sym := range usedSymbols {
		if pkg := packageFromFQN(fqnFromSymbol(sym)); pkg != "" {
			preferred[pkg] = true
		}
	}
	return preferred
}

var preferredJDKTypeSymbols = map[string]string{
	"List":          "java/util/List#",
	"Map":           "java/util/Map#",
	"Set":           "java/util/Set#",
	"Collection":    "java/util/Collection#",
	"Collections":   "java/util/Collections#",
	"Optional":      "java/util/Optional#",
	"ArrayList":     "java/util/ArrayList#",
	"HashMap":       "java/util/HashMap#",
	"HashSet":       "java/util/HashSet#",
	"LocalDate":     "java/time/LocalDate#",
	"LocalDateTime": "java/time/LocalDateTime#",
	"Instant":       "java/time/Instant#",
	"Duration":      "java/time/Duration#",
	"Path":          "java/nio/file/Path#",
	"Paths":         "java/nio/file/Paths#",
}

func resolveTypeNameForImport(name string, idx *index.Index, filePackage string, preferredPkgs map[string]bool) string {
	candidates := idx.TypeBySimpleName(name)
	if len(candidates) == 0 {
		return ""
	}

	filtered := make([]index.Symbol, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, sym := range candidates {
		if sym.Name != name || !index.IsTypeSymbol(sym) || seen[sym.Symbol] {
			continue
		}
		if !isImportVisibleType(sym, filePackage) {
			continue
		}
		seen[sym.Symbol] = true
		filtered = append(filtered, sym)
	}
	if len(filtered) == 0 {
		return ""
	}

	if len(filtered) == 1 {
		return filtered[0].Symbol
	}

	if sym := uniqueCandidateMatching(filtered, func(sym index.Symbol) bool {
		return packageFromFQN(fqnFromSymbol(sym.Symbol)) == filePackage
	}); sym != "" {
		return sym
	}

	if sym := uniqueCandidateMatching(filtered, isJavaLangType); sym != "" {
		return sym
	}

	if preferred, ok := preferredJDKTypeSymbols[name]; ok {
		for _, sym := range filtered {
			if sym.Symbol == preferred {
				return sym.Symbol
			}
		}
	}

	if sym := uniqueCandidateMatching(filtered, isWorkspaceType); sym != "" {
		return sym
	}

	bestScore := 0
	bestSym := ""
	tied := false
	for _, sym := range filtered {
		pkg := packageFromFQN(fqnFromSymbol(sym.Symbol))
		score := bestPackageAffinity(pkg, preferredPkgs)
		if score > bestScore {
			bestScore = score
			bestSym = sym.Symbol
			tied = false
			continue
		}
		if score == bestScore && score > 0 && bestSym != "" && bestSym != sym.Symbol {
			tied = true
		}
	}
	if bestScore > 0 && !tied {
		return bestSym
	}
	return ""
}

func isImportVisibleType(sym index.Symbol, filePackage string) bool {
	if !index.IsTypeSymbol(sym) {
		return false
	}

	// External/JDK types don't currently track visibility in the index; treat them
	// as importable and let source availability decide the rest.
	if sym.URI == "" {
		return true
	}

	typePkg := packageFromSymbol(sym.Symbol)
	switch sym.Visibility {
	case index.VisibilityPublic, index.VisibilityUnknown:
		return true
	case index.VisibilityProtected:
		return typePkg != "" && typePkg == filePackage
	case index.VisibilityPackagePrivate:
		return true
	case index.VisibilityPrivate:
		return false
	default:
		return false
	}
}

func isImportVisibleSymbol(idx *index.Index, sym string, filePackage string) bool {
	if sym == "" {
		return false
	}
	if def := idx.SymbolDefinition(sym); def != nil {
		return isImportVisibleType(*def, filePackage)
	}
	return true
}

func isKnownImportedType(idx *index.Index, simpleName, importPath string) bool {
	if idx == nil || simpleName == "" || importPath == "" {
		return false
	}
	for _, sym := range idx.TypeBySimpleName(simpleName) {
		if fqnFromSymbol(sym.Symbol) == importPath {
			return true
		}
	}
	return false
}

func uniqueCandidateMatching(candidates []index.Symbol, match func(index.Symbol) bool) string {
	found := ""
	for _, sym := range candidates {
		if !match(sym) {
			continue
		}
		if found != "" && found != sym.Symbol {
			return ""
		}
		found = sym.Symbol
	}
	return found
}

func isJavaLangType(sym index.Symbol) bool {
	return strings.HasPrefix(fqnFromSymbol(sym.Symbol), "java.lang.")
}

func isWorkspaceType(sym index.Symbol) bool {
	return sym.URI != ""
}

func bestPackageAffinity(pkg string, preferredPkgs map[string]bool) int {
	best := 0
	for preferred := range preferredPkgs {
		if preferred == "" {
			continue
		}
		if score := packageAffinityScore(pkg, preferred); score > best {
			best = score
		}
	}
	return best
}

func packageAffinityScore(a, b string) int {
	if a == "" || b == "" {
		return 0
	}
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	limit := len(aParts)
	if len(bParts) < limit {
		limit = len(bParts)
	}
	score := 0
	for i := 0; i < limit; i++ {
		if aParts[i] != bParts[i] {
			break
		}
		score++
	}
	return score
}

// parseImportBlock finds the contiguous import region in a Java source file using AST.
func parseImportBlock(root *slog.Node, content []byte) importBlock {
	var block importBlock
	block.startLine = -1

	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		if child.Type() == "import_declaration" {
			spec := parseImport(child, content)
			if spec.Path == "" {
				continue
			}

			line := int(child.StartPoint().Row)
			if block.startLine == -1 {
				block.startLine = line
			}
			if spec.Static {
				block.staticImports = append(block.staticImports, spec.Path)
			} else {
				block.imports = append(block.imports, spec.Path)
			}
			block.endLine = int(child.EndPoint().Row) + 1
		}
	}

	if block.startLine == -1 {
		// No imports found. Insert after the package declaration (or at top).
		pkgNode := findChildByType(root, "package_declaration")
		if pkgNode != nil {
			block.startLine = int(pkgNode.EndPoint().Row) + 1
			block.endLine = block.startLine
		} else {
			block.startLine = 0
			block.endLine = 0
		}
	}

	return block
}

// detectPackage returns the package name from the Java source using AST.
func detectPackage(root *slog.Node, content []byte) string {
	pkgNode := findChildByType(root, "package_declaration")
	if pkgNode == nil {
		return ""
	}
	// The package name is in a scoped_identifier or identifier child.
	for j := 0; j < int(pkgNode.NamedChildCount()); j++ {
		gc := pkgNode.NamedChild(j)
		if gc.Type() == "scoped_identifier" || gc.Type() == "identifier" {
			return gc.Content(content)
		}
	}
	return ""
}

// simpleNameFromImport extracts "List" from "java.util.List".
func simpleNameFromImport(imp string) string {
	if idx := strings.LastIndex(imp, "."); idx >= 0 {
		return imp[idx+1:]
	}
	return imp
}

// simpleNameFromSymbol extracts a simple class/interface name from a SemanticDB symbol.
// e.g. "java/util/List#" -> "List", "com/example/Foo#bar()." -> "Foo",
// "com/example/Outer#Inner#" -> "Inner"
func simpleNameFromSymbol(sym string) string {
	if !strings.HasSuffix(sym, "#") {
		// It's a member or something else. Strip everything after the last #.
		hashIdx := strings.LastIndex(sym, "#")
		if hashIdx < 0 {
			return ""
		}
		sym = sym[:hashIdx+1]
	}

	parts := strings.Split(strings.TrimSuffix(sym, "#"), "#")
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	if slashIdx := strings.LastIndex(last, "/"); slashIdx >= 0 {
		return last[slashIdx+1:]
	}
	return last
}

// fqnFromSymbol converts a SemanticDB symbol to a Java fully-qualified name.
// e.g. "java/util/List#" -> "java.util.List"
// Also handles nested type symbols like "com/example/Outer#Inner#".
func fqnFromSymbol(sym string) string {
	if !strings.HasSuffix(sym, "#") {
		return ""
	}
	parts := strings.Split(sym, "#")
	if len(parts) < 2 || parts[len(parts)-1] != "" {
		return ""
	}

	classPart := parts[0]
	if classPart == "" || !strings.Contains(classPart, "/") {
		return ""
	}

	var b strings.Builder
	b.WriteString(strings.ReplaceAll(classPart, "/", "."))
	for _, nested := range parts[1 : len(parts)-1] {
		if nested == "" {
			return ""
		}
		b.WriteByte('.')
		b.WriteString(nested)
	}
	return b.String()
}

func packageFromSymbol(sym string) string {
	if sym == "" {
		return ""
	}
	parts := strings.Split(sym, "#")
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	classPart := parts[0]
	if slashIdx := strings.LastIndex(classPart, "/"); slashIdx >= 0 {
		return strings.ReplaceAll(classPart[:slashIdx], "/", ".")
	}
	return ""
}

// packageFromFQN returns the package from a fully-qualified name.
// e.g. "java.util.List" -> "java.util"
func packageFromFQN(fqn string) string {
	if idx := strings.LastIndex(fqn, "."); idx >= 0 {
		return fqn[:idx]
	}
	return ""
}

// importGroup returns a group number for sorting: 0=java, 1=javax, 2=other.
func importGroup(imp string) int {
	if strings.HasPrefix(imp, "java.") {
		return 0
	}
	if strings.HasPrefix(imp, "javax.") {
		return 1
	}
	return 2
}

// importSortKey produces a sort key that groups java/javax/other and sorts within.
func importSortKey(imp string) string {
	g := importGroup(imp)
	return string(rune('0'+g)) + imp
}

// addImportEdit computes a WorkspaceEdit that adds a single import statement
// for the given fully-qualified name at the correct sorted position.
func addImportEdit(fileURI string, overlay string, fqn string) *WorkspaceEdit {
	return addImportEditWithOverlay(fileURI, overlay, overlay != "", fqn)
}

func addImportEditWithOverlay(fileURI string, overlay string, hasOverlay bool, fqn string) *WorkspaceEdit {
	content := readContent(fileURI, overlay, hasOverlay)
	if content == nil {
		return nil
	}

	tree, err := getTree(content)
	if err != nil {
		return nil
	}
	root := tree.RootNode()
	pkg := detectPackage(root, content)
	importPkg := packageFromFQN(fqn)
	if importPkg == "" || importPkg == "java.lang" || importPkg == pkg {
		return nil
	}

	block := parseImportBlock(root, content)

	// Check if already imported.
	for _, imp := range block.imports {
		if imp == fqn {
			return nil
		}
	}

	// Calculate the insertion.
	insertLine, newText := calculateImportInsert(root, content, block, fqn)

	return insertTextAtLine(fileURI, insertLine, newText)
}

// calculateImportInsert determines the line and text for inserting a new import.
func calculateImportInsert(root *slog.Node, content []byte, block importBlock, fqn string) (int, string) {
	// Find the correct insertion point within the existing imports.
	newKey := importSortKey(fqn)
	insertIdx := len(block.imports)
	for i, imp := range block.imports {
		if importSortKey(imp) > newKey {
			insertIdx = i
			break
		}
	}

	// Build the import line.
	importLine := "import " + fqn + ";\n"

	// Determine if we need blank line separators for grouping.
	var newText string
	if len(block.imports) == 0 && len(block.staticImports) == 0 {
		// No existing imports — add after package declaration with blank line.
		newText = "\n" + importLine
	} else {
		newGroup := importGroup(fqn)
		var sb strings.Builder
		if insertIdx > 0 && importGroup(block.imports[insertIdx-1]) != newGroup {
			sb.WriteByte('\n')
		}
		sb.WriteString(importLine)
		if insertIdx < len(block.imports) && importGroup(block.imports[insertIdx]) != newGroup {
			sb.WriteByte('\n')
		}
		newText = sb.String()
	}

	// Calculate the insertion line in the file.
	var insertLine int
	if len(block.imports) == 0 && len(block.staticImports) == 0 {
		insertLine = block.startLine
	} else {
		// Find the actual line of the insertIdx-th regular import.
		regularIdx := 0
		insertLine = block.endLine // default: append at end

		// Re-parse regular imports from nodes to find the line of insertIdx.
		for i := 0; i < int(root.NamedChildCount()); i++ {
			child := root.NamedChild(i)
			if child.Type() == "import_declaration" {
				spec := parseImport(child, content)
				if !spec.Static && spec.Path != "" {
					if regularIdx == insertIdx {
						insertLine = int(child.StartPoint().Row)
						break
					}
					regularIdx++
				}
			}
		}
	}
	return insertLine, newText
}

// computeImportEdit returns a TextEdit that inserts an import statement for the
// given FQN, or nil if the import already exists or is unnecessary (java.lang, same package).
func computeImportEdit(content []byte, imports []ImportSpec, pkg string, fqn string) *TextEdit {
	importPkg := packageFromFQN(fqn)
	if importPkg == "" || importPkg == "java.lang" || importPkg == pkg {
		return nil
	}

	// Check if already imported (explicit or wildcard).
	for _, imp := range imports {
		if imp.Static {
			continue
		}
		if imp.Path == fqn {
			return nil
		}
		if imp.Wildcard && strings.TrimSuffix(imp.Path, ".*") == importPkg {
			return nil
		}
	}

	tree, err := getTree(content)
	if err != nil {
		return nil
	}
	root := tree.RootNode()
	block := parseImportBlock(root, content)

	// Calculate the insertion.
	insertLine, newText := calculateImportInsert(root, content, block, fqn)

	return &TextEdit{
		Range:   Range{Start: Position{Line: insertLine, Character: 0}, End: Position{Line: insertLine, Character: 0}},
		NewText: newText,
	}
}

// collectTypeIdentifiers walks the AST and returns a set of type identifier texts
// (excluding import and package declarations). In Java tree-sitter, type references
// use the "type_identifier" node type, which reliably identifies class/interface names.
func collectTypeIdentifiers(root *slog.Node, content []byte) map[string]bool {
	types := make(map[string]bool)
	var walk func(n *slog.Node)
	walk = func(n *slog.Node) {
		switch n.Type() {
		case "import_declaration", "package_declaration":
			return
		case "type_identifier":
			types[n.Content(content)] = true
		case "identifier":
			if name := n.Content(content); looksLikeTypeIdentifier(name) && isTypeIdentifierFallbackNode(n) {
				types[name] = true
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	return types
}

func looksLikeTypeIdentifier(name string) bool {
	if name == "" {
		return false
	}
	b := name[0]
	return b >= 'A' && b <= 'Z'
}

func isTypeIdentifierFallbackNode(node *slog.Node) bool {
	if node == nil {
		return false
	}
	parent := node.Parent()
	if parent == nil {
		return false
	}

	// Type parameters like "T" are not imports.
	for n := node; n != nil; n = n.Parent() {
		switch n.Type() {
		case "type_parameters", "type_parameter":
			return false
		}
	}

	switch parent.Type() {
	case "class_declaration", "interface_declaration", "enum_declaration", "record_declaration",
		"method_declaration", "constructor_declaration", "variable_declarator",
		"package_declaration", "import_declaration":
		return false
	case "field_access", "method_invocation":
		// Allow if it's the 'object' part (static qualifier), e.g., MyType.staticMethod()
		return parent.ChildByFieldName("object") == node
	case "scoped_identifier", "scoped_type_identifier":
		// If it's already qualified, we don't need to add an import for the simple name.
		return false
	default:
		return true
	}
}

// collectIdentifiers walks the AST and returns a set of all identifier texts
// (excluding import declarations) used in the file. This is used to detect
// whether a statically-imported member name is actually referenced.
func collectIdentifiers(root *slog.Node, content []byte) map[string]bool {
	idents := make(map[string]bool)
	var walk func(n *slog.Node)
	walk = func(n *slog.Node) {
		if n.Type() == "import_declaration" {
			return
		}
		if n.Type() == "identifier" {
			idents[n.Content(content)] = true
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	return idents
}

type unqualifiedMethodCall struct {
	name       string
	line       int
	character  int
	paramCount int
	argTypes   []*index.TypeExpr
}

type unresolvedIdentifierUse struct {
	name      string
	line      int
	character int
}

func collectMissingStaticMethodImports(root *slog.Node, content []byte, fileURI string, idx *index.Index, block importBlock, filePackage string) []string {
	existingStatic := make(map[string]bool, len(block.staticImports))
	existingStaticNames := make(map[string]bool, len(block.staticImports))
	for _, imp := range block.staticImports {
		existingStatic[imp] = true
		existingStaticNames[simpleNameFromImport(imp)] = true
	}

	var missing []string
	seen := make(map[string]bool)
	for _, call := range collectUnqualifiedMethodCalls(root, content) {
		if call.name == "" || existingStaticNames[call.name] {
			continue
		}
		if occ := idx.OccurrenceAt(fileURI, call.line, call.character); occ != nil && occ.Symbol != "" {
			continue
		}
		imp := resolveStaticMethodImport(call.name, call.paramCount, call.argTypes, idx, filePackage)
		if imp == "" || existingStatic[imp] || seen[imp] {
			continue
		}
		seen[imp] = true
		missing = append(missing, imp)
	}
	return missing
}

func collectUnqualifiedMethodCalls(root *slog.Node, content []byte) []unqualifiedMethodCall {
	var calls []unqualifiedMethodCall
	var walk func(*slog.Node)
	walk = func(n *slog.Node) {
		if n == nil {
			return
		}
		if n.Type() == "method_invocation" {
			if obj := n.ChildByFieldName("object"); obj == nil {
				if name := n.ChildByFieldName("name"); name != nil {
					call := unqualifiedMethodCall{
						name:       name.Content(content),
						line:       int(name.StartPoint().Row),
						character:  int(name.StartPoint().Column),
						paramCount: countInvocationArguments(n.ChildByFieldName("arguments")),
						argTypes:   collectInvocationArgumentTypes(n.ChildByFieldName("arguments"), content),
					}
					calls = append(calls, call)
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	return calls
}

func collectMissingStaticFieldImports(root *slog.Node, content []byte, fileURI string, idx *index.Index, block importBlock, filePackage string) []string {
	existingStatic := make(map[string]bool, len(block.staticImports))
	existingStaticNames := make(map[string]bool, len(block.staticImports))
	for _, imp := range block.staticImports {
		existingStatic[imp] = true
		existingStaticNames[simpleNameFromImport(imp)] = true
	}

	var missing []string
	seen := make(map[string]bool)
	for _, ident := range collectUnresolvedIdentifierUses(root, content) {
		if ident.name == "" || existingStaticNames[ident.name] {
			continue
		}
		if occ := idx.OccurrenceAt(fileURI, ident.line, ident.character); occ != nil && occ.Symbol != "" {
			continue
		}
		imp := resolveStaticFieldImport(ident.name, idx, filePackage)
		if imp == "" || existingStatic[imp] || seen[imp] {
			continue
		}
		seen[imp] = true
		missing = append(missing, imp)
	}
	return missing
}

func collectUnresolvedIdentifierUses(root *slog.Node, content []byte) []unresolvedIdentifierUse {
	declared := collectDeclaredIdentifiers(root, content)
	var uses []unresolvedIdentifierUse
	var walk func(*slog.Node)
	walk = func(n *slog.Node) {
		if n == nil {
			return
		}
		if n.Type() == "identifier" && isStaticFieldIdentifierUse(n) {
			name := n.Content(content)
			if !declared[name] {
				uses = append(uses, unresolvedIdentifierUse{
					name:      name,
					line:      int(n.StartPoint().Row),
					character: int(n.StartPoint().Column),
				})
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	return uses
}

func collectDeclaredIdentifiers(root *slog.Node, content []byte) map[string]bool {
	declared := make(map[string]bool)
	var walk func(*slog.Node)
	walk = func(n *slog.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "class_declaration", "interface_declaration", "enum_declaration", "annotation_type_declaration",
			"method_declaration", "constructor_declaration", "variable_declarator", "formal_parameter",
			"spread_parameter", "catch_formal_parameter", "resource", "enhanced_for_statement",
			"lambda_expression", "type_parameter":
			for i := 0; i < int(n.NamedChildCount()); i++ {
				child := n.NamedChild(i)
				if child.Type() == "identifier" {
					declared[child.Content(content)] = true
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	return declared
}

func isStaticFieldIdentifierUse(n *slog.Node) bool {
	parent := n.Parent()
	if parent == nil {
		return false
	}
	for cur := n; cur != nil; cur = cur.Parent() {
		switch cur.Type() {
		case "import_declaration", "package_declaration", "scoped_identifier", "scoped_type_identifier":
			return false
		}
	}
	switch parent.Type() {
	case "package_declaration", "import_declaration", "method_invocation", "field_access",
		"class_declaration", "interface_declaration", "enum_declaration", "annotation_type_declaration",
		"method_declaration", "constructor_declaration", "variable_declarator", "formal_parameter",
		"spread_parameter", "catch_formal_parameter", "resource", "type_parameter", "enum_constant":
		return false
	}
	return true
}

func countInvocationArguments(argsNode *slog.Node) int {
	if argsNode == nil {
		return 0
	}
	return int(argsNode.NamedChildCount())
}

func collectInvocationArgumentTypes(argsNode *slog.Node, content []byte) []*index.TypeExpr {
	if argsNode == nil {
		return nil
	}
	argTypes := make([]*index.TypeExpr, 0, int(argsNode.NamedChildCount()))
	for i := 0; i < int(argsNode.NamedChildCount()); i++ {
		arg := argsNode.NamedChild(i)
		te, _ := inferTypeFromExpr(arg, content, nil)
		argTypes = append(argTypes, te)
	}
	return argTypes
}

func resolveStaticMethodImport(name string, paramCount int, argTypes []*index.TypeExpr, idx *index.Index, filePackage string) string {
	candidates := idx.MembersBySimpleName(name)
	if len(candidates) == 0 {
		return ""
	}

	resolver := &typeResolver{idx: idx, pkg: filePackage}
	bestScore := -1 << 30
	bestImport := ""
	tied := false
	for _, sym := range candidates {
		if sym.Kind != sdb.SymbolInformation_METHOD || !sym.IsStatic {
			continue
		}
		if !staticMethodMatchesArity(sym, paramCount, idx) {
			continue
		}
		owner := ownerFQNFromMemberSymbol(sym.Symbol)
		if owner == "" {
			continue
		}
		if pkg := packageFromFQN(owner); pkg == "" || pkg == "java.lang" || pkg == filePackage {
			continue
		}
		score := staticMethodImportScore(sym, argTypes, idx, resolver)
		imp := owner + "." + name
		if score > bestScore {
			bestScore = score
			bestImport = imp
			tied = false
			continue
		}
		if score == bestScore && bestImport != "" && bestImport != imp {
			tied = true
		}
	}

	if bestImport == "" || tied {
		return ""
	}
	return bestImport
}

func resolveStaticFieldImport(name string, idx *index.Index, filePackage string) string {
	candidates := idx.MembersBySimpleName(name)
	if len(candidates) == 0 {
		return ""
	}

	uniqueImports := make(map[string]bool)
	for _, sym := range candidates {
		if sym.Kind != sdb.SymbolInformation_FIELD || !sym.IsStatic {
			continue
		}
		owner := ownerFQNFromMemberSymbol(sym.Symbol)
		if owner == "" {
			continue
		}
		if pkg := packageFromFQN(owner); pkg == "" || pkg == "java.lang" || pkg == filePackage {
			continue
		}
		uniqueImports[owner+"."+name] = true
	}

	if len(uniqueImports) != 1 {
		return ""
	}
	for imp := range uniqueImports {
		return imp
	}
	return ""
}

func staticMethodMatchesArity(sym index.Symbol, desiredArity int, idx *index.Index) bool {
	if pts := idx.DeclParamTypesOf(sym.Symbol); len(pts) > 0 {
		return len(pts) == desiredArity
	}
	if sig := sym.Signature; sig != nil {
		if len(sig.Params) > 0 {
			return len(sig.Params) == desiredArity
		}
		if sig.HasParams {
			return len(sig.ParseParams()) == desiredArity
		}
	}
	return desiredArity == 0
}

func staticMethodImportScore(sym index.Symbol, actualArgTypes []*index.TypeExpr, idx *index.Index, resolver *typeResolver) int {
	score := 0
	paramTypes := staticMethodParamTypes(sym, idx, resolver)
	if len(paramTypes) > 0 {
		score += 100
	}
	if len(actualArgTypes) == 0 || len(paramTypes) == 0 {
		return score
	}
	for i := 0; i < len(actualArgTypes) && i < len(paramTypes); i++ {
		actual := actualArgTypes[i]
		formal := paramTypes[i]
		if actual == nil || formal == nil {
			continue
		}
		resolved := resolver.resolveParameterized(actual)
		if resolved == nil {
			resolved = actual
		}
		switch {
		case sameTypeExpr(formal, resolved):
			score += 20
		case typeExprMatchesExpected(resolved, formal):
			score += 10
		case resolved.Sym != "" && formal.Sym != "" && idx.IsAssignableTo(resolved.Sym, formal.Sym):
			score += 5
		}
	}
	return score
}

func staticMethodParamTypes(sym index.Symbol, idx *index.Index, resolver *typeResolver) []*index.TypeExpr {
	if pts := idx.DeclParamTypesOf(sym.Symbol); len(pts) > 0 {
		return pts
	}
	if sym.Signature == nil {
		return nil
	}

	var result []*index.TypeExpr
	for _, param := range sym.Signature.Params {
		if param.TypeSym != "" {
			result = append(result, &index.TypeExpr{Sym: param.TypeSym})
			continue
		}
		if te := signatureParamTypeExpr(param.Type, resolver); te != nil {
			result = append(result, te)
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

func ownerFQNFromMemberSymbol(sym string) string {
	hashIdx := strings.LastIndex(sym, "#")
	if hashIdx < 0 {
		return ""
	}
	return fqnFromSymbol(sym[:hashIdx+1])
}

func findChildByType(n *slog.Node, nodeType string) *slog.Node {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		if child.Type() == nodeType {
			return child
		}
	}
	return nil
}
