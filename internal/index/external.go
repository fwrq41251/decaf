package index

import (
	"archive/zip"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"github.com/fwrq41251/decaf/internal/uri"
)

func (idx *Index) resolveExternalSymbol(sym string) *Symbol {
	idx.logger.Printf("Resolving external symbol: %s", sym)

	// Clean the symbol: strip trailing SemanticDB markers.
	cleanSym := strings.TrimRight(sym, "#.:().")
	if leftParenIdx := strings.Index(cleanSym, "("); leftParenIdx != -1 {
		cleanSym = cleanSym[:leftParenIdx]
	}
	idx.logger.Printf("Cleaned symbol for resolution: %s", cleanSym)

	// Iteratively probe for the source file by stripping segments from the end.
	// This handles nested classes like java/util/Map.Entry (in Map.java) or
	// com/example/Outer#Inner (in Outer.java).
	parts := strings.FieldsFunc(cleanSym, func(r rune) bool {
		return r == '/' || r == '.' || r == '#'
	})
	idx.logger.Printf("Symbol parts: %v", parts)

	// Read JDK root and dependency sources once.
	idx.mu.RLock()
	jdkRoot := idx.jdkSourceRoot
	depSources := make([]string, len(idx.dependencySources))
	copy(depSources, idx.dependencySources)
	idx.mu.RUnlock()

	idx.logger.Printf("JDK root: %s, Dependency sources count: %d", jdkRoot, len(depSources))

	// Probing from most specific (all parts) to least specific (at least one part).
	for i := len(parts); i > 0; i-- {
		relPath := strings.Join(parts[:i], "/") + ".java"
		idx.logger.Printf("Probing relPath: %s", relPath)

		// Check cache.
		if cachedPath, ok := idx.externalCache.Load(relPath); ok {
			idx.logger.Printf("Found in cache: %s -> %s", relPath, cachedPath)
			return idx.createExternalSymbol(sym, cachedPath.(string))
		}

		// Search in JDK.
		if jdkRoot != "" {
			if s := idx.tryResolveFromContainer(jdkRoot, relPath, sym); s != nil {
				idx.logger.Printf("Resolved from JDK: %s", relPath)
				return s
			}
		}

		// Search in dependencies.
		for _, jar := range depSources {
			if s := idx.tryResolveFromContainer(jar, relPath, sym); s != nil {
				idx.logger.Printf("Resolved from dependency %s: %s", jar, relPath)
				return s
			}
		}
	}

	idx.logger.Printf("Failed to resolve external symbol: %s", sym)
	return nil
}

func (idx *Index) tryResolveFromContainer(container, relPath, originalSym string) *Symbol {
	info, err := os.Stat(container)
	if err != nil {
		idx.logger.Printf("Container stat error: %s: %v", container, err)
		return nil
	}

	if info.IsDir() {
		foundPath := filepath.Join(container, relPath)
		if _, err := os.Stat(foundPath); err == nil {
			idx.logger.Printf("Found on disk: %s", foundPath)
			idx.externalCache.Store(relPath, foundPath)
			return idx.createExternalSymbol(originalSym, foundPath)
		}
	} else if strings.HasSuffix(container, ".zip") || strings.HasSuffix(container, ".jar") {
		if foundPath := idx.findAndExtractFromJar(container, relPath); foundPath != "" {
			idx.logger.Printf("Found in JAR/ZIP: %s!/%s -> %s", container, relPath, foundPath)
			idx.externalCache.Store(relPath, foundPath)
			return idx.createExternalSymbol(originalSym, foundPath)
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
