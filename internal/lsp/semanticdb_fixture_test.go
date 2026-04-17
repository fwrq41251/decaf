package lsp

import (
	"os"
	"path/filepath"
	"testing"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

func writeSourcePlaceholdersForDocs(t *testing.T, sourceRoot string, docs *sdb.TextDocuments) {
	t.Helper()

	seen := make(map[string]struct{})
	for _, doc := range docs.Documents {
		if doc == nil || doc.Uri == "" {
			continue
		}
		srcPath := filepath.Join(sourceRoot, filepath.FromSlash(doc.Uri))
		if _, ok := seen[srcPath]; ok {
			continue
		}
		seen[srcPath] = struct{}{}
		if err := os.MkdirAll(filepath.Dir(srcPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(srcPath, []byte("class Placeholder {}\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
}
