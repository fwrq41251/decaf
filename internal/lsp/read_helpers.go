package lsp

import (
	"os"

	"github.com/fwrq41251/decaf/internal/uri"
)

// readContent returns file content from overlay or disk.
func readContent(fileURI string, overlay string, hasOverlay bool) []byte {
	if hasOverlay {
		return []byte(overlay)
	}
	content, err := os.ReadFile(uri.ToPath(fileURI))
	if err != nil {
		return nil
	}
	return content
}

// readContentString returns file content as string from overlay or disk.
func readContentString(fileURI string, overlay string, hasOverlay bool) string {
	if hasOverlay {
		return overlay
	}
	content, err := os.ReadFile(uri.ToPath(fileURI))
	if err != nil {
		return ""
	}
	return string(content)
}
