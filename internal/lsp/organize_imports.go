package lsp

import (
	"bufio"
	"os"
	"sort"
	"strings"

	"github.com/fwrq41251/decaf/internal/index"
	"github.com/fwrq41251/decaf/internal/uri"
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
	var lines []string
	if overlay != "" {
		lines = strings.Split(overlay, "\n")
	} else {
		filePath := uri.ToPath(fileURI)
		var err error
		lines, err = readLines(filePath)
		if err != nil {
			return nil
		}
	}

	block := parseImportBlock(lines)

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

	filePackage := detectPackage(lines)
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

	// Build the replacement text.
	if len(kept) == 0 && len(block.imports) == 0 && len(block.staticImports) == 0 {
		return nil
	}

	var sb strings.Builder

	// Emit static imports first (preserved as-is, sorted).
	if len(block.staticImports) > 0 {
		sorted := make([]string, len(block.staticImports))
		copy(sorted, block.staticImports)
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
		newText = "\n" + newText
	}

	editRange := Range{
		Start: Position{Line: block.startLine, Character: 0},
		End:   Position{Line: block.endLine, Character: 0},
	}

	return &WorkspaceEdit{
		Changes: map[string][]TextEdit{
			fileURI: {{Range: editRange, NewText: newText}},
		},
	}
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// parseImportBlock finds the contiguous import region in a Java source file.
func parseImportBlock(lines []string) importBlock {
	var block importBlock
	block.startLine = -1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import static ") {
			imp := strings.TrimSuffix(strings.TrimPrefix(trimmed, "import static "), ";")
			imp = strings.TrimSpace(imp)
			if block.startLine == -1 {
				block.startLine = i
			}
			block.staticImports = append(block.staticImports, imp)
			block.endLine = i + 1
		} else if strings.HasPrefix(trimmed, "import ") {
			imp := strings.TrimSuffix(strings.TrimPrefix(trimmed, "import "), ";")
			imp = strings.TrimSpace(imp)
			if block.startLine == -1 {
				block.startLine = i
			}
			block.imports = append(block.imports, imp)
			block.endLine = i + 1
		} else if block.startLine != -1 {
			// Allow blank lines within the import block.
			if trimmed == "" {
				continue
			}
			// Non-import, non-blank line after imports started — end the block.
			break
		}
	}

	if block.startLine == -1 {
		// No imports found. Insert after the package declaration (or at top).
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "package ") {
				block.startLine = i + 1
				block.endLine = i + 1
				return block
			}
		}
		block.startLine = 0
		block.endLine = 0
	}

	return block
}

// detectPackage returns the package name from the Java source (e.g. "com.example").
func detectPackage(lines []string) string {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			pkg := strings.TrimSuffix(strings.TrimPrefix(trimmed, "package "), ";")
			return strings.TrimSpace(pkg)
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
// e.g. "java/util/List#" -> "List", "com/example/Foo#bar()." -> "Foo"
func simpleNameFromSymbol(sym string) string {
	// Strip method/field suffix (anything after #, including the # for owner types).
	hashIdx := strings.Index(sym, "#")
	if hashIdx < 0 {
		return ""
	}
	// Get the class portion: "java/util/List"
	classPart := sym[:hashIdx]
	if slashIdx := strings.LastIndex(classPart, "/"); slashIdx >= 0 {
		return classPart[slashIdx+1:]
	}
	return classPart
}

// fqnFromSymbol converts a SemanticDB symbol to a Java fully-qualified name.
// e.g. "java/util/List#" -> "java.util.List"
// Only handles top-level type symbols (ending with "#").
func fqnFromSymbol(sym string) string {
	if !strings.HasSuffix(sym, "#") {
		// Only type symbols, not methods/fields.
		// Check for inner symbol like "com/example/Outer#Inner#"
		parts := strings.Split(sym, "#")
		if len(parts) != 2 || parts[1] != "" {
			return ""
		}
	}
	classPart := strings.TrimSuffix(sym, "#")
	if classPart == "" || !strings.Contains(classPart, "/") {
		return ""
	}
	return strings.ReplaceAll(classPart, "/", ".")
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
	var lines []string
	if overlay != "" {
		lines = strings.Split(overlay, "\n")
	} else {
		filePath := uri.ToPath(fileURI)
		var err error
		lines, err = readLines(filePath)
		if err != nil {
			return nil
		}
	}

	block := parseImportBlock(lines)

	// Check if already imported.
	for _, imp := range block.imports {
		if imp == fqn {
			return nil
		}
	}

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
		for i := block.startLine; i < block.endLine; i++ {
			trimmed := strings.TrimSpace(lines[i])
			if strings.HasPrefix(trimmed, "import ") && !strings.HasPrefix(trimmed, "import static ") {
				if regularIdx == insertIdx {
					insertLine = i
					break
				}
				regularIdx++
			}
		}
	}

	editRange := Range{
		Start: Position{Line: insertLine, Character: 0},
		End:   Position{Line: insertLine, Character: 0},
	}

	return &WorkspaceEdit{
		Changes: map[string][]TextEdit{
			fileURI: {{Range: editRange, NewText: newText}},
		},
	}
}
