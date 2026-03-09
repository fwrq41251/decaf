package setup

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

const (
	semanticdbJavacVersion  = "0.11.2"
	semanticdbJavacGroup    = "com/sourcegraph"
	semanticdbJavacArtifact = "semanticdb-javac"
	mavenCentralBase        = "https://repo1.maven.org/maven2"
)

// semanticdbJavacJarURL returns the Maven Central URL for the jar.
func semanticdbJavacJarURL() string {
	return fmt.Sprintf("%s/%s/%s/%s/%s-%s.jar",
		mavenCentralBase, semanticdbJavacGroup, semanticdbJavacArtifact,
		semanticdbJavacVersion, semanticdbJavacArtifact, semanticdbJavacVersion)
}

// cacheDir returns the decaf cache directory (~/.cache/decaf/).
func cacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".cache", "decaf")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// ensureSemanticDBJavac downloads the semanticdb-javac jar if not cached.
// Returns the absolute path to the jar.
func (s *Setup) ensureSemanticDBJavac(ctx context.Context) (string, error) {
	cache, err := cacheDir()
	if err != nil {
		return "", fmt.Errorf("getting cache dir: %w", err)
	}

	jarName := fmt.Sprintf("%s-%s.jar", semanticdbJavacArtifact, semanticdbJavacVersion)
	jarPath := filepath.Join(cache, jarName)

	// Already cached?
	if _, err := os.Stat(jarPath); err == nil {
		s.logger.Printf("semanticdb-javac already cached at %s", jarPath)
		return jarPath, nil
	}

	// Download from Maven Central.
	url := semanticdbJavacJarURL()
	s.logger.Printf("downloading semanticdb-javac from %s", url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: HTTP %d for %s", resp.StatusCode, url)
	}

	tmpFile := jarPath + ".tmp"
	f, err := os.Create(tmpFile)
	if err != nil {
		return "", err
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmpFile)
		return "", fmt.Errorf("writing jar: %w", err)
	}
	f.Close()

	if err := os.Rename(tmpFile, jarPath); err != nil {
		return "", err
	}

	s.logger.Printf("downloaded semanticdb-javac to %s", jarPath)
	return jarPath, nil
}
