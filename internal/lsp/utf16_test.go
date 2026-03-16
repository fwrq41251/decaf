package lsp

import "testing"

func TestUTF16Index(t *testing.T) {
	tests := []struct {
		s    string
		n    int
		want int
	}{
		{"abc", 0, 0},
		{"abc", 1, 1},
		{"abc", 3, 3},
		{"abc", 4, 3},
		{"你好", 1, 3}, // "你" is 3 bytes in UTF-8
		{"你好", 2, 6}, // "好" is 3 bytes in UTF-8
		{"𐐷a", 1, 0},   // 𐐷 is 2 code units, but utf16Index returns byte offset where n-th unit starts? 
		                // Wait, if n=1, it should return the byte offset of the 1st code unit (which is 0).
		                // If n=2, it should return the byte offset of the 2nd code unit (which is still part of 𐐷?).
		                // Actually, my utf16Index:
		                // for i, r := range s { if cur16 >= n { return i } cur16 += utf16Len(r) }
		                // If n=2 and r is 𐐷 (2 units), cur16 becomes 2. Next iteration returns i.
	}

	for _, tt := range tests {
		got := utf16Index(tt.s, tt.n)
		if got != tt.want {
			t.Errorf("utf16Index(%q, %d) = %d, want %d", tt.s, tt.n, got, tt.want)
		}
	}
}

func TestUTF16PositionToOffset(t *testing.T) {
	content := "a\n你好\n𐐷b"
	// Line 0: "a" (1 unit)
	// Line 1: "你好" (2 units: 你, 好)
	// Line 2: "𐐷b" (3 units: 𐐷 is 2 units, b is 1 unit)
	
	tests := []struct {
		line, char int
		want       int
	}{
		{0, 0, 0},
		{0, 1, 1},
		{1, 0, 2},
		{1, 1, 5}, // after "你" (3 bytes)
		{1, 2, 8}, // after "好" (3 bytes)
		{2, 0, 9},
		{2, 2, 13}, // after 𐐷 (4 bytes)
		{2, 3, 14}, // after "b" (1 byte)
	}

	for _, tt := range tests {
		got := positionToOffset(content, tt.line, tt.char)
		if got != tt.want {
			t.Errorf("positionToOffset(%d, %d) = %d, want %d", tt.line, tt.char, got, tt.want)
		}
	}
}
