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
	scanRoots, watchRoots := idx.discoverScanAndWatchRoots()

	idx.mu.RLock()
	hasWatcher := idx.watcher != nil
	rootsMatch := sameRoots(idx.scanRoots, scanRoots) && sameRoots(idx.watchRoots, watchRoots)
	idx.mu.RUnlock()

	if hasWatcher && rootsMatch {
		return idx.loadFromWatcher()
	}
	return idx.loadFull(scanRoots, watchRoots)
}

// loadFull walks the entire workspace, indexes all .semanticdb files,
// and starts the file watcher for future incremental loads.
//
// File I/O and protobuf unmarshaling run lock-free; only the final index
// mutation step acquires the write lock to minimize blocking of LSP queries.
func (idx *Index) loadFull(scanRoots, watchRoots []string) error {
	// Phase 1: Walk the directory tree (lock-free, sourceRoot is immutable).
	current, err := idx.collectCurrentSemanticDBFiles(scanRoots)
	if err != nil {
		return fmt.Errorf("walking source root: %w", err)
	}

	// Phase 2: Diff against previous modTimes under a brief read lock.
	idx.mu.RLock()
	var toIndex []string
	for path, info := range current {
		prev, ok := idx.modTimes[path]
		if !ok || info.ModTime().After(prev) || idx.hasMissingIndexedSourcesLocked(path) {
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
			if idx.shouldKeepMissingFile(path, scanRoots) {
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
			if idx.shouldCompactStorage() {
				idx.compactStorage()
			}
		}
		idx.logStats()
	}

	idx.scanRoots = append(idx.scanRoots[:0], scanRoots...)
	oldWatcher := idx.watcher
	idx.watcher = nil
	idx.watchRoots = nil
	idx.mu.Unlock()

	// Start watcher for subsequent incremental loads.
	w, err := newWatcher(idx)
	if err != nil {
		idx.logger.Printf("warning: failed to start file watcher: %v (falling back to full scan)", err)
	} else {
		w.watchRoots(watchRoots)
		idx.mu.Lock()
		idx.watcher = w
		idx.watchRoots = append(idx.watchRoots[:0], watchRoots...)
		idx.mu.Unlock()
		idx.logger.Printf("file watcher started for %d root(s)", len(watchRoots))
	}
	if oldWatcher != nil {
		_ = oldWatcher.close()
	}
	return nil
}

func (idx *Index) collectCurrentSemanticDBFiles(roots []string) (map[string]os.FileInfo, error) {
	current := make(map[string]os.FileInfo)
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !info.IsDir() {
			continue
		}
		err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
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
			return nil, err
		}
	}
	return current, nil
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

			// Filter stale documents: skip entries where the source file no longer exists.
			filtered := docs.Documents[:0]
			for _, doc := range docs.Documents {
				if doc.Uri == "" {
					continue
				}
				srcPath := filepath.Join(idx.sourceRoot, filepath.FromSlash(doc.Uri))
				if _, err := os.Stat(srcPath); err != nil {
					idx.logger.Printf("skipping stale document %s (source not found)", doc.Uri)
					continue
				}
				filtered = append(filtered, doc)
			}
			docs.Documents = filtered

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

	idx.mu.RLock()
	for path := range idx.modTimes {
		if idx.hasMissingIndexedSourcesLocked(path) {
			dirty = append(dirty, path)
		}
	}
	idx.mu.RUnlock()

	dirty = uniquePaths(dirty)
	removed = uniquePaths(removed)

	if len(dirty) == 0 && len(removed) == 0 {
		idx.mu.RLock()
		hasFiles := len(idx.modTimes) > 0
		idx.mu.RUnlock()
		if hasFiles {
			idx.logger.Printf("index up-to-date (no watcher events)")
			return nil
		}
		// No watcher events and no files indexed yet — fall back to full scan
		// to catch files created before the watcher was ready.
		idx.logger.Printf("no watcher events and empty index, falling back to full scan")
		scanRoots, watchRoots := idx.discoverScanAndWatchRoots()
		return idx.loadFull(scanRoots, watchRoots)
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
		if idx.shouldCompactStorage() {
			idx.compactStorage()
		}
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

// LogStatsSnapshot emits a detailed index/memory snapshot for profiling.
func (idx *Index) LogStatsSnapshot(label string) {
	idx.mu.RLock()
	definitions := 0
	for _, defs := range idx.definitions {
		definitions += len(defs)
	}
	references := 0
	for _, refs := range idx.references {
		references += len(refs)
	}
	fileOccurrences := 0
	for _, occs := range idx.fileOccurrences {
		fileOccurrences += len(occs)
	}
	ownerMembersEntries := 0
	for _, members := range idx.ownerMembers {
		ownerMembersEntries += len(members)
	}
	implementors := len(idx.implementors)
	childToParents := len(idx.childToParents)
	typeBySimpleName := len(idx.typeBySimpleName)
	ownerMembers := len(idx.ownerMembers)
	symbolType := len(idx.symbolType)
	symbolDeclType := len(idx.symbolDeclType)
	symbolDeclParamTypes := len(idx.symbolDeclParamTypes)
	classTypeParams := len(idx.classTypeParams)
	parentTypes := len(idx.parentTypes)
	internPool := len(idx.internPool)
	modTimes := len(idx.modTimes)
	sdbToURIs := len(idx.sdbToURIs)
	indexedJARs := len(idx.indexedJARs)
	classLocations := len(idx.classLocations)
	fullyIndexedClasses := len(idx.fullyIndexedClasses)
	externalTypes := len(idx.externalTypeInfo)
	files := len(idx.fileOccurrences)
	fileSymbols := len(idx.fileSymbols)
	idx.mu.RUnlock()

	idx.logger.Printf("--- stats snapshot [%s] ---", label)
	idx.logger.Printf("index stats: definitions=%d references=%d fileOccurrences=%d files=%d fileSymbols=%d",
		definitions, references, fileOccurrences, files, fileSymbols)
	idx.logger.Printf("index stats: implementors=%d childToParents=%d typeBySimpleName=%d ownerMembers=%d(%d entries)",
		implementors, childToParents, typeBySimpleName, ownerMembers, ownerMembersEntries)
	idx.logger.Printf("index stats: symbolType=%d symbolDeclType=%d symbolDeclParamTypes=%d classTypeParams=%d parentTypes=%d",
		symbolType, symbolDeclType, symbolDeclParamTypes, classTypeParams, parentTypes)
	idx.logger.Printf("index stats: internPool=%d modTimes=%d sdbToURIs=%d",
		internPool, modTimes, sdbToURIs)
	idx.logger.Printf("index stats: indexedJARs=%d classLocations=%d externalTypes=%d fullyIndexedClasses=%d",
		indexedJARs, classLocations, externalTypes, fullyIndexedClasses)
}

// Close stops the file watcher. Safe to call multiple times.
func (idx *Index) Close() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.watcher != nil {
		_ = idx.watcher.close()
		idx.watcher = nil
	}
	idx.scanRoots = nil
	idx.watchRoots = nil
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

// hasMissingIndexedSourcesLocked reports whether any currently indexed document
// for a .semanticdb file points at a source file that no longer exists.
// Must be called with idx.mu held.
func (idx *Index) hasMissingIndexedSourcesLocked(sdbPath string) bool {
	uris := idx.sdbToURIs[sdbPath]
	for _, u := range uris {
		srcPath := filepath.Join(idx.sourceRoot, filepath.FromSlash(u))
		if _, err := os.Stat(srcPath); err != nil {
			return true
		}
	}
	return false
}

// shouldKeepMissingFile reports whether a missing .semanticdb file should keep
// its stale index entries temporarily. If the file is outside the currently
// configured scan roots, the index must be dropped immediately because this is
// a root switch, not a transient compile failure.
// Must be called with idx.mu held.
func (idx *Index) shouldKeepMissingFile(sdbPath string, scanRoots []string) bool {
	if !pathWithinAnyRoot(sdbPath, scanRoots) {
		return false
	}
	return idx.sourceFilesExist(sdbPath)
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

func uniquePaths(paths []string) []string {
	if len(paths) < 2 {
		return paths
	}
	seen := make(map[string]struct{}, len(paths))
	out := paths[:0]
	for _, path := range paths {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

// removeDocument removes all index entries for a given document URI.
func (idx *Index) removeDocument(uri string) {
	// Collect symbols defined in this document for cleanup.
	var docSymbols []string
	for _, id := range idx.fileSymbols[uri] {
		docSymbols = append(docSymbols, idx.symbol(id).Symbol)
	}

	// Remove definitions belonging to this URI.
	for _, sym := range docSymbols {
		if defs, ok := idx.definitions[sym]; ok {
			filtered := defs[:0]
			for _, id := range defs {
				if idx.symbol(id).URI != uri {
					filtered = append(filtered, id)
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
	uriID, hasURIID := idx.lookupStringID(uri)
	if refSyms, ok := idx.uriRefSymbols[uri]; ok {
		for _, sym := range refSyms {
			if refs, ok := idx.references[sym]; ok {
				filtered := refs[:0]
				for _, occID := range refs {
					if !hasURIID || idx.occurrence(occID).URIID != uriID {
						filtered = append(filtered, occID)
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
		delete(idx.symbolDeclParamTypes, sym)
		delete(idx.classTypeParams, sym)
		delete(idx.parentTypes, sym)
		if owner := extractOwner(sym); owner != "" {
			ownersToUpdate[owner] = struct{}{}
		}
	}
	for owner := range ownersToUpdate {
		if members, ok := idx.ownerMembers[owner]; ok {
			filtered := members[:0]
			for _, id := range members {
				if idx.symbol(id).URI != uri {
					filtered = append(filtered, id)
				}
			}
			if len(filtered) > 0 {
				idx.ownerMembers[owner] = filtered
			} else {
				delete(idx.ownerMembers, owner)
			}
		}
	}

	// Remove simple-name index entries.
	for _, id := range idx.fileSymbols[uri] {
		s := idx.symbol(id)
		name := strings.ToLower(s.Name)
		if IsTypeSymbol(*s) {
			if syms, ok := idx.typeBySimpleName[name]; ok {
				filtered := syms[:0]
				for _, sid := range syms {
					if idx.symbol(sid).URI != uri {
						filtered = append(filtered, sid)
					}
				}
				if len(filtered) > 0 {
					idx.typeBySimpleName[name] = filtered
				} else {
					delete(idx.typeBySimpleName, name)
				}
			}
		} else {
			if syms, ok := idx.memberBySimpleName[name]; ok {
				filtered := syms[:0]
				for _, sid := range syms {
					if idx.symbol(sid).URI != uri {
						filtered = append(filtered, sid)
					}
				}
				if len(filtered) > 0 {
					idx.memberBySimpleName[name] = filtered
				} else {
					delete(idx.memberBySimpleName, name)
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
	delete(idx.internPool, uri)
}

// shouldCompactStorage reports whether tombstones in the symbol/occurrence
// arenas have grown large enough to justify rebuilding them. Must be called
// with idx.mu held.
func (idx *Index) shouldCompactStorage() bool {
	liveSymbols := 0
	for _, ids := range idx.fileSymbols {
		liveSymbols += len(ids)
	}
	liveOccurrences := 0
	for _, ids := range idx.fileOccurrences {
		liveOccurrences += len(ids)
	}

	return arenaNeedsCompaction(len(idx.symbols), liveSymbols) ||
		arenaNeedsCompaction(len(idx.occurrences), liveOccurrences)
}

func arenaNeedsCompaction(total, live int) bool {
	if total == 0 || live >= total {
		return false
	}
	dead := total - live
	return dead > 0 && total*2 >= live*3
}

// compactStorage rebuilds the live symbol/occurrence/string arenas so removed
// documents do not leave unreachable entries behind forever. Must be called
// with idx.mu held.
func (idx *Index) compactStorage() {
	if len(idx.symbols) == 0 && len(idx.occurrences) == 0 {
		return
	}

	oldSymbols := idx.symbols
	oldOccurrences := idx.occurrences
	oldStrings := idx.strings

	newInternPool := make(map[string]string, len(idx.internPool))
	intern := func(s string) string {
		if s == "" {
			return ""
		}
		if interned, ok := newInternPool[s]; ok {
			return interned
		}
		newInternPool[s] = s
		return s
	}

	newStringIDs := make(map[string]StringID, len(idx.stringIDs))
	newStrings := make([]string, 0, len(oldStrings))
	stringID := func(s string) StringID {
		s = intern(s)
		if id, ok := newStringIDs[s]; ok {
			return id
		}
		newStrings = append(newStrings, s)
		id := StringID(len(newStrings))
		newStringIDs[s] = id
		return id
	}

	newSymbols := make([]Symbol, 0, len(oldSymbols))
	symbolRemap := make(map[SymbolID]SymbolID, len(oldSymbols))
	remapSymbolID := func(oldID SymbolID) SymbolID {
		if newID, ok := symbolRemap[oldID]; ok {
			return newID
		}
		s := oldSymbols[int(oldID)]
		s.Symbol = intern(s.Symbol)
		s.URI = intern(s.URI)
		newSymbols = append(newSymbols, s)
		newID := SymbolID(len(newSymbols) - 1)
		symbolRemap[oldID] = newID
		stringID(s.Symbol)
		stringID(s.URI)
		return newID
	}

	newOccurrences := make([]storedOccurrence, 0, len(oldOccurrences))
	occRemap := make(map[OccurrenceID]OccurrenceID, len(oldOccurrences))
	lookupOldString := func(id StringID) string {
		if id == 0 || int(id) > len(oldStrings) {
			return ""
		}
		return oldStrings[int(id)-1]
	}
	remapOccurrenceID := func(oldID OccurrenceID) OccurrenceID {
		if newID, ok := occRemap[oldID]; ok {
			return newID
		}
		occ := oldOccurrences[int(oldID)-1]
		newOccurrences = append(newOccurrences, storedOccurrence{
			SymbolID: stringID(lookupOldString(occ.SymbolID)),
			Role:     occ.Role,
			URIID:    stringID(lookupOldString(occ.URIID)),
			Range:    occ.Range,
		})
		newID := OccurrenceID(len(newOccurrences))
		occRemap[oldID] = newID
		return newID
	}

	remapSymbolSlice := func(ids []SymbolID) []SymbolID {
		if len(ids) == 0 {
			return nil
		}
		out := make([]SymbolID, len(ids))
		for i, id := range ids {
			out[i] = remapSymbolID(id)
		}
		return out
	}

	remapOccurrenceSlice := func(ids []OccurrenceID) []OccurrenceID {
		if len(ids) == 0 {
			return nil
		}
		out := make([]OccurrenceID, len(ids))
		for i, id := range ids {
			out[i] = remapOccurrenceID(id)
		}
		return out
	}

	for sym, ids := range idx.definitions {
		idx.definitions[intern(sym)] = remapSymbolSlice(ids)
		if canon := intern(sym); canon != sym {
			delete(idx.definitions, sym)
		}
	}
	for uri, ids := range idx.fileSymbols {
		idx.fileSymbols[intern(uri)] = remapSymbolSlice(ids)
		if canon := intern(uri); canon != uri {
			delete(idx.fileSymbols, uri)
		}
	}
	for name, ids := range idx.typeBySimpleName {
		idx.typeBySimpleName[name] = remapSymbolSlice(ids)
	}
	for name, ids := range idx.memberBySimpleName {
		idx.memberBySimpleName[name] = remapSymbolSlice(ids)
	}
	for owner, ids := range idx.ownerMembers {
		idx.ownerMembers[intern(owner)] = remapSymbolSlice(ids)
		if canon := intern(owner); canon != owner {
			delete(idx.ownerMembers, owner)
		}
	}
	for sym, ids := range idx.references {
		idx.references[intern(sym)] = remapOccurrenceSlice(ids)
		if canon := intern(sym); canon != sym {
			delete(idx.references, sym)
		}
	}
	for uri, ids := range idx.fileOccurrences {
		idx.fileOccurrences[intern(uri)] = remapOccurrenceSlice(ids)
		if canon := intern(uri); canon != uri {
			delete(idx.fileOccurrences, uri)
		}
	}
	for uri, syms := range idx.uriRefSymbols {
		for i, sym := range syms {
			syms[i] = intern(sym)
		}
		idx.uriRefSymbols[intern(uri)] = syms
		if canon := intern(uri); canon != uri {
			delete(idx.uriRefSymbols, uri)
		}
	}
	for child, parents := range idx.childToParents {
		for i, parent := range parents {
			parents[i] = intern(parent)
		}
		idx.childToParents[intern(child)] = parents
		if canon := intern(child); canon != child {
			delete(idx.childToParents, child)
		}
	}
	for parent, children := range idx.implementors {
		for i, child := range children {
			children[i] = intern(child)
		}
		idx.implementors[intern(parent)] = children
		if canon := intern(parent); canon != parent {
			delete(idx.implementors, parent)
		}
	}
	for path, uris := range idx.sdbToURIs {
		for i, uri := range uris {
			uris[i] = intern(uri)
		}
		idx.sdbToURIs[path] = uris
	}
	for sym, typeSym := range idx.symbolType {
		delete(idx.symbolType, sym)
		idx.symbolType[intern(sym)] = intern(typeSym)
	}
	for sym, params := range idx.classTypeParams {
		delete(idx.classTypeParams, sym)
		for i, param := range params {
			params[i] = intern(param)
		}
		idx.classTypeParams[intern(sym)] = params
	}

	idx.symbols = newSymbols
	idx.occurrences = newOccurrences
	idx.strings = newStrings
	idx.stringIDs = newStringIDs
	idx.internPool = newInternPool
}

func (idx *Index) indexDocument(uri string, doc *sdb.TextDocument) {
	uri = idx.intern(filepath.ToSlash(uri))

	// Build symbol lookup for resolving symlinks in signatures.
	symbolLookup := make(map[string]*sdb.SymbolInformation, len(doc.Symbols))
	for _, sym := range doc.Symbols {
		symbolLookup[sym.Symbol] = sym
	}

	// Index symbol definitions.
	for _, sym := range doc.Symbols {
		symStr := idx.intern(sym.Symbol)
		var docStr string
		if sym.Documentation != nil {
			docStr = sym.Documentation.Message
		}

		kind := sym.Kind
		owner := extractOwner(symStr)
		isEnumConstant := isEnumValueSymbol(sym, owner)
		if isEnumConstant {
			kind = sdb.SymbolInformation_FIELD
		}
		displayName := sym.DisplayName
		if isEnumConstant {
			displayName = normalizeEnumConstantName(displayName)
		}

		sid := idx.addSymbol(Symbol{
			Name:       displayName,
			Symbol:     symStr,
			Kind:       kind,
			URI:        uri,
			Signature:  buildSignatureInfo(displayName, sym.Signature, symbolLookup),
			Doc:        docStr,
			Visibility: visibilityFromSemanticDBAccess(sym.Access),
			IsStatic:   sym.Properties&int32(sdb.SymbolInformation_STATIC) != 0,
			IsAbstract: sym.Properties&int32(sdb.SymbolInformation_ABSTRACT) != 0,
			IsFinal:    sym.Properties&int32(sdb.SymbolInformation_FINAL) != 0,
			IsSealed:   sym.Properties&int32(sdb.SymbolInformation_SEALED) != 0,
			IsOverride: sym.Properties&int32(sdb.SymbolInformation_OVERRIDE) != 0,
		})
		s := idx.symbol(sid)

		// For enum constants, override the signature to field-style (name: Type)
		// instead of method-style with parameters.
		if isEnumConstant {
			if typeSym := extractTypeSym(sym.Signature); typeSym != "" {
				typeName := SimpleTypeName(typeSym)
				s.Signature = &SignatureInfo{Label: fmt.Sprintf("%s: %s", displayName, typeName)}
			}
		}
		idx.definitions[symStr] = append(idx.definitions[symStr], sid)
		idx.fileSymbols[uri] = append(idx.fileSymbols[uri], sid)

		// Build simple-name indexes for completion.
		name := strings.ToLower(displayName)
		if IsTypeSymbolString(symStr) || IsTypeKind(kind) {
			idx.typeBySimpleName[name] = append(idx.typeBySimpleName[name], sid)
		} else {
			idx.memberBySimpleName[name] = append(idx.memberBySimpleName[name], sid)
		}

		// Build ownerMembers: extract owner type from symbol string.
		if owner != "" {
			idx.ownerMembers[owner] = append(idx.ownerMembers[owner], sid)
		}

		// Extract type information from signature for completion.
		if sig := sym.Signature; sig != nil {
			if typeSym := extractTypeSym(sig); typeSym != "" {
				idx.symbolType[symStr] = typeSym
			}
			if te := extractTypeExpr(sig); te != nil {
				idx.symbolDeclType[symStr] = te
			}
			if pts := extractParamTypeExprs(sig); len(pts) > 0 {
				idx.symbolDeclParamTypes[symStr] = pts
			}
		}

		// Build implementors index from class signatures.
		if sig := sym.Signature; sig != nil {
			if cs, ok := sig.SealedValue.(*sdb.Signature_ClassSignature); ok {
				for _, parent := range cs.ClassSignature.Parents {
					if tr, ok := parent.SealedValue.(*sdb.Type_TypeRef); ok {
						parentSym := tr.TypeRef.Symbol
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
	uriID := idx.stringID(uri)
	refSymSet := make(map[string]struct{})
	for _, occ := range doc.Occurrences {
		occSym := idx.intern(occ.Symbol)
		o := storedOccurrence{
			SymbolID: idx.stringID(occSym),
			Role:     occ.Role,
			URIID:    uriID,
			Range:    FromSDB(occ.Range),
		}
		occID := idx.addOccurrence(o)

		if occ.Role == sdb.SymbolOccurrence_DEFINITION {
			// Update the definition's range if we have it from occurrences.
			if defs, ok := idx.definitions[occSym]; ok {
				for _, id := range defs {
					d := idx.symbol(id)
					if d.URI == uri && d.Range.IsEmpty() {
						d.Range = FromSDB(occ.Range)
					}
				}
			}
		} else {
			idx.references[occSym] = append(idx.references[occSym], occID)
			refSymSet[occSym] = struct{}{}
		}

		idx.fileOccurrences[uri] = append(idx.fileOccurrences[uri], occID)
	}
	if len(refSymSet) > 0 {
		refSyms := make([]string, 0, len(refSymSet))
		for sym := range refSymSet {
			refSyms = append(refSyms, sym)
		}
		idx.uriRefSymbols[uri] = refSyms
	}

	// Sort occurrences by start position for binary search in symbolAt.
	occs := idx.fileOccurrences[uri]
	sort.Slice(occs, func(i, j int) bool {
		ri, rj := idx.occurrence(occs[i]).Range, idx.occurrence(occs[j]).Range
		if ri.IsEmpty() || rj.IsEmpty() {
			return !ri.IsEmpty()
		}
		if ri.StartLine != rj.StartLine {
			return ri.StartLine < rj.StartLine
		}
		return ri.StartCharacter < rj.StartCharacter
	})
}

func visibilityFromSemanticDBAccess(access *sdb.Access) Visibility {
	if access == nil {
		return VisibilityPackagePrivate
	}
	switch access.GetSealedValue().(type) {
	case *sdb.Access_PublicAccess:
		return VisibilityPublic
	case *sdb.Access_ProtectedAccess, *sdb.Access_ProtectedThisAccess, *sdb.Access_ProtectedWithinAccess:
		return VisibilityProtected
	case *sdb.Access_PrivateAccess, *sdb.Access_PrivateThisAccess, *sdb.Access_PrivateWithinAccess:
		return VisibilityPrivate
	default:
		return VisibilityPackagePrivate
	}
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

// extractParamTypeExprs extracts parameter types as TypeExprs from a SemanticDB MethodSignature.
func extractParamTypeExprs(sig *sdb.Signature) []*TypeExpr {
	ms, ok := sig.SealedValue.(*sdb.Signature_MethodSignature)
	if !ok || ms.MethodSignature == nil {
		return nil
	}
	var result []*TypeExpr
	for _, paramList := range ms.MethodSignature.ParameterLists {
		for _, hl := range paramList.Hardlinks {
			if hl.Signature == nil {
				continue
			}
			vs, ok := hl.Signature.SealedValue.(*sdb.Signature_ValueSignature)
			if !ok || vs.ValueSignature == nil {
				continue
			}
			te := typeToExpr(vs.ValueSignature.Tpe)
			result = append(result, te)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func isEnumValueSymbol(sym *sdb.SymbolInformation, owner string) bool {
	if sym == nil || owner == "" {
		return false
	}
	if sym.Properties&int32(sdb.SymbolInformation_ENUM) == 0 {
		return false
	}
	if sym.Signature == nil {
		return false
	}
	vs, ok := sym.Signature.SealedValue.(*sdb.Signature_ValueSignature)
	if !ok || vs.ValueSignature == nil {
		return false
	}
	return typeRefSymbol(vs.ValueSignature.Tpe) == owner
}

func normalizeEnumConstantName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}

	end := 0
	for end < len(name) {
		ch := name[end]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '$' {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return name
	}
	return name[:end]
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
