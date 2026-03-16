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
	"time"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"github.com/fwrq41251/decaf/internal/uri"
	"google.golang.org/protobuf/proto"
)

// Index stores SemanticDB data for the workspace, providing lookups for
// goto definition, find references, etc.
type Index struct {
	mu         sync.RWMutex
	logger     *log.Logger
	sourceRoot string // workspace root (file path, not URI)

	// symbol string -> list of definition locations
	definitions map[string][]*Symbol
	// symbol string -> list of reference occurrences
	references map[string][]*Occurrence
	// uri -> all occurrences in that file (for position-based lookups)
	fileOccurrences map[string][]*Occurrence
	// uri -> all symbol infos in that file
	fileSymbols map[string][]*Symbol
	// parent symbol -> list of child symbols that extend/implement it
	implementors map[string][]string

	// Reverse indexes for efficient removeDocument.
	// uri -> set of symbols whose references appear in that file
	uriRefSymbols map[string]map[string]struct{}
	// child symbol -> list of parent symbols it implements/extends
	childToParents map[string][]string

	// string intern pool to deduplicate URI and symbol strings
	internPool map[string]string

	// Incremental indexing state.
	// .semanticdb file path -> last indexed modification time
	modTimes map[string]time.Time
	// .semanticdb file path -> list of document URIs it contained
	sdbToURIs map[string][]string
	// file system watcher (nil until first Load completes)
	watcher *watcher

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
		definitions:       make(map[string][]*Symbol),
		references:        make(map[string][]*Occurrence),
		fileOccurrences:   make(map[string][]*Occurrence),
		fileSymbols:       make(map[string][]*Symbol),
		implementors:      make(map[string][]string),
		uriRefSymbols:     make(map[string]map[string]struct{}),
		childToParents:    make(map[string][]string),
		internPool:        make(map[string]string),
		modTimes:          make(map[string]time.Time),
		sdbToURIs:         make(map[string][]string),
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

// Load indexes .semanticdb files incrementally.
// On the first call it does a full directory walk and starts a file watcher.
// Subsequent calls process only files reported as changed by the watcher.
func (idx *Index) Load() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.watcher != nil {
		return idx.loadFromWatcher()
	}
	return idx.loadFull()
}

// loadFull walks the entire workspace, indexes all .semanticdb files,
// and starts the file watcher for future incremental loads.
func (idx *Index) loadFull() error {
	current := make(map[string]os.FileInfo)
	err := filepath.Walk(idx.sourceRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if isSkippedDir(info.Name(), path) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".semanticdb") {
			current[path] = info
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking source root: %w", err)
	}

	var toIndex []string
	for path, info := range current {
		prev, ok := idx.modTimes[path]
		if !ok || info.ModTime().After(prev) {
			toIndex = append(toIndex, path)
		}
	}

	var deleted []string
	for path := range idx.modTimes {
		if _, ok := current[path]; !ok {
			deleted = append(deleted, path)
		}
	}

	if len(toIndex) == 0 && len(deleted) == 0 {
		idx.logger.Printf("index up-to-date (%d files, no changes)", len(current))
	} else {
		idx.logger.Printf("full scan: %d new/modified, %d deleted (of %d total)",
			len(toIndex), len(deleted), len(current))

		for _, path := range deleted {
			idx.removeFile(path)
		}
		for _, path := range toIndex {
			idx.removeFile(path)
			if err := idx.indexFile(path); err != nil {
				idx.logger.Printf("warning: failed to index %s: %v", path, err)
				continue
			}
			idx.modTimes[path] = current[path].ModTime()
		}
		if len(toIndex) > 0 || len(deleted) > 0 {
			idx.clearExternalCache()
		}
		idx.logStats()
	}

	// Start watcher for subsequent incremental loads.
	w, err := newWatcher(idx)
	if err != nil {
		idx.logger.Printf("warning: failed to start file watcher: %v (falling back to full scan)", err)
	} else {
		w.watchDirs(idx.sourceRoot)
		idx.watcher = w
		idx.logger.Printf("file watcher started for %s", idx.sourceRoot)
	}

	return nil
}

