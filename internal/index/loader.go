package index

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

// parsedFile holds the result of reading and unmarshaling a single .semanticdb file.
type parsedFile struct {
	path string
	docs *sdb.TextDocuments
}

// Load indexes .semanticdb files incrementally.
// On the first call it does a full directory walk and starts a file watcher.
// Subsequent calls process only files reported as changed by the watcher.
func (idx *Index) Load() error {
	idx.mu.RLock()
	hasWatcher := idx.watcher != nil
	idx.mu.RUnlock()

	if hasWatcher {
		return idx.loadFromWatcher()
	}
	return idx.loadFull()
}

// loadFull walks the entire workspace, indexes all .semanticdb files,
// and starts the file watcher for future incremental loads.
//
// File I/O and protobuf unmarshaling run lock-free; only the final index
// mutation step acquires the write lock to minimize blocking of LSP queries.
func (idx *Index) loadFull() error {
	// Phase 1: Walk the directory tree (lock-free, sourceRoot is immutable).
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

	// Phase 2: Diff against previous modTimes under a brief read lock.
	idx.mu.RLock()
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
	idx.mu.RUnlock()

	// Phase 3: Concurrent file I/O + protobuf unmarshal (lock-free).
	parsed := idx.parseFilesConcurrently(toIndex)

	// Phase 4: Acquire write lock for serial index mutation.
	idx.mu.Lock()
	if len(toIndex) == 0 && len(deleted) == 0 {
		idx.logger.Printf("index up-to-date (%d files, no changes)", len(current))
	} else {
		idx.logger.Printf("full scan: %d new/modified, %d deleted (of %d total)",
			len(toIndex), len(deleted), len(current))

		for _, path := range deleted {
			if idx.sourceFilesExist(path) {
				idx.logger.Printf("keeping stale index for %s (source files still exist)", path)
			} else {
				idx.removeFile(path)
			}
		}

		for _, pf := range parsed {
			idx.removeFile(pf.path)
			var uris []string
			for _, doc := range pf.docs.Documents {
				uri := doc.Uri
				if uri == "" {
					idx.logger.Printf("warning: empty URI in %s", pf.path)
					continue
				}
				uri = filepath.ToSlash(uri)
				uris = append(uris, uri)
				idx.indexDocument(uri, doc)
			}
			idx.sdbToURIs[pf.path] = uris
			idx.modTimes[pf.path] = current[pf.path].ModTime()
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
	idx.mu.Unlock()

	return nil
}

// parseFilesConcurrently reads and unmarshals .semanticdb files in parallel.
// It returns successfully parsed files; failures are logged and skipped.
func (idx *Index) parseFilesConcurrently(paths []string) []parsedFile {
	if len(paths) == 0 {
		return nil
	}

	workers := runtime.NumCPU()
	if workers > len(paths) {
		workers = len(paths)
	}

	results := make([]parsedFile, len(paths))
	var parseErrors sync.Map

	g := new(errgroup.Group)
	g.SetLimit(workers)

	for i, path := range paths {
		g.Go(func() error {
			data, err := os.ReadFile(path)
			if err != nil {
				parseErrors.Store(path, err)
				return nil // don't abort other goroutines
			}
			var docs sdb.TextDocuments
			if err := proto.Unmarshal(data, &docs); err != nil {
				parseErrors.Store(path, fmt.Errorf("unmarshaling: %w", err))
				return nil
			}
			results[i] = parsedFile{path: path, docs: &docs}
			return nil
		})
	}
	_ = g.Wait()

	// Compact: keep only successful parses.
	out := results[:0]
	for _, r := range results {
		if r.docs != nil {
			out = append(out, r)
		}
	}

	parseErrors.Range(func(key, value any) bool {
		idx.logger.Printf("warning: failed to parse %s: %v", key, value)
		return true
	})

	return out
}

// loadFromWatcher processes only files reported as changed by the file watcher.
// I/O (drain, stat, parse) runs lock-free; only the merge step holds the write lock.
func (idx *Index) loadFromWatcher() error {
	// Phase 1: Drain watcher events and stat files (lock-free).
	idx.mu.RLock()
	w := idx.watcher
	idx.mu.RUnlock()

	dirty, removed := w.drain()

	if len(dirty) == 0 && len(removed) == 0 {
		idx.logger.Printf("index up-to-date (no watcher events)")
		return nil
	}

	idx.logger.Printf("watcher index: %d changed, %d removed", len(dirty), len(removed))

	// Filter out files that disappeared between watcher event and now.
	var validDirty []string
	var gonePaths []string
	modInfos := make(map[string]os.FileInfo)
	for _, path := range dirty {
		info, err := os.Stat(path)
		if err != nil {
			gonePaths = append(gonePaths, path)
			continue
		}
		validDirty = append(validDirty, path)
		modInfos[path] = info
	}

	// Phase 2: Concurrent parse (lock-free).
	parsed := idx.parseFilesConcurrently(validDirty)

	// Phase 3: Acquire write lock for serial index mutation.
	idx.mu.Lock()
	defer idx.mu.Unlock()

	for _, path := range removed {
		if idx.sourceFilesExist(path) {
			idx.logger.Printf("keeping stale index for %s (source files still exist)", path)
			continue
		}
		idx.removeFile(path)
	}
	for _, path := range gonePaths {
		if idx.sourceFilesExist(path) {
			idx.logger.Printf("keeping stale index for %s (source files still exist)", path)
			continue
		}
		idx.removeFile(path)
	}

	for _, pf := range parsed {
		idx.removeFile(pf.path)
		var uris []string
		for _, doc := range pf.docs.Documents {
			uri := doc.Uri
			if uri == "" {
				idx.logger.Printf("warning: empty URI in %s", pf.path)
				continue
			}
			uri = filepath.ToSlash(uri)
			uris = append(uris, uri)
			idx.indexDocument(uri, doc)
		}
		idx.sdbToURIs[pf.path] = uris
		idx.modTimes[pf.path] = modInfos[pf.path].ModTime()
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

// sourceFilesExist checks if any source files associated with a .semanticdb
// file still exist on disk. Used to avoid eagerly removing index data when
// compilation fails (the .semanticdb is gone but the source is still there).
// Must be called with idx.mu held.
func (idx *Index) sourceFilesExist(sdbPath string) bool {
	uris := idx.sdbToURIs[sdbPath]
	for _, u := range uris {
		srcPath := filepath.Join(idx.sourceRoot, filepath.FromSlash(u))
		if _, err := os.Stat(srcPath); err == nil {
			return true
		}
	}
	return false
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

	// Remove ownerMembers, symbolType, and generics entries for symbols from this document.
	ownersToUpdate := make(map[string]struct{})
	for _, sym := range docSymbols {
		delete(idx.symbolType, sym)
		delete(idx.symbolDeclType, sym)
		delete(idx.classTypeParams, sym)
		delete(idx.parentTypes, sym)
		if owner := extractOwner(sym); owner != "" {
			ownersToUpdate[owner] = struct{}{}
		}
	}
	for owner := range ownersToUpdate {
		if members, ok := idx.ownerMembers[owner]; ok {
			filtered := members[:0]
			for _, m := range members {
				if m.URI != uri {
					filtered = append(filtered, m)
				}
			}
			if len(filtered) > 0 {
				idx.ownerMembers[owner] = filtered
			} else {
				delete(idx.ownerMembers, owner)
			}
		}
	}

	// Remove typeBySimpleName entries.
	for _, s := range idx.fileSymbols[uri] {
		if isTypeKind(s.Kind) {
			name := strings.ToLower(s.Name)
			if syms, ok := idx.typeBySimpleName[name]; ok {
				filtered := syms[:0]
				for _, sym := range syms {
					if sym.URI != uri {
						filtered = append(filtered, sym)
					}
				}
				if len(filtered) > 0 {
					idx.typeBySimpleName[name] = filtered
				} else {
					delete(idx.typeBySimpleName, name)
				}
			}
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

func (idx *Index) indexDocument(uri string, doc *sdb.TextDocument) {
	uri = idx.intern(filepath.ToSlash(uri))

	// Index symbol definitions.
	for _, sym := range doc.Symbols {
		symStr := idx.intern(sym.Symbol)
		var doc string
		if sym.Documentation != nil {
			doc = sym.Documentation.Message
		}
		s := &Symbol{
			Name:      sym.DisplayName,
			Symbol:    symStr,
			Kind:      sym.Kind,
			URI:       uri,
			Signature: buildSignatureInfo(sym.DisplayName, sym.Signature),
			Doc:       doc,
			IsStatic:  sym.Properties&int32(sdb.SymbolInformation_STATIC) != 0,
		}
		idx.definitions[symStr] = append(idx.definitions[symStr], s)
		idx.fileSymbols[uri] = append(idx.fileSymbols[uri], s)

		// Build typeBySimpleName index.
		if isTypeKind(sym.Kind) {
			name := strings.ToLower(sym.DisplayName)
			idx.typeBySimpleName[name] = append(idx.typeBySimpleName[name], s)
		}

		// Build ownerMembers: extract owner type from symbol string.
		if owner := extractOwner(symStr); owner != "" {
			idx.ownerMembers[owner] = append(idx.ownerMembers[owner], s)
		}

		// Extract type information from signature for completion.
		if sig := sym.Signature; sig != nil {
			if typeSym := extractTypeSym(sig); typeSym != "" {
				idx.symbolType[symStr] = idx.intern(typeSym)
			}
			if te := extractTypeExpr(sig); te != nil {
				idx.symbolDeclType[symStr] = te
			}
		}

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

				// Extract parent types with generic arguments.
				for _, parent := range cs.ClassSignature.Parents {
					if te := typeToExpr(parent); te != nil {
						idx.parentTypes[symStr] = append(idx.parentTypes[symStr], te)
					}
				}

				// Extract class type parameters.
				if tp := cs.ClassSignature.TypeParameters; tp != nil {
					var params []string
					for _, hl := range tp.Hardlinks {
						if hl.Symbol != "" {
							params = append(params, hl.Symbol)
						}
					}
					params = append(params, tp.Symlinks...)
					if len(params) > 0 {
						idx.classTypeParams[symStr] = params
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

	// Sort occurrences by start position for binary search in symbolAt.
	occs := idx.fileOccurrences[uri]
	sort.Slice(occs, func(i, j int) bool {
		ri, rj := occs[i].Range, occs[j].Range
		if ri == nil || rj == nil {
			return ri != nil
		}
		if ri.StartLine != rj.StartLine {
			return ri.StartLine < rj.StartLine
		}
		return ri.StartCharacter < rj.StartCharacter
	})
}

// extractOwner returns the owner type symbol for a member symbol.
// e.g. "com/example/Foo#bar()." → "com/example/Foo#"
// Returns "" for type-level symbols (e.g. "com/example/Foo#").
func extractOwner(sym string) string {
	lastHash := strings.LastIndex(sym, "#")
	if lastHash == -1 || lastHash == len(sym)-1 {
		return "" // no hash or hash is the last char (it's a type itself)
	}
	return sym[:lastHash+1]
}

// typeToExpr converts a SemanticDB Type to a TypeExpr (recursive, handles TypeRef with type_arguments).
func typeToExpr(t *sdb.Type) *TypeExpr {
	if t == nil {
		return nil
	}
	tr, ok := t.SealedValue.(*sdb.Type_TypeRef)
	if !ok {
		return nil
	}
	te := &TypeExpr{Sym: tr.TypeRef.Symbol}
	for _, arg := range tr.TypeRef.TypeArguments {
		if a := typeToExpr(arg); a != nil {
			te.Args = append(te.Args, a)
		}
	}
	return te
}

// extractTypeExpr extracts the type as a TypeExpr from a SemanticDB Signature.
// For methods: returns the return type. For values/fields: returns the declared type.
func extractTypeExpr(sig *sdb.Signature) *TypeExpr {
	switch s := sig.SealedValue.(type) {
	case *sdb.Signature_MethodSignature:
		return typeToExpr(s.MethodSignature.ReturnType)
	case *sdb.Signature_ValueSignature:
		return typeToExpr(s.ValueSignature.Tpe)
	default:
		return nil
	}
}

// extractTypeSym extracts the type symbol from a SemanticDB Signature.
// For methods: returns the return type symbol.
// For values/fields: returns the declared type symbol.
func extractTypeSym(sig *sdb.Signature) string {
	switch s := sig.SealedValue.(type) {
	case *sdb.Signature_MethodSignature:
		return typeRefSymbol(s.MethodSignature.ReturnType)
	case *sdb.Signature_ValueSignature:
		return typeRefSymbol(s.ValueSignature.Tpe)
	default:
		return ""
	}
}

// typeRefSymbol extracts the symbol string from a Type if it's a TypeRef.
func typeRefSymbol(t *sdb.Type) string {
	if t == nil {
		return ""
	}
	if tr, ok := t.SealedValue.(*sdb.Type_TypeRef); ok {
		return tr.TypeRef.Symbol
	}
	return ""
}
