package index

import (
	"archive/zip"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"google.golang.org/protobuf/proto"
)

// Index stores SemanticDB data for the workspace, providing lookups for
// goto definition, find references, etc.
type Index struct {
	mu         sync.RWMutex
	logger     *log.Logger
	sourceRoot string // workspace root (file path, not URI)

	// symbol string -> list of definition locations
	definitions map[string][]Symbol
	// symbol string -> list of reference occurrences
	references map[string][]Occurrence
	// uri -> all occurrences in that file (for position-based lookups)
	fileOccurrences map[string][]Occurrence
	// uri -> all symbol infos in that file
	fileSymbols map[string][]Symbol
	// parent symbol -> list of child symbols that extend/implement it
	implementors map[string][]string
	// path to JDK source (e.g., /path/to/jdk/lib/src.zip)
	jdkSourceRoot string
	// list of third-party source JARs (file paths)
	dependencySources []string
	// cache for external symbol resolutions (relPath -> extractedPath)
	externalCache sync.Map
}

// NewIndex creates a new empty index.
func NewIndex(logger *log.Logger, sourceRoot string) *Index {
	return &Index{
		logger:            logger,
		sourceRoot:        sourceRoot,
		definitions:       make(map[string][]Symbol),
		references:        make(map[string][]Occurrence),
		fileOccurrences:   make(map[string][]Occurrence),
		fileSymbols:       make(map[string][]Symbol),
		implementors:      make(map[string][]string),
		dependencySources: []string{},
	}
}

// SetDependencySources sets the list of third-party library source JARs.
func (idx *Index) SetDependencySources(paths []string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.dependencySources = paths
	idx.clearExternalCache()
}

// AddDependencySource adds a single third-party library source JAR.
func (idx *Index) AddDependencySource(path string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.logger.Printf("Adding dependency source: %s", path)
	idx.dependencySources = append(idx.dependencySources, path)
	idx.clearExternalCache()
}

func (idx *Index) clearExternalCache() {
	idx.externalCache = sync.Map{}
}

// SetJdkSourceRoot sets the path to the JDK source files.
func (idx *Index) SetJdkSourceRoot(path string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.jdkSourceRoot = path
	idx.clearExternalCache()
}

func (idx *Index) HasFiles() bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.fileOccurrences) > 0
}

// Load scans the workspace for .semanticdb files and indexes them.
func (idx *Index) Load() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	idx.logger.Printf("Indexing workspace: %s", idx.sourceRoot)

	// Clear existing data.
	idx.definitions = make(map[string][]Symbol)
	idx.references = make(map[string][]Occurrence)
	idx.fileOccurrences = make(map[string][]Occurrence)
	idx.fileSymbols = make(map[string][]Symbol)
	idx.implementors = make(map[string][]string)
	idx.clearExternalCache()

	var files []string
	err := filepath.Walk(idx.sourceRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			// Skip internal build tool and cache directories.
			if name == ".bloop" || name == ".metals" || name == ".git" || 
			   strings.Contains(path, "bloop-internal-classes") || 
			   strings.Contains(path, "bloop-bsp-clients-classes") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".semanticdb") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking source root: %w", err)
	}

	idx.logger.Printf("Found %d .semanticdb files in %s", len(files), idx.sourceRoot)

	for _, f := range files {
		if err := idx.indexFile(f); err != nil {
			idx.logger.Printf("warning: failed to index %s: %v", f, err)
		} else {
			idx.logger.Printf("Indexed file: %s", f)
		}
	}

	totalDefs := 0
	for _, defs := range idx.definitions {
		totalDefs += len(defs)
	}
	totalRefs := 0
	for _, refs := range idx.references {
		totalRefs += len(refs)
	}
	idx.logger.Printf("Indexed %d definitions, %d references across %d files", totalDefs, totalRefs, len(files))

	return nil
}

func (idx *Index) indexFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var docs sdb.TextDocuments
	if err := proto.Unmarshal(data, &docs); err != nil {
		return fmt.Errorf("unmarshaling %s: %w", path, err)
	}

	for _, doc := range docs.Documents {
		uri := doc.Uri
		if uri == "" {
			idx.logger.Printf("warning: empty URI in %s", path)
			continue
		}
		idx.indexDocument(uri, doc)
	}

	return nil
}

