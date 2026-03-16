package lsp

// utf16Index returns the byte index of the n-th UTF-16 code unit in s.
// If n falls in the middle of a surrogate pair, it returns the start of that pair.
// If n is out of range, it returns the length of s.
func utf16Index(s string, n int) int {
	if n <= 0 {
		return 0
	}
	
	cur16 := 0
	for i, r := range s {
		l := utf16Len(r)
		if cur16+l > n {
			return i
		}
		cur16 += l
		if cur16 == n {
			// Found exact boundary.
			return i + len(string(r))
		}
	}
	return len(s)
}

// utf16Len returns the number of UTF-16 code units for a rune.
func utf16Len(r rune) int {
	if r <= 0xFFFF {
		return 1
	}
	return 2
}

// byteToUTF16Offset converts a byte offset to a UTF-16 code unit offset.
func byteToUTF16Offset(s string, byteOff int) int {
	if byteOff <= 0 {
		return 0
	}
	if byteOff >= len(s) {
		byteOff = len(s)
	}
	
	cur16 := 0
	for i, r := range s {
		if i >= byteOff {
			break
		}
		cur16 += utf16Len(r)
	}
	return cur16
}
