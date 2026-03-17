package index

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"google.golang.org/protobuf/proto"
)

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
