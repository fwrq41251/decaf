package index

import (
	"archive/zip"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fwrq41251/decaf/internal/uri"
)

type archiveIndex struct {
	once    sync.Once
	entries []string
	exact   map[string]string
	err     error
}

type cachedSymbolLocation struct {
	found bool
	rng   Range
}

func (idx *Index) resolveExternalSymbol(sym string) *Symbol {
	// Clean the symbol: strip trailing SemanticDB markers.
	cleanSym := strings.TrimRight(sym, "#.:().")
	if leftParenIdx := strings.Index(cleanSym, "("); leftParenIdx != -1 {
		cleanSym = cleanSym[:leftParenIdx]
	}

	// Iteratively probe for the source file by stripping segments from the end.
	// This handles nested classes like java/util/Map.Entry (in Map.java) or
	// com/example/Outer#Inner (in Outer.java).
	parts := strings.FieldsFunc(cleanSym, func(r rune) bool {
		return r == '/' || r == '.' || r == '#'
	})

	// Read JDK root and dependency sources once.
	idx.mu.RLock()
	jdkRoot := idx.jdkSourceRoot
	depSources := make([]string, len(idx.dependencySources))
	copy(depSources, idx.dependencySources)
	idx.mu.RUnlock()

	// Probing from most specific (all parts) to least specific (at least one part).
	for i := len(parts); i > 0; i-- {
		relPath := strings.Join(parts[:i], "/") + ".java"

		// Check cache.
		if cachedPath, ok := idx.externalCache.Load(relPath); ok {
			return idx.createExternalSymbol(sym, cachedPath.(string))
		}
		if _, ok := idx.externalMisses.Load(relPath); ok {
			continue
		}

		// Search in JDK.
		if jdkRoot != "" {
			if s := idx.tryResolveFromContainer(jdkRoot, relPath, sym); s != nil {
				return s
			}
		}

		// Search in dependencies.
		for _, jar := range depSources {
			if s := idx.tryResolveFromContainer(jar, relPath, sym); s != nil {
				return s
			}
		}

		idx.externalMisses.Store(relPath, struct{}{})
	}

	return nil
}

func (idx *Index) tryResolveFromContainer(container, relPath, originalSym string) *Symbol {
	info, err := os.Stat(container)
	if err != nil {
		return nil
	}

	if info.IsDir() {
		foundPath := filepath.Join(container, relPath)
		if _, err := os.Stat(foundPath); err == nil {
			idx.externalCache.Store(relPath, foundPath)
			return idx.createExternalSymbol(originalSym, foundPath)
		}
	} else if strings.HasSuffix(container, ".zip") || strings.HasSuffix(container, ".jar") {
		if foundPath := idx.findAndExtractFromJar(container, relPath); foundPath != "" {
			idx.externalCache.Store(relPath, foundPath)
			return idx.createExternalSymbol(originalSym, foundPath)
		}
	}
	return nil
}

func (idx *Index) createExternalSymbol(sym, path string) *Symbol {
	fileURI := uri.FromPath(path)

	s := &Symbol{
		Symbol: sym,
		URI:    fileURI,
	}

	location := idx.cachedSymbolLocation(path, sym)
	if location.found {
		s.Range = location.rng
	}

	return s
}

func (idx *Index) findAndExtractFromJar(jarPath, relPath string) string {
	targetEntry, ok := idx.lookupArchiveEntry(jarPath, relPath)
	if !ok {
		return ""
	}

	// 2. Extract the file to a writable cache directory.
	// Using os.TempDir keeps tests and sandboxed runs working even when ~/.cache is not writable.
	h := sha1.Sum([]byte(jarPath))
	sanitizedJar := hex.EncodeToString(h[:])

	destDir := filepath.Join(os.TempDir(), "decaf", "lib-src", sanitizedJar)
	destPath := filepath.Join(destDir, targetEntry)

	if _, err := os.Stat(destPath); err == nil {
		return destPath
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return ""
	}

	r, err := zip.OpenReader(jarPath)
	if err != nil {
		idx.logger.Printf("Failed to open JAR %s: %v", jarPath, err)
		return ""
	}
	defer r.Close()

	rc, err := r.Open(targetEntry)
	if err != nil {
		return ""
	}
	defer rc.Close()

	// Atomic extraction: write to a temporary file and then rename.
	tmpFile, err := os.CreateTemp(filepath.Dir(destPath), filepath.Base(destPath)+".tmp-*")
	if err != nil {
		return ""
	}
	tmpPath := tmpFile.Name()

	if _, err := io.Copy(tmpFile, rc); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return ""
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return ""
	}

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

func (idx *Index) cachedSymbolLocation(path, sym string) cachedSymbolLocation {
	key := path + "\x00" + sym
	if cached, ok := idx.symbolLocations.Load(key); ok {
		return cached.(cachedSymbolLocation)
	}

	line, col := FindSymbolLocation(path, sym)
	if line == -1 {
		location := cachedSymbolLocation{}
		idx.symbolLocations.Store(key, location)
		return location
	}

	location := cachedSymbolLocation{
		found: true,
		rng: Range{
			StartLine:      int32(line),
			StartCharacter: int32(col),
			EndLine:        int32(line),
			EndCharacter:   int32(col + len(ExtractShortName(sym))),
		},
	}
	idx.symbolLocations.Store(key, location)
	return location
}

func (idx *Index) lookupArchiveEntry(container, relPath string) (string, bool) {
	archive := idx.getArchiveIndex(container)
	if archive == nil || archive.err != nil {
		return "", false
	}
	if entry, ok := archive.exact[relPath]; ok {
		return entry, true
	}
	for _, entry := range archive.entries {
		if strings.HasSuffix(entry, relPath) {
			return entry, true
		}
	}
	return "", false
}

func (idx *Index) getArchiveIndex(container string) *archiveIndex {
	value, _ := idx.archiveIndexes.LoadOrStore(container, &archiveIndex{})
	archive := value.(*archiveIndex)
	archive.once.Do(func() {
		r, err := zip.OpenReader(container)
		if err != nil {
			archive.err = err
			return
		}
		defer r.Close()

		archive.exact = make(map[string]string, len(r.File))
		for _, file := range r.File {
			name := filepath.ToSlash(file.Name)
			if !strings.HasSuffix(name, ".java") {
				continue
			}
			archive.entries = append(archive.entries, name)
			archive.exact[name] = file.Name
		}
	})
	return archive
}
