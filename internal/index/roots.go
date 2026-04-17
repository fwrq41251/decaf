package index

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

type bloopRootConfig struct {
	Project struct {
		Directory  string `json:"directory"`
		ClassesDir string `json:"classesDir"`
	} `json:"project"`
}

// discoverScanAndWatchRoots narrows semanticdb scanning to known Bloop output
// directories when possible. It falls back to the full workspace for projects
// without .bloop metadata.
func (idx *Index) discoverScanAndWatchRoots() (scanRoots, watchRoots []string) {
	bloopDir := filepath.Join(idx.sourceRoot, ".bloop")
	entries, err := os.ReadDir(bloopDir)
	if err != nil {
		return []string{idx.sourceRoot}, []string{idx.sourceRoot}
	}

	scanSet := make(map[string]struct{})
	watchSet := make(map[string]struct{})

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		configPath := filepath.Join(bloopDir, entry.Name())
		data, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}

		var cfg bloopRootConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}

		classesDir := resolveBloopPath(idx.sourceRoot, cfg.Project.Directory, cfg.Project.ClassesDir)
		if classesDir == "" {
			continue
		}

		scanSet[filepath.Join(classesDir, "META-INF", "semanticdb")] = struct{}{}
		watchSet[classesDir] = struct{}{}
	}

	if len(scanSet) == 0 {
		return []string{idx.sourceRoot}, []string{idx.sourceRoot}
	}

	return sortedKeys(scanSet), sortedKeys(watchSet)
}

func resolveBloopPath(workspaceRoot, projectDir, p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if projectDir != "" {
		if filepath.IsAbs(projectDir) {
			return filepath.Clean(filepath.Join(projectDir, p))
		}
		return filepath.Clean(filepath.Join(workspaceRoot, projectDir, p))
	}
	return filepath.Clean(filepath.Join(workspaceRoot, p))
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sameRoots(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
