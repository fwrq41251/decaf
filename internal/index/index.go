package index

import (
	"fmt"
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
}

// NewIndex creates a new empty index.
func NewIndex(logger *log.Logger, sourceRoot string) *Index {
	return &Index{
		logger:          logger,
		sourceRoot:      sourceRoot,
		definitions:     make(map[string][]Symbol),
		references:      make(map[string][]Occurrence),
		fileOccurrences: make(map[string][]Occurrence),
		fileSymbols:     make(map[string][]Symbol),
	}
}

// Load scans the workspace for .semanticdb files and indexes them.
func (idx *Index) Load() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Clear existing data.
	idx.definitions = make(map[string][]Symbol)
	idx.references = make(map[string][]Occurrence)
	idx.fileOccurrences = make(map[string][]Occurrence)
	idx.fileSymbols = make(map[string][]Symbol)

	var files []string
	err := filepath.Walk(idx.sourceRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".semanticdb") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking source root: %w", err)
	}

	idx.logger.Printf("found %d .semanticdb files", len(files))

	for _, f := range files {
		if err := idx.indexFile(f); err != nil {
			idx.logger.Printf("warning: failed to index %s: %v", f, err)
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
	idx.logger.Printf("indexed %d definitions, %d references", totalDefs, totalRefs)

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
		idx.indexDocument(uri, doc)
	}

	return nil
}

func (idx *Index) indexDocument(uri string, doc *sdb.TextDocument) {
	// Index symbol definitions.
	for _, sym := range doc.Symbols {
		s := Symbol{
			Name:   sym.DisplayName,
			Symbol: sym.Symbol,
			Kind:   sym.Kind,
			URI:    uri,
		}
		idx.definitions[sym.Symbol] = append(idx.definitions[sym.Symbol], s)
		idx.fileSymbols[uri] = append(idx.fileSymbols[uri], s)
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
	if sym == "" {
		return nil
	}

	return idx.definitions[sym]
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

	return idx.references[sym]
}

// SymbolAt returns the SemanticDB symbol string at the given position.
func (idx *Index) symbolAt(uri string, line, character int) string {
	occs := idx.fileOccurrences[uri]
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

// toRelativeURI converts a file:// URI to a relative path matching SemanticDB URIs.
func (idx *Index) toRelativeURI(uri string) string {
	path := strings.TrimPrefix(uri, "file://")
	rel, err := filepath.Rel(idx.sourceRoot, path)
	if err != nil {
		return uri
	}
	return rel
}

// SourceRoot returns the workspace source root path.
func (idx *Index) SourceRoot() string {
	return idx.sourceRoot
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
