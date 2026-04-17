package index

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// watcher monitors directories for .semanticdb file changes and maintains
// a set of dirty (changed/created) and deleted file paths.
type watcher struct {
	fsw     *fsnotify.Watcher
	mu      sync.Mutex
	dirty   map[string]struct{} // paths that were created or modified
	removed map[string]struct{} // paths that were deleted
	roots   []string
	idx     *Index
}

func newWatcher(idx *Index) (*watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &watcher{
		fsw:     fsw,
		dirty:   make(map[string]struct{}),
		removed: make(map[string]struct{}),
		idx:     idx,
	}
	go w.loop()
	return w, nil
}

func (w *watcher) loop() {
	for {
		select {
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(event.Name, ".semanticdb") {
				// Watch for new subdirectories to cover new build outputs.
				if event.Op&fsnotify.Create != 0 {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						if !isSkippedDir(info.Name(), event.Name) && w.shouldWatchDir(event.Name) {
							_ = w.fsw.Add(event.Name)
						}
					}
				}
				continue
			}
			if !w.shouldTrackFile(event.Name) {
				continue
			}

			w.mu.Lock()
			switch {
			case event.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
				delete(w.dirty, event.Name)
				w.removed[event.Name] = struct{}{}
			case event.Op&(fsnotify.Create|fsnotify.Write) != 0:
				delete(w.removed, event.Name)
				w.dirty[event.Name] = struct{}{}
			}
			w.mu.Unlock()

		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
		}
	}
}

// drain returns the current dirty and removed sets and resets them.
func (w *watcher) drain() (dirty, removed []string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for p := range w.dirty {
		dirty = append(dirty, p)
	}
	for p := range w.removed {
		removed = append(removed, p)
	}

	w.dirty = make(map[string]struct{})
	w.removed = make(map[string]struct{})
	return
}

// hasPending returns true if there are any pending changes.
func (w *watcher) hasPending() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.dirty) > 0 || len(w.removed) > 0
}

// watchRoots adds all directories under the provided roots that might contain
// .semanticdb files. Missing roots are replaced with their nearest existing
// ancestor so newly-created classes/META-INF directories are still observed.
func (w *watcher) watchRoots(roots []string) {
	w.setRoots(roots)

	seen := make(map[string]struct{})
	for _, root := range roots {
		start := nearestExistingDir(root)
		if start == "" {
			continue
		}
		if _, ok := seen[start]; ok {
			continue
		}
		seen[start] = struct{}{}
		_ = filepath.Walk(start, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if isSkippedDir(info.Name(), path) {
					return filepath.SkipDir
				}
				if !w.shouldWatchDir(path) {
					return filepath.SkipDir
				}
				_ = w.fsw.Add(path)
			}
			return nil
		})
	}
}

func (w *watcher) setRoots(roots []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.roots = make([]string, 0, len(roots))
	for _, root := range roots {
		w.roots = append(w.roots, filepath.Clean(root))
	}
}

func (w *watcher) shouldTrackFile(path string) bool {
	w.mu.Lock()
	roots := append([]string(nil), w.roots...)
	w.mu.Unlock()
	return pathWithinAnyRoot(path, roots)
}

func (w *watcher) shouldWatchDir(path string) bool {
	w.mu.Lock()
	roots := append([]string(nil), w.roots...)
	w.mu.Unlock()
	return pathRelevantToRoots(path, roots)
}

func nearestExistingDir(path string) string {
	path = filepath.Clean(path)
	for {
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return ""
		}
		path = parent
	}
}

func (w *watcher) close() error {
	return w.fsw.Close()
}

func isSkippedDir(name, path string) bool {
	return name == ".git" || name == ".metals" || name == ".bloop" ||
		strings.Contains(path, "bloop-internal-classes") ||
		strings.Contains(path, "bloop-bsp-clients-classes")
}

func pathRelevantToRoots(path string, roots []string) bool {
	for _, root := range roots {
		if pathWithinRoot(path, root) || pathWithinRoot(root, path) {
			return true
		}
	}
	return false
}

func pathWithinAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		if pathWithinRoot(path, root) {
			return true
		}
	}
	return false
}

func pathWithinRoot(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}
