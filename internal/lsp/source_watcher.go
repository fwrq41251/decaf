package lsp

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/fwrq41251/decaf/internal/uri"
)

// sourceWatcher monitors .java source files for changes using fsnotify,
// acting as a server-side fallback for editors that do not send
// workspace/didChangeWatchedFiles notifications (e.g., external code agents,
// plain text editors like Notepad).
type sourceWatcher struct {
	logger     *log.Logger
	sourceRoot string
	fsw        *fsnotify.Watcher
	compile    func(uris ...string) // typically workspaceService.scheduleCompile
	clear      func(string)         // typically Handler.clearDiagnostics

	mu      sync.Mutex
	pending map[string]struct{}
	timer   *time.Timer
}

func newSourceWatcher(logger *log.Logger, sourceRoot string, compile func(uris ...string), clear func(string)) (*sourceWatcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &sourceWatcher{
		logger:     logger,
		sourceRoot: sourceRoot,
		fsw:        fsw,
		compile:    compile,
		clear:      clear,
		pending:    make(map[string]struct{}),
	}, nil
}

const sourceWatcherDebounce = 300 * time.Millisecond

// start begins watching and processing events. It blocks until ctx is cancelled.
func (sw *sourceWatcher) start(ctx context.Context) {
	sw.addDirs()
	sw.logger.Printf("source watcher started for %s", sw.sourceRoot)

	for {
		select {
		case <-ctx.Done():
			sw.mu.Lock()
			if sw.timer != nil {
				sw.timer.Stop()
			}
			sw.mu.Unlock()
			return
		case event, ok := <-sw.fsw.Events:
			if !ok {
				return
			}
			sw.handleEvent(event)

		case _, ok := <-sw.fsw.Errors:
			if !ok {
				return
			}
		}
	}
}

// flush drains pending paths and triggers compilation.
// Called from AfterFunc goroutine; all state access is mutex-protected.
func (sw *sourceWatcher) flush() {
	sw.mu.Lock()
	if len(sw.pending) == 0 {
		sw.mu.Unlock()
		return
	}
	uris := make([]string, 0, len(sw.pending))
	for p := range sw.pending {
		uris = append(uris, uri.FromPath(p))
	}
	sw.pending = make(map[string]struct{})
	sw.mu.Unlock()

	sw.logger.Printf("source watcher detected %d java file change(s)", len(uris))
	sw.compile(uris...)
}

func (sw *sourceWatcher) close() error {
	return sw.fsw.Close()
}

// addDirs recursively adds directories under sourceRoot that may contain
// .java source files.
func (sw *sourceWatcher) addDirs() {
	_ = filepath.Walk(sw.sourceRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if isSourceSkippedDir(info.Name()) {
				return filepath.SkipDir
			}
			_ = sw.fsw.Add(path)
		}
		return nil
	})
}

func (sw *sourceWatcher) addDirTree(root string) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		if isSourceSkippedDir(info.Name()) {
			return filepath.SkipDir
		}
		return sw.fsw.Add(path)
	})
}

func (sw *sourceWatcher) enqueuePath(path string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.pending[path] = struct{}{}
	if sw.timer != nil {
		sw.timer.Stop()
	}
	sw.timer = time.AfterFunc(sourceWatcherDebounce, sw.flush)
}

func (sw *sourceWatcher) enqueueJavaFilesUnder(root string) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if isSourceSkippedDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".java") {
			sw.enqueuePath(path)
		}
		return nil
	})
}

func (sw *sourceWatcher) handleEvent(event fsnotify.Event) {
	// Watch for new subdirectories so we pick up newly created packages.
	if event.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			sw.addDirTree(event.Name)
			sw.enqueueJavaFilesUnder(event.Name)
			return
		}
	}
	if !strings.HasSuffix(event.Name, ".java") {
		return
	}
	if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
		return
	}
	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 && sw.clear != nil {
		sw.clear(uri.FromPath(event.Name))
	}
	sw.enqueuePath(event.Name)
}

// isSourceSkippedDir returns true for directories that should not be watched
// for .java source changes (build outputs, VCS, IDE metadata, etc.).
func isSourceSkippedDir(name string) bool {
	switch name {
	case ".git", ".bloop", ".metals", ".bsp", ".idea", ".settings",
		"target", "build", "out", "bin", "node_modules":
		return true
	}
	return false
}
