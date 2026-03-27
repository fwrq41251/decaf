package index

import (
	"log"
	"path/filepath"
	"sync"
	"time"
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
	// simple name (lowercase) -> list of matching type symbols
	typeBySimpleName map[string][]*Symbol
	// ownerMembers maps a type symbol to its direct member definitions.
	// e.g. "com/example/Foo#" → [bar, baz, ...]
	ownerMembers map[string][]*Symbol
	// symbolType maps a symbol to its type's SemanticDB symbol string.
	// For fields/params: the declared type. For methods: the return type.
	// e.g. "com/example/Foo#items." → "java/util/List#"
	symbolType map[string]string

	// symbolDeclType maps a symbol to its declared type as a TypeExpr (preserving generics).
	// For fields/params: the declared type. For methods: the return type.
	symbolDeclType map[string]*TypeExpr

	// classTypeParams maps a class symbol to its type parameter symbols (in declaration order).
	// e.g. "java/util/List#" → ["java/util/List#[E]"]
	classTypeParams map[string][]string

	// parentTypes maps a child class symbol to its parent types with generic arguments.
	// e.g. "com/example/StringList#" → [{Sym:"java/util/ArrayList#", Args:[{Sym:"java/lang/String#"}]}]
	parentTypes map[string][]*TypeExpr

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
	// set of third-party source JARs (file paths)
	dependencySources   []string
	depSourcesSet       map[string]struct{}
	// cache for external symbol resolutions (relPath -> extractedPath)
	externalCache sync.Map

	// set of classpath JARs already indexed by the class indexer
	indexedJARs map[string]struct{}
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
		typeBySimpleName:  make(map[string][]*Symbol),
		ownerMembers:      make(map[string][]*Symbol),
		symbolType:        make(map[string]string),
		symbolDeclType:    make(map[string]*TypeExpr),
		classTypeParams:   make(map[string][]string),
		parentTypes:       make(map[string][]*TypeExpr),
		internPool:        make(map[string]string),
		modTimes:          make(map[string]time.Time),
		sdbToURIs:         make(map[string][]string),
		dependencySources: []string{},
		depSourcesSet:     make(map[string]struct{}),
		indexedJARs:       make(map[string]struct{}),
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
	if _, ok := idx.depSourcesSet[path]; ok {
		return
	}
	idx.logger.Printf("Adding dependency source: %s", path)
	idx.depSourcesSet[path] = struct{}{}
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

// HasFiles returns true if the index contains any indexed files.
func (idx *Index) HasFiles() bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.fileOccurrences) > 0
}

// intern returns a canonical string, ensuring identical strings share memory.
func (idx *Index) intern(s string) string {
	if interned, ok := idx.internPool[s]; ok {
		return interned
	}
	idx.internPool[s] = s
	return s
}
