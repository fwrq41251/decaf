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
						if !isSkippedDir(info.Name(), event.Name) {
							_ = w.fsw.Add(event.Name)
						}
					}
				}
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

// watchDirs adds all directories under root that might contain .semanticdb files.
func (w *watcher) watchDirs(root string) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if isSkippedDir(info.Name(), path) {
				return filepath.SkipDir
			}
			_ = w.fsw.Add(path)
		}
		return nil
	})
}

func (w *watcher) close() error {
	return w.fsw.Close()
}

func isSkippedDir(name, path string) bool {
	return name == ".git" || name == ".metals" || name == ".bloop" ||
		strings.Contains(path, "bloop-internal-classes") ||
		strings.Contains(path, "bloop-bsp-clients-classes")
}