// loadFromWatcher processes only files reported as changed by the file watcher.
func (idx *Index) loadFromWatcher() error {
	dirty, removed := idx.watcher.drain()

	if len(dirty) == 0 && len(removed) == 0 {
		idx.logger.Printf("index up-to-date (no watcher events)")
		return nil
	}

	idx.logger.Printf("watcher index: %d changed, %d removed", len(dirty), len(removed))

	for _, path := range removed {
		idx.removeFile(path)
	}

	for _, path := range dirty {
		info, err := os.Stat(path)
		if err != nil {
			// File was created then quickly deleted.
			idx.removeFile(path)
			continue
		}
		idx.removeFile(path)
		if err := idx.indexFile(path); err != nil {
			idx.logger.Printf("warning: failed to index %s: %v", path, err)
			continue
		}
		idx.modTimes[path] = info.ModTime()
	}

	if len(dirty) > 0 || len(removed) > 0 {
		idx.clearExternalCache()
	}
	idx.logStats()
	return nil
}

func (idx *Index) logStats() {
	totalDefs := 0
	for _, defs := range idx.definitions {
		totalDefs += len(defs)
	}
	totalRefs := 0
	for _, refs := range idx.references {
		totalRefs += len(refs)
	}
	idx.logger.Printf("index totals: %d definitions, %d references", totalDefs, totalRefs)
}

// Close stops the file watcher. Safe to call multiple times.
func (idx *Index) Close() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.watcher != nil {
		_ = idx.watcher.close()
		idx.watcher = nil
	}
}

// removeFile removes all index data associated with a .semanticdb file.
func (idx *Index) removeFile(path string) {
	uris := idx.sdbToURIs[path]
	for _, uri := range uris {
		idx.removeDocument(uri)
	}
	delete(idx.sdbToURIs, path)
	delete(idx.modTimes, path)
}

// removeDocument removes all index entries for a given document URI.
func (idx *Index) removeDocument(uri string) {
	// Collect symbols defined in this document for cleanup.
	var docSymbols []string
	for _, s := range idx.fileSymbols[uri] {
		docSymbols = append(docSymbols, s.Symbol)
	}

	// Remove definitions belonging to this URI.
	for _, sym := range docSymbols {
		if defs, ok := idx.definitions[sym]; ok {
			filtered := defs[:0]
			for _, d := range defs {
				if d.URI != uri {
					filtered = append(filtered, d)
				}
			}
			if len(filtered) > 0 {
				idx.definitions[sym] = filtered
			} else {
				delete(idx.definitions, sym)
			}
		}
	}

	// Remove references belonging to this URI using reverse index.
	if refSyms, ok := idx.uriRefSymbols[uri]; ok {
		for sym := range refSyms {
			if refs, ok := idx.references[sym]; ok {
				filtered := refs[:0]
				for _, r := range refs {
					if r.URI != uri {
						filtered = append(filtered, r)
					}
				}
				if len(filtered) > 0 {
					idx.references[sym] = filtered
				} else {
					delete(idx.references, sym)
				}
			}
		}
		delete(idx.uriRefSymbols, uri)
	}

	// Remove implementors contributed by symbols from this URI using reverse index.
	for _, sym := range docSymbols {
		if parents, ok := idx.childToParents[sym]; ok {
			for _, parent := range parents {
				if children, ok := idx.implementors[parent]; ok {
					filtered := children[:0]
					for _, child := range children {
						if child != sym {
							filtered = append(filtered, child)
						}
					}
					if len(filtered) > 0 {
						idx.implementors[parent] = filtered
					} else {
						delete(idx.implementors, parent)
					}
				}
			}
			delete(idx.childToParents, sym)
		}
	}

	delete(idx.fileOccurrences, uri)
	delete(idx.fileSymbols, uri)

	// Clean intern pool entries for removed symbols/URI.
	// Only remove if no other document references them.
	for _, sym := range docSymbols {
		if _, ok := idx.definitions[sym]; !ok {
			delete(idx.internPool, sym)
		}
	}
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

	var uris []string
	for _, doc := range docs.Documents {
		uri := doc.Uri
		if uri == "" {
			idx.logger.Printf("warning: empty URI in %s", path)
			continue
		}
		uri = filepath.ToSlash(uri)
		uris = append(uris, uri)
		idx.indexDocument(uri, doc)
	}
	idx.sdbToURIs[path] = uris

	return nil
}

