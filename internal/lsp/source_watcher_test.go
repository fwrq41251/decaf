package lsp

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/fwrq41251/decaf/internal/uri"
)

func TestSourceWatcher_RecursivelyWatchesNewDirectories(t *testing.T) {
	rootDir := t.TempDir()
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	sw, err := newSourceWatcher(logger, rootDir, func(...string) {}, nil)
	if err != nil {
		t.Fatalf("newSourceWatcher: %v", err)
	}
	defer sw.close()
	sw.addDirs()

	pkgDir := filepath.Join(rootDir, "src", "main", "java", "com", "example")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	sw.handleEvent(fsnotify.Event{Name: filepath.Join(rootDir, "src"), Op: fsnotify.Create})

	watched := make(map[string]struct{})
	for _, path := range sw.fsw.WatchList() {
		watched[path] = struct{}{}
	}
	for _, want := range []string{
		filepath.Join(rootDir, "src"),
		filepath.Join(rootDir, "src", "main"),
		filepath.Join(rootDir, "src", "main", "java"),
		filepath.Join(rootDir, "src", "main", "java", "com"),
		filepath.Join(rootDir, "src", "main", "java", "com", "example"),
	} {
		if _, ok := watched[want]; !ok {
			t.Fatalf("watch list missing %q; watched=%v", want, sw.fsw.WatchList())
		}
	}
}

func TestSourceWatcher_CompilesExistingJavaFilesInNewDirectoryTree(t *testing.T) {
	rootDir := t.TempDir()
	var (
		mu       sync.Mutex
		compiled []string
	)
	sw, err := newSourceWatcher(log.New(&bytes.Buffer{}, "[test] ", 0), rootDir, func(uris ...string) {
		mu.Lock()
		compiled = append(compiled, uris...)
		mu.Unlock()
	}, nil)
	if err != nil {
		t.Fatalf("newSourceWatcher: %v", err)
	}
	defer sw.close()

	pkgDir := filepath.Join(rootDir, "src", "main", "java", "com", "example")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	javaPath := filepath.Join(pkgDir, "Main.java")
	if err := os.WriteFile(javaPath, []byte("class Main {}\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	sw.handleEvent(fsnotify.Event{Name: filepath.Join(rootDir, "src"), Op: fsnotify.Create})

	waitForCondition(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, got := range compiled {
			if got == uri.FromPath(javaPath) {
				return true
			}
		}
		return false
	}, "expected existing java files in new directory tree to trigger compile")
}

func TestSourceWatcher_ClearsDiagnosticsOnDelete(t *testing.T) {
	var (
		mu      sync.Mutex
		cleared []string
	)
	sw, err := newSourceWatcher(log.New(&bytes.Buffer{}, "[test] ", 0), t.TempDir(), func(...string) {}, func(uri string) {
		mu.Lock()
		cleared = append(cleared, uri)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("newSourceWatcher: %v", err)
	}
	defer sw.close()

	javaPath := filepath.Join(sw.sourceRoot, "src", "Main.java")
	sw.handleEvent(fsnotify.Event{Name: javaPath, Op: fsnotify.Remove})
	want := uri.FromPath(javaPath)
	mu.Lock()
	defer mu.Unlock()
	if len(cleared) != 1 || cleared[0] != want {
		t.Fatalf("cleared = %v, want [%q]", cleared, want)
	}
}
