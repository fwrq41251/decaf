package lsp

import "testing"

func TestDocStore_OpenGetClose(t *testing.T) {
	ds := newDocStore()

	ds.Open("file:///a.java", "hello world")
	got, ok := ds.Get("file:///a.java")
	if !ok || got != "hello world" {
		t.Fatalf("Get after Open: ok=%v, got=%q", ok, got)
	}

	ds.Close("file:///a.java")
	_, ok = ds.Get("file:///a.java")
	if ok {
		t.Fatal("expected document to be removed after Close")
	}
}

func TestDocStore_ApplyFullReplace(t *testing.T) {
	ds := newDocStore()
	ds.Open("file:///a.java", "old content")

	ds.ApplyChanges("file:///a.java", []TextDocumentContentChangeEvent{
		{Range: nil, Text: "new content"},
	})

	got, _ := ds.Get("file:///a.java")
	if got != "new content" {
		t.Fatalf("got %q, want %q", got, "new content")
	}
}

func TestDocStore_ApplyIncrementalInsert(t *testing.T) {
	ds := newDocStore()
	ds.Open("file:///a.java", "hello world")

	// Insert " beautiful" between "hello" and " world"
	r := Range{
		Start: Position{Line: 0, Character: 5},
		End:   Position{Line: 0, Character: 5},
	}
	ds.ApplyChanges("file:///a.java", []TextDocumentContentChangeEvent{
		{Range: &r, Text: " beautiful"},
	})

	got, _ := ds.Get("file:///a.java")
	if got != "hello beautiful world" {
		t.Fatalf("got %q, want %q", got, "hello beautiful world")
	}
}

func TestDocStore_ApplyIncrementalDelete(t *testing.T) {
	ds := newDocStore()
	ds.Open("file:///a.java", "hello beautiful world")

	// Delete " beautiful"
	r := Range{
		Start: Position{Line: 0, Character: 5},
		End:   Position{Line: 0, Character: 15},
	}
	ds.ApplyChanges("file:///a.java", []TextDocumentContentChangeEvent{
		{Range: &r, Text: ""},
	})

	got, _ := ds.Get("file:///a.java")
	if got != "hello world" {
		t.Fatalf("got %q, want %q", got, "hello world")
	}
}

func TestDocStore_ApplyIncrementalReplace(t *testing.T) {
	ds := newDocStore()
	ds.Open("file:///a.java", "hello world")

	// Replace "world" with "Go"
	r := Range{
		Start: Position{Line: 0, Character: 6},
		End:   Position{Line: 0, Character: 11},
	}
	ds.ApplyChanges("file:///a.java", []TextDocumentContentChangeEvent{
		{Range: &r, Text: "Go"},
	})

	got, _ := ds.Get("file:///a.java")
	if got != "hello Go" {
		t.Fatalf("got %q, want %q", got, "hello Go")
	}
}

func TestDocStore_ApplyMultiLine(t *testing.T) {
	ds := newDocStore()
	ds.Open("file:///a.java", "line0\nline1\nline2\n")

	// Replace "line1" on the second line with "REPLACED"
	r := Range{
		Start: Position{Line: 1, Character: 0},
		End:   Position{Line: 1, Character: 5},
	}
	ds.ApplyChanges("file:///a.java", []TextDocumentContentChangeEvent{
		{Range: &r, Text: "REPLACED"},
	})

	got, _ := ds.Get("file:///a.java")
	want := "line0\nREPLACED\nline2\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDocStore_ApplyMultiLineSpan(t *testing.T) {
	ds := newDocStore()
	ds.Open("file:///a.java", "aaa\nbbb\nccc\nddd\n")

	// Delete lines 1-2 ("bbb\nccc\n")
	r := Range{
		Start: Position{Line: 1, Character: 0},
		End:   Position{Line: 3, Character: 0},
	}
	ds.ApplyChanges("file:///a.java", []TextDocumentContentChangeEvent{
		{Range: &r, Text: ""},
	})

	got, _ := ds.Get("file:///a.java")
	want := "aaa\nddd\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDocStore_ApplyMultipleChanges(t *testing.T) {
	ds := newDocStore()
	ds.Open("file:///a.java", "abcdef")

	// Two sequential changes in one batch (applied in order).
	// After first change: "aXXdef" (replace "bc" at 1-3 with "XX")
	// After second change: "aXXYYf" (replace "de" at 3-5 with "YY")
	ds.ApplyChanges("file:///a.java", []TextDocumentContentChangeEvent{
		{Range: &Range{Start: Position{0, 1}, End: Position{0, 3}}, Text: "XX"},
		{Range: &Range{Start: Position{0, 3}, End: Position{0, 5}}, Text: "YY"},
	})

	got, _ := ds.Get("file:///a.java")
	if got != "aXXYYf" {
		t.Fatalf("got %q, want %q", got, "aXXYYf")
	}
}