func (idx *Index) indexDocument(uri string, doc *sdb.TextDocument) {
	uri = idx.intern(filepath.ToSlash(uri))

	// Index symbol definitions.
	for _, sym := range doc.Symbols {
		symStr := idx.intern(sym.Symbol)
		s := &Symbol{
			Name:      sym.DisplayName,
			Symbol:    symStr,
			Kind:      sym.Kind,
			URI:       uri,
			Signature: buildSignatureInfo(sym.DisplayName, sym.Signature),
		}
		idx.definitions[symStr] = append(idx.definitions[symStr], s)
		idx.fileSymbols[uri] = append(idx.fileSymbols[uri], s)

		// Build implementors index from class signatures.
		if sig := sym.Signature; sig != nil {
			if cs, ok := sig.SealedValue.(*sdb.Signature_ClassSignature); ok {
				for _, parent := range cs.ClassSignature.Parents {
					if tr, ok := parent.SealedValue.(*sdb.Type_TypeRef); ok {
						parentSym := idx.intern(tr.TypeRef.Symbol)
						idx.implementors[parentSym] = append(idx.implementors[parentSym], symStr)
						idx.childToParents[symStr] = append(idx.childToParents[symStr], parentSym)
					}
				}
			}
		}
	}

	// Index occurrences (both definitions and references with ranges).
	for _, occ := range doc.Occurrences {
		occSym := idx.intern(occ.Symbol)
		o := &Occurrence{
			Symbol: occSym,
			Role:   occ.Role,
			URI:    uri,
			Range:  occ.Range,
		}

		if occ.Role == sdb.SymbolOccurrence_DEFINITION {
			// Update the definition's range if we have it from occurrences.
			if defs, ok := idx.definitions[occSym]; ok {
				for _, d := range defs {
					if d.URI == uri && d.Range == nil {
						d.Range = occ.Range
					}
				}
			}
		} else {
			idx.references[occSym] = append(idx.references[occSym], o)
			if idx.uriRefSymbols[uri] == nil {
				idx.uriRefSymbols[uri] = make(map[string]struct{})
			}
			idx.uriRefSymbols[uri][occSym] = struct{}{}
		}

		idx.fileOccurrences[uri] = append(idx.fileOccurrences[uri], o)
	}
}

// intern returns a canonical string, ensuring identical strings share memory.
func (idx *Index) intern(s string) string {
	if interned, ok := idx.internPool[s]; ok {
		return interned
	}
	idx.internPool[s] = s
	return s
}

// Definition returns the definition locations for a symbol at the given position.
func (idx *Index) Definition(uri string, line, character int) []Symbol {
	idx.mu.RLock()

	// Find the symbol at the given position.
	relURI := idx.toRelativeURI(uri)
	sym := idx.symbolAt(relURI, line, character)
	idx.logger.Printf("Definition request: uri=%s, relURI=%s, symbolAt=%s", uri, relURI, sym)
	
	if sym == "" {
		idx.mu.RUnlock()
		return nil
	}

	defs := idx.definitions[sym]
	
	// If it's an internal symbol with definitions, return them.
	if len(defs) > 0 {
		result := deduplicateSymbols(copySymbols(defs))
		idx.mu.RUnlock()
		return result
	}

	// It's a possible external symbol. Release RLock before doing potential I/O in resolveExternalSymbol.
	jdkRoot := idx.jdkSourceRoot
	depSources := idx.dependencySources
	idx.mu.RUnlock()

	if jdkRoot != "" || len(depSources) > 0 {
		// Fallback for external symbols (JDK/Dependencies).
		if ext := idx.resolveExternalSymbol(sym); ext != nil {
			return []Symbol{*ext}
		}
	}
	return nil
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

	// Check cache first (sync.Map is thread-safe).
	if cachedPath, ok := idx.externalCache.Load(relPath); ok {
		return idx.createExternalSymbol(sym, cachedPath.(string))
	}

	// Read JDK root and dependency sources under a short RLock.
	idx.mu.RLock()
	jdkRoot := idx.jdkSourceRoot
	depSources := make([]string, len(idx.dependencySources))
	copy(depSources, idx.dependencySources)
	idx.mu.RUnlock()

	// 1. Search in JDK source if available.
	if jdkRoot != "" {
		info, err := os.Stat(jdkRoot)
		if err == nil {
			if info.IsDir() {
				// Already extracted or manually set directory.
				foundPath := filepath.Join(jdkRoot, relPath)
				if _, err := os.Stat(foundPath); err == nil {
					idx.externalCache.Store(relPath, foundPath)
					return idx.createExternalSymbol(sym, foundPath)
				}
			} else if strings.HasSuffix(jdkRoot, ".zip") || strings.HasSuffix(jdkRoot, ".jar") {
				// It's a zip file (like src.zip). Extract on demand.
				if foundPath := idx.findAndExtractFromJar(jdkRoot, relPath); foundPath != "" {
					idx.externalCache.Store(relPath, foundPath)
					return idx.createExternalSymbol(sym, foundPath)
				}
			}
		}
	}

	// 2. Search in third-party dependency JARs.
	for _, jar := range depSources {
		if foundPath := idx.findAndExtractFromJar(jar, relPath); foundPath != "" {
			idx.externalCache.Store(relPath, foundPath)
			return idx.createExternalSymbol(sym, foundPath)
		}
	}

	return nil
}

