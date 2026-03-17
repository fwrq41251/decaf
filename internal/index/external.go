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
