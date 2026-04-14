package lsp

import (
	"sort"
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
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
	content := readContent(fileURI, overlay)
	if content == nil {
		return nil
	}

	tree, err := getTree(content)
	if err != nil {
		return nil
	}
	root := tree.RootNode()

	block := parseImportBlock(root, content)

	// Gather all SemanticDB symbols referenced in this file.
	occs := idx.AllFileOccurrences(fileURI)
	usedSymbols := make(map[string]bool, len(occs))
	for _, occ := range occs {
		usedSymbols[occ.Symbol] = true
	}

	// Determine which simple names are actually used in the file.
	usedSimpleNames := make(map[string]bool)
	for sym := range usedSymbols {
		if name := simpleNameFromSymbol(sym); name != "" {
			usedSimpleNames[name] = true
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
	for _, imp := range block.imports {
		simple := simpleNameFromImport(imp)
		if simple == "*" {
			kept = append(kept, imp)
			continue
		}
		if wildcardPkgs[packageFromFQN(imp)] {
			// Already covered by a wildcard import — skip.
			continue
		}
		if usedSimpleNames[simple] {
			kept = append(kept, imp)
		}
	}

	// Find missing imports: symbols used that have a package prefix,
	// are not in the current file's package, and have no matching import.
	importedSet := make(map[string]bool, len(kept))
	for _, imp := range kept {
		importedSet[imp] = true
	}

	filePackage := detectPackage(root, content)
	for sym := range usedSymbols {
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
			if def := idx.SymbolDefinition(sym); def != nil {
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
	for _, imp := range block.staticImports {
		member := simpleNameFromImport(imp)
		if member == "*" || usedIdents[member] {
			keptStatic = append(keptStatic, imp)
		}
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
	if len(block.imports) == 0 && len(kept) > 0 {
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
	content := readContent(fileURI, overlay)
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

func findChildByType(n *slog.Node, nodeType string) *slog.Node {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		if child.Type() == nodeType {
			return child
		}
	}
	return nil
}
