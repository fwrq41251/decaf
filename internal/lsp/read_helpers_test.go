package lsp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fwrq41251/decaf/internal/uri"
)

func TestReadContentStringPrefersEmptyOverlay(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "Test.java")
	if err := os.WriteFile(filePath, []byte("disk content"), 0644); err != nil {
		t.Fatal(err)
	}

	fileURI := uri.FromPath(filePath)
	got := readContentString(fileURI, "", true)
	if got != "" {
		t.Fatalf("readContentString() = %q, want empty overlay", got)
	}
}

func TestReadContentPrefersEmptyOverlay(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "Test.java")
	if err := os.WriteFile(filePath, []byte("disk content"), 0644); err != nil {
		t.Fatal(err)
	}

	fileURI := uri.FromPath(filePath)
	got := readContent(fileURI, "", true)
	if string(got) != "" {
		t.Fatalf("readContent() = %q, want empty overlay", string(got))
	}
}