func (idx *Index) indexDocument(uri string, doc *sdb.TextDocument) {
	uri = filepath.ToSlash(uri)
	// Index symbol definitions.
	for _, sym := range doc.Symbols {
		s := Symbol{
			Name:      sym.DisplayName,
			Symbol:    sym.Symbol,
			Kind:      sym.Kind,
			URI:       uri,
			Signature: sym.Signature,
		}
		idx.definitions[sym.Symbol] = append(idx.definitions[sym.Symbol], s)
		idx.fileSymbols[uri] = append(idx.fileSymbols[uri], s)
	}

	// Build implementors index from class signatures.
	for _, sym := range doc.Symbols {
		if sig := sym.Signature; sig != nil {
			if cs, ok := sig.SealedValue.(*sdb.Signature_ClassSignature); ok {
				for _, parent := range cs.ClassSignature.Parents {
					if tr, ok := parent.SealedValue.(*sdb.Type_TypeRef); ok {
						idx.implementors[tr.TypeRef.Symbol] = append(idx.implementors[tr.TypeRef.Symbol], sym.Symbol)
					}
				}
			}
		}
	}

	// Index occurrences (both definitions and references with ranges).
	for _, occ := range doc.Occurrences {
		o := Occurrence{
			Symbol: occ.Symbol,
			Role:   occ.Role,
			URI:    uri,
			Range:  occ.Range,
		}

		if occ.Role == sdb.SymbolOccurrence_DEFINITION {
			// Update the definition's range if we have it from occurrences.
			if defs, ok := idx.definitions[occ.Symbol]; ok {
				for i := range defs {
					if defs[i].URI == uri && defs[i].Range == nil {
						defs[i].Range = occ.Range
					}
				}
			}
		} else {
			idx.references[occ.Symbol] = append(idx.references[occ.Symbol], o)
		}

		idx.fileOccurrences[uri] = append(idx.fileOccurrences[uri], o)
	}
}

// Definition returns the definition locations for a symbol at the given position.
func (idx *Index) Definition(uri string, line, character int) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	// Find the symbol at the given position.
	relURI := idx.toRelativeURI(uri)
	sym := idx.symbolAt(relURI, line, character)
	idx.logger.Printf("Definition request: uri=%s, relURI=%s, symbolAt=%s", uri, relURI, sym)
	if sym == "" {
		return nil
	}

	defs := idx.definitions[sym]
	if len(defs) == 0 && (idx.jdkSourceRoot != "" || len(idx.dependencySources) > 0) {
		// Fallback for external symbols (JDK/Dependencies).
		if ext := idx.resolveExternalSymbol(sym); ext != nil {
			return []Symbol{*ext}
		}
	}
	return deduplicateSymbols(defs)
}

func (idx *Index) resolveExternalSymbol(sym string) *Symbol {
	idx.logger.Printf("Resolving external symbol: %s", sym)
	// Simple mapping for JDK/Dependency symbols.
	// Format: "java/lang/String#" -> "java/lang/String.java"
	// Format: "org/springframework/util/StringUtils#hasText()." -> "org/springframework/util/StringUtils.java"

	parts := strings.Split(sym, "#")
	if len(parts) == 0 {
		return nil
	}
	relPath := parts[0] + ".java"

	// Check cache first.
	if cachedPath, ok := idx.externalCache.Load(relPath); ok {
		return idx.createExternalSymbol(sym, cachedPath.(string))
	}

	// 1. Search in JDK source if available.
	if idx.jdkSourceRoot != "" {
		info, err := os.Stat(idx.jdkSourceRoot)
		if err == nil {
			if info.IsDir() {
				// Already extracted or manually set directory.
				foundPath := filepath.Join(idx.jdkSourceRoot, relPath)
				if _, err := os.Stat(foundPath); err == nil {
					idx.externalCache.Store(relPath, foundPath)
					return idx.createExternalSymbol(sym, foundPath)
				}
			} else if strings.HasSuffix(idx.jdkSourceRoot, ".zip") || strings.HasSuffix(idx.jdkSourceRoot, ".jar") {
				// It's a zip file (like src.zip). Extract on demand.
				if foundPath := idx.findAndExtractFromJar(idx.jdkSourceRoot, relPath); foundPath != "" {
					idx.externalCache.Store(relPath, foundPath)
					return idx.createExternalSymbol(sym, foundPath)
				}
			}
		}
	}

	// 2. Search in third-party dependency JARs.
	// Caller (Definition) already holds the RLock, so we access dependencySources directly.
	for _, jar := range idx.dependencySources {
		if foundPath := idx.findAndExtractFromJar(jar, relPath); foundPath != "" {
			idx.externalCache.Store(relPath, foundPath)
			return idx.createExternalSymbol(sym, foundPath)
		}
	}

	return nil
}