func (idx *Index) createExternalSymbol(sym, path string) *Symbol {
	fileURI := uri.FromPath(path)

	line, col := FindSymbolLocation(path, sym)

	s := &Symbol{
		Symbol: sym,
		URI:    fileURI,
	}

	if line != -1 {
		s.Range = &sdb.Range{
			StartLine:      int32(line),
			StartCharacter: int32(col),
			EndLine:        int32(line),
			EndCharacter:   int32(col + len(ExtractShortName(sym))),
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
	return deduplicateOccurrences(copyOccurrences(refs))
}

// copySymbols dereferences a slice of pointers into a value slice.
func copySymbols(ptrs []*Symbol) []Symbol {
	if len(ptrs) == 0 {
		return nil
	}
	out := make([]Symbol, len(ptrs))
	for i, p := range ptrs {
		out[i] = *p
	}
	return out
}

// copyOccurrences dereferences a slice of pointers into a value slice.
func copyOccurrences(ptrs []*Occurrence) []Occurrence {
	if len(ptrs) == 0 {
		return nil
	}
	out := make([]Occurrence, len(ptrs))
	for i, p := range ptrs {
		out[i] = *p
	}
	return out
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
	return defs[0]
}

// FileSymbols returns all symbol definitions in the given file (for documentSymbol).
func (idx *Index) FileSymbols(uri string) []Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	return copySymbols(idx.fileSymbols[relURI])
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
		for _, d := range defs {
			result = append(result, *d)
		}
	}
	return result
}

// toRelativeURI converts a file:// URI or an absolute path to a relative path matching SemanticDB URIs.
func (idx *Index) toRelativeURI(u string) string {
	if !uri.IsURI(u) && !filepath.IsAbs(u) {
		return filepath.ToSlash(u)
	}
	return uri.Rel(idx.sourceRoot, u)
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
				result = append(result, *d)
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
				sameFile = append(sameFile, *d)
			} else {
				otherFile = append(otherFile, *d)
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
	return defs[0]
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

	// Collect all occurrences (definitions + references).
	for _, fileOccs := range idx.fileOccurrences {
		for _, occ := range fileOccs {
			if occ.Symbol == sym {
				result = append(result, *occ)
			}
		}
	}

	return sym, result
}

// OccurrenceAt returns the SemanticDB occurrence at the given position.
func (idx *Index) OccurrenceAt(uri string, line, character int) *Occurrence {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	occs, ok := idx.fileOccurrences[relURI]
	if !ok {
		return nil
	}
	for _, occ := range occs {
		r := occ.Range
		if r == nil {
			continue
		}
		if containsPosition(r, line, character) {
			return occ
		}
	}
	return nil
}

// AllFileOccurrences returns all occurrences in the given file.
func (idx *Index) AllFileOccurrences(uri string) []Occurrence {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	relURI := idx.toRelativeURI(uri)
	return copyOccurrences(idx.fileOccurrences[relURI])
}

// SymbolDefinition returns the definition for a SemanticDB symbol string.
// This is useful for resolving a symbol's fully-qualified type without a position.
func (idx *Index) SymbolDefinition(sym string) *Symbol {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	defs := idx.definitions[sym]
	if len(defs) == 0 {
		return nil
	}
	s := *defs[0]
	return &s
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
			result = append(result, *occ)
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
			for _, d := range defs {
				result = append(result, *d)
			}
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
