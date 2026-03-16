package lsp

import (
	"strings"
	"sync"
)

// docStore is a thread-safe in-memory store for open document contents.
// It acts as an overlay on top of the filesystem, keeping the latest
// buffer content as reported by the editor via didOpen/didChange/didClose.
type docStore struct {
	mu   sync.RWMutex
	docs map[string]string // URI -> full text content
}

func newDocStore() *docStore {
	return &docStore{docs: make(map[string]string)}
}

// Open stores the initial content of a document.
func (ds *docStore) Open(uri, text string) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	ds.docs[uri] = text
}

// Close removes a document from the store.
func (ds *docStore) Close(uri string) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	delete(ds.docs, uri)
}

// Get returns the content for a URI and whether it exists in the store.
func (ds *docStore) Get(uri string) (string, bool) {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	text, ok := ds.docs[uri]
	return text, ok
}

// ApplyChanges applies a sequence of incremental content changes to a document.
// Each change has a Range (line/character based) and new text.
// If Range is nil the change replaces the entire document.
func (ds *docStore) ApplyChanges(uri string, changes []TextDocumentContentChangeEvent) {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	content, ok := ds.docs[uri]
	if !ok {
		return
	}

	for _, ch := range changes {
		if ch.Range == nil {
			// Full document replacement.
			content = ch.Text
		} else {
			content = applyEdit(content, *ch.Range, ch.Text)
		}
	}

	ds.docs[uri] = content
}

// applyEdit replaces the text between start and end positions with newText.
func applyEdit(content string, r Range, newText string) string {
	startOff := positionToOffset(content, r.Start.Line, r.Start.Character, false)
	endOff := positionToOffset(content, r.End.Line, r.End.Character, true)

	if startOff < 0 {
		startOff = 0
	}
	if endOff < 0 || endOff > len(content) {
		endOff = len(content)
	}
	if startOff > endOff {
		startOff = endOff
	}

	var sb strings.Builder
	sb.Grow(startOff + len(newText) + len(content) - endOff)
	sb.WriteString(content[:startOff])
	sb.WriteString(newText)
	sb.WriteString(content[endOff:])
	return sb.String()
}

// positionToOffset converts a 0-based line/character position to a byte offset.
func positionToOffset(content string, line, character int, end bool) int {
	cur := 0
	for l := 0; l < line; l++ {
		idx := strings.IndexByte(content[cur:], '\n')
		if idx < 0 {
			return len(content)
		}
		cur += idx + 1
	}

	// character is a 0-based UTF-16 code unit offset from the start of the line.
	lineStart := cur
	lineEnd := strings.IndexByte(content[lineStart:], '\n')
	if lineEnd < 0 {
		lineEnd = len(content)
	} else {
		lineEnd += lineStart
	}

	lineText := content[lineStart:lineEnd]
	var byteOffInLine int
	if end {
		byteOffInLine = utf16IndexEnd(lineText, character)
	} else {
		byteOffInLine = utf16Index(lineText, character)
	}
	
	return lineStart + byteOffInLine
}