func (idx *Index) createExternalSymbol(sym, path string) *Symbol {
	uri := "file://" + path

	line, col := FindSymbolLocation(path, sym)

	s := &Symbol{
		Symbol: sym,
		URI:    uri,
	}

	if line != -1 {
		s.Range = &sdb.Range{
			StartLine:      int32(line),
			StartCharacter: int32(col),
			EndLine:        int32(line),
			EndCharacter:   int32(col + len(extractShortName(sym))),
		}
	}

	return s
}

func (idx *Index) findAndExtractFromJar(jarPath, relPath string) string {
	// 1. Check if the JAR contains the file.
	r, err := zip.OpenReader(jarPath)
	if err != nil {
		idx.logger.Printf("Failed to open JAR %s: %v", jarPath, err)
		return ""
	}
	defer r.Close()

	var targetFile *zip.File
	for _, f := range r.File {
		// Use filepath.ToSlash for consistency in ZIP names
		zipName := filepath.ToSlash(f.Name)
		if strings.HasSuffix(zipName, relPath) {
			targetFile = f
			break
		}
	}

	if targetFile == nil {
		return ""
	}

	// 2. Extract the file to cache.
	home, _ := os.UserHomeDir()

	// Use a hash of the jarPath for a safe, cross-platform, fixed-length directory name.
	h := sha1.Sum([]byte(jarPath))
	sanitizedJar := hex.EncodeToString(h[:])

	destDir := filepath.Join(home, ".cache", "decaf", "lib-src", sanitizedJar)
	destPath := filepath.Join(destDir, targetFile.Name)

	if _, err := os.Stat(destPath); err == nil {
		return destPath
	}

	os.MkdirAll(filepath.Dir(destPath), 0755)
	rc, err := targetFile.Open()
	if err != nil {
		return ""
	}
	defer rc.Close()

	// Atomic extraction: write to a temporary file and then rename.
	tmpPath := destPath + ".tmp"
	out, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return ""
	}

	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		os.Remove(tmpPath)
		return ""
	}
	out.Close()

	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		// Check again if another goroutine succeeded in the meantime.
		if _, statErr := os.Stat(destPath); statErr == nil {
			return destPath
		}
		return ""
	}

	return destPath
}

// References returns all reference locations for a symbol at the given position.
func (idx *Index) References(uri string, line, character int) []Occurrence {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	sym := idx.symbolAt(relURI, line, character)
	if sym == "" {
		return nil
	}

	refs := idx.references[sym]
	return deduplicateOccurrences(refs)
}

func deduplicateSymbols(symbols []Symbol) []Symbol {
	if len(symbols) <= 1 {
		return symbols
	}
	// Use a map to keep only one definition per URI.
	// In case of multiple definitions in the same file (unlikely for Java),
	// this will pick the first one encountered.
	seen := make(map[string]bool)
	var result []Symbol
	for _, s := range symbols {
		if s.Range == nil {
			continue
		}
		if !seen[s.URI] {
			seen[s.URI] = true
			result = append(result, s)
		}
	}
	return result
}

func deduplicateOccurrences(occs []Occurrence) []Occurrence {
	if len(occs) <= 1 {
		return occs
	}
	seen := make(map[string]bool)
	var result []Occurrence
	for _, o := range occs {
		if o.Range == nil {
			continue
		}
		key := fmt.Sprintf("%s:%d:%d-%d:%d", o.URI, o.Range.StartLine, o.Range.StartCharacter, o.Range.EndLine, o.Range.EndCharacter)
		if !seen[key] {
			seen[key] = true
			result = append(result, o)
		}
	}
	return result
}

// Hover returns the symbol information at the given position (for hover).
func (idx *Index) Hover(uri string, line, character int) *Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	sym := idx.symbolAt(relURI, line, character)
	if sym == "" {
		return nil
	}

	defs := idx.definitions[sym]
	if len(defs) == 0 {
		return nil
	}
	return &defs[0]
}

// FileSymbols returns all symbol definitions in the given file (for documentSymbol).
func (idx *Index) FileSymbols(uri string) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	return idx.fileSymbols[relURI]
}