func TestDocStore_InsertNewline(t *testing.T) {
	ds := newDocStore()
	ds.Open("file:///a.java", "hello world")

	// Insert a newline after "hello"
	r := Range{
		Start: Position{Line: 0, Character: 5},
		End:   Position{Line: 0, Character: 5},
	}
	ds.ApplyChanges("file:///a.java", []TextDocumentContentChangeEvent{
		{Range: &r, Text: "\n"},
	})

	got, _ := ds.Get("file:///a.java")
	want := "hello\n world"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDocStore_UTF16(t *testing.T) {
	ds := newDocStore()
	ds.Open("file:///a.java", "你好 world") // "你好" is 2 UTF-16 units

	// Replace "你好" with "Hello"
	r := Range{
		Start: Position{Line: 0, Character: 0},
		End:   Position{Line: 0, Character: 2},
	}
	ds.ApplyChanges("file:///a.java", []TextDocumentContentChangeEvent{
		{Range: &r, Text: "Hello"},
	})

	got, _ := ds.Get("file:///a.java")
	if got != "Hello world" {
		t.Fatalf("got %q, want %q", got, "Hello world")
	}

	// Now insert emoji 🚀 (2 UTF-16 units)
	ds.Open("file:///emoji.java", "rocket ")
	r2 := Range{
		Start: Position{Line: 0, Character: 7},
		End:   Position{Line: 0, Character: 7},
	}
	ds.ApplyChanges("file:///emoji.java", []TextDocumentContentChangeEvent{
		{Range: &r2, Text: "🚀"},
	})
	got2, _ := ds.Get("file:///emoji.java")
	if got2 != "rocket 🚀" {
		t.Fatalf("got %q, want %q", got2, "rocket 🚀")
	}

	// Delete part of 🚀 (not recommended but should handle gracefully)
	// If char=8, it's the second half of surrogate pair. 
	// Our utf16Index returns the start of the pair if n falls in middle.
	r3 := Range{
		Start: Position{Line: 0, Character: 7},
		End:   Position{Line: 0, Character: 8},
	}
	ds.ApplyChanges("file:///emoji.java", []TextDocumentContentChangeEvent{
		{Range: &r3, Text: "!"},
	})
	got3, _ := ds.Get("file:///emoji.java")
	// Since both 7 and 8 point to the start of 🚀, replacing 7-8 with "!"
	// replaces the whole 🚀.
	if got3 != "rocket !" {
		t.Fatalf("got %q, want %q", got3, "rocket !")
	}
}

func TestPositionToOffset(t *testing.T) {
	tests := []struct {
		content   string
		line, col int
		want      int
	}{
		{"hello", 0, 0, 0},
		{"hello", 0, 3, 3},
		{"hello", 0, 5, 5},
		{"hello\nworld", 1, 0, 6},
		{"hello\nworld", 1, 3, 9},
		{"a\nb\nc", 2, 0, 4},
		{"a\nb\nc", 2, 1, 5},
		// Past end of line clamps to line end.
		{"hello", 0, 100, 5},
		// Past last line returns end of content.
		{"hello", 5, 0, 5},
	}

	for _, tt := range tests {
		got := PositionToByteOffset([]byte(tt.content), tt.line, tt.col)
		if got != tt.want {
			t.Errorf("PositionToByteOffset(%q, %d, %d) = %d, want %d", tt.content, tt.line, tt.col, got, tt.want)
		}
	}
}
