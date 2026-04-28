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

// ApplyResult describes how a call to ApplyChanges was handled, allowing the
// caller to surface protocol-violation diagnostics (e.g. didChange before
// didOpen).
type ApplyResult struct {
	// ImplicitOpen is true when the document was not previously tracked but a
	// full-text replacement in the change list was used as a recovery baseline.
	ImplicitOpen bool
	// DroppedIncremental is the number of leading incremental changes that
	// were discarded because no baseline content was available. When non-zero,
	// the editor's view of the document may diverge from the server's.
	DroppedIncremental int
}

// ApplyChanges applies a sequence of incremental content changes to a document.
// Each change has a Range (line/character based) and new text.
// If Range is nil the change replaces the entire document.
//
// When the document is not in the store (e.g. didChange arrives before
// didOpen, or after a server restart): if the change list contains a full-text
// replacement, ApplyChanges treats it as an implicit Open (using the last
// preceding incremental changes' count as DroppedIncremental). If no full-text
// replacement is present, all incremental changes are dropped and reported via
// the result so the caller can log a warning.
func (ds *docStore) ApplyChanges(uri string, changes []TextDocumentContentChangeEvent) ApplyResult {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	var res ApplyResult
	content, exists := ds.docs[uri]
	if !exists {
		// Find the last full-text replacement to use as baseline. Anything
		// before it would be discarded by the replacement anyway, so only
		// incremental changes after the last replacement matter.
		baseline := -1
		for i, ch := range changes {
			if ch.Range == nil {
				baseline = i
			}
		}
		if baseline == -1 {
			// No baseline available: every change is incremental and there is
			// nothing to apply them to.
			res.DroppedIncremental = len(changes)
			return res
		}
		// Count leading incremental changes that were discarded.
		for i := 0; i < baseline; i++ {
			if changes[i].Range != nil {
				res.DroppedIncremental++
			}
		}
		content = changes[baseline].Text
		changes = changes[baseline+1:]
		res.ImplicitOpen = true
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
	return res
}

// applyEdit replaces the text between start and end positions with newText.
func applyEdit(content string, r Range, newText string) string {
	contentBytes := []byte(content)
	startOff := PositionToByteOffset(contentBytes, r.Start.Line, r.Start.Character)
	endOff := PositionToByteOffsetEnd(contentBytes, r.End.Line, r.End.Character)

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