// SymbolAt returns the SemanticDB symbol string at the given position.
func (idx *Index) symbolAt(uri string, line, character int) string {
	uri = filepath.ToSlash(uri)
	occs, ok := idx.fileOccurrences[uri]
	if !ok {
		return ""
	}
	for _, occ := range occs {
		r := occ.Range
		if r == nil {
			continue
		}
		if containsPosition(r, line, character) {
			return occ.Symbol
		}
	}
	return ""
}

// AllSymbols returns all indexed symbol definitions (for completion, etc).
func (idx *Index) AllSymbols() []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []Symbol
	for _, defs := range idx.definitions {
		result = append(result, defs...)
	}
	return result
}

// toRelativeURI converts a file:// URI or an absolute path to a relative path matching SemanticDB URIs.
func (idx *Index) toRelativeURI(uri string) string {
	path := strings.TrimPrefix(uri, "file://")
	if !filepath.IsAbs(path) {
		return filepath.ToSlash(path)
	}
	rel, err := filepath.Rel(idx.sourceRoot, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// SourceRoot returns the workspace source root path.
func (idx *Index) SourceRoot() string {
	return idx.sourceRoot
}

// SearchSymbols returns symbols matching the query string (case-insensitive substring match).
func (idx *Index) SearchSymbols(query string) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	query = strings.ToLower(query)
	var result []Symbol
	for _, defs := range idx.definitions {
		for _, d := range defs {
			if strings.Contains(strings.ToLower(d.Name), query) {
				result = append(result, d)
			}
		}
	}
	return result
}

// CompletionSymbols returns symbols matching the given prefix for completion.
// Results from the same file are prioritized.
func (idx *Index) CompletionSymbols(uri string, prefix string) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	prefix = strings.ToLower(prefix)
	relURI := idx.toRelativeURI(uri)

	var sameFile []Symbol
	var otherFile []Symbol
	for _, defs := range idx.definitions {
		for _, d := range defs {
			if !strings.HasPrefix(strings.ToLower(d.Name), prefix) {
				continue
			}
			if d.URI == relURI {
				sameFile = append(sameFile, d)
			} else {
				otherFile = append(otherFile, d)
			}
		}
	}

	result := make([]Symbol, 0, len(sameFile)+len(otherFile))
	result = append(result, sameFile...)
	result = append(result, otherFile...)

	// Cap at 100 results.
	if len(result) > 100 {
		result = result[:100]
	}
	return result
}

// SymbolSignature returns the method signature for the symbol at the given position.
func (idx *Index) SymbolSignature(uri string, line, character int) *Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	sym := idx.symbolAt(relURI, line, character)
	if sym == "" {
		return nil
	}

	defs := idx.definitions[sym]
	if len(defs) == 0 {
		return nil
	}
	return &defs[0]
}

// RenameOccurrences returns all occurrences (definitions + references) for rename.
func (idx *Index) RenameOccurrences(uri string, line, character int) (string, []Occurrence) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	sym := idx.symbolAt(relURI, line, character)
	if sym == "" {
		return "", nil
	}

	var result []Occurrence

	// Collect all definition occurrences.
	for _, fileOccs := range idx.fileOccurrences {
		for _, occ := range fileOccs {
			if occ.Symbol == sym {
				result = append(result, occ)
			}
		}
	}

	return sym, result
}

// FileOccurrencesOf returns all occurrences of a symbol in a specific file.
func (idx *Index) FileOccurrencesOf(uri string, line, character int) []Occurrence {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	sym := idx.symbolAt(relURI, line, character)
	if sym == "" {
		return nil
	}

	var result []Occurrence
	for _, occ := range idx.fileOccurrences[relURI] {
		if occ.Symbol == sym {
			result = append(result, occ)
		}
	}
	return result
}

// Implementations returns definitions of types that implement/extend the symbol at the given position.
func (idx *Index) Implementations(uri string, line, character int) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	sym := idx.symbolAt(relURI, line, character)
	if sym == "" {
		return nil
	}

	implSymbols := idx.implementors[sym]
	var result []Symbol
	for _, implSym := range implSymbols {
		if defs, ok := idx.definitions[implSym]; ok {
			result = append(result, defs...)
		}
	}
	return result
}

func containsPosition(r *sdb.Range, line, character int) bool {
	if int(r.StartLine) > line || int(r.EndLine) < line {
		return false
	}
	if int(r.StartLine) == line && int(r.StartCharacter) > character {
		return false
	}
	if int(r.EndLine) == line && int(r.EndCharacter) <= character {
		return false
	}
	return true
}
