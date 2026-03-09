package index

import (
	"archive/zip"
	"bytes"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func TestExternalSymbolResolution(t *testing.T) {
	tmpDir := t.TempDir()
	
	// 1. Create a dummy JAR file with some Java source.
	jarPath := filepath.Join(tmpDir, "lib.jar")
	createDummyJar(t, jarPath, "com/example/Lib.java", `package com.example;
public class Lib {
    public void hello() {}
}
`)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	idx.AddDependencySource(jarPath)

	// 2. Resolve an external symbol.
	sym := "com/example/Lib#hello()."
	s := idx.resolveExternalSymbol(sym)
	if s == nil {
		t.Fatal("failed to resolve external symbol")
	}

	// Verify the symbol data.
	if s.Symbol != sym {
		t.Errorf("got symbol %q, want %q", s.Symbol, sym)
	}
	if s.Range == nil {
		t.Fatal("expected range to be set")
	}
	if s.Range.StartLine != 2 { // line 3 in file (0-indexed is 2)
		t.Errorf("got line %d, want 2", s.Range.StartLine)
	}

	// 3. Verify caching.
	// Check if the path is in the sync.Map.
	relPath := "com/example/Lib.java"
	cachedPath, ok := idx.externalCache.Load(relPath)
	if !ok {
		t.Fatal("expected result to be cached")
	}
	extractedPath := cachedPath.(string)
	if _, err := os.Stat(extractedPath); err != nil {
		t.Errorf("extracted file does not exist: %v", err)
	}

	// 4. Test cache clearing.
	idx.SetDependencySources([]string{jarPath})
	if _, ok := idx.externalCache.Load(relPath); ok {
		t.Fatal("expected cache to be cleared after SetDependencySources")
	}
}

func createDummyJar(t *testing.T, path string, fileName string, content string) {
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	defer w.Close()

	zf, err := w.Create(fileName)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(zf, content)
	if err != nil {
		t.Fatal(err)
	}
}

func TestJDKSourceResolution(t *testing.T) {
	tmpDir := t.TempDir()
	
	// Mock JDK source directory.
	jdkDir := filepath.Join(tmpDir, "jdk-src")
	relPath := "java/lang/String.java"
	fullPath := filepath.Join(jdkDir, relPath)
	os.MkdirAll(filepath.Dir(fullPath), 0755)
	os.WriteFile(fullPath, []byte("package java.lang;\npublic class String {}"), 0644)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	idx.SetJdkSourceRoot(jdkDir)

	sym := "java/lang/String#"
	s := idx.resolveExternalSymbol(sym)
	if s == nil {
		t.Fatal("failed to resolve JDK symbol")
	}
	if s.Symbol != sym {
		t.Errorf("got %q, want %q", s.Symbol, sym)
	}
	
	// Verify it used the directory directly (no extraction needed).
	cachedPath, ok := idx.externalCache.Load(relPath)
	if !ok {
		t.Fatal("expected JDK path to be cached")
	}
	if cachedPath.(string) != fullPath {
		t.Errorf("got cached path %q, want %q", cachedPath, fullPath)
	}
}

func TestConcurrentExtraction(t *testing.T) {
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "lib.jar")
	createDummyJar(t, jarPath, "com/example/Concurrent.java", `package com.example;
public class Concurrent {}
`)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	idx.AddDependencySource(jarPath)

	const count = 20
	done := make(chan bool)
	sym := "com/example/Concurrent#"
	
	for i := 0; i < count; i++ {
		go func() {
			s := idx.resolveExternalSymbol(sym)
			if s == nil {
				t.Errorf("concurrent resolution failed")
			}
			done <- true
		}()
	}

	for i := 0; i < count; i++ {
		<-done
	}

	// Final verification of extraction.
	relPath := "com/example/Concurrent.java"
	cachedPath, ok := idx.externalCache.Load(relPath)
	if !ok {
		t.Fatal("expected cached result")
	}
	if _, err := os.Stat(cachedPath.(string)); err != nil {
		t.Errorf("extracted file does not exist: %v", err)
	}
}
