package lsp

import (
	"bytes"
)

// PositionToByteOffset converts a 0-indexed (line, character) position to a byte offset
// in the given content, correctly handling UTF-16 code units for the character offset.
func PositionToByteOffset(content []byte, line, character int) int {
	return positionToByteOffsetImpl(content, line, character, false)
}

// PositionToByteOffsetEnd is like PositionToByteOffset but returns the end of the character
// if the position falls in the middle of a multi-byte UTF-8 sequence.
func PositionToByteOffsetEnd(content []byte, line, character int) int {
	return positionToByteOffsetImpl(content, line, character, true)
}

func positionToByteOffsetImpl(content []byte, line, character int, end bool) int {
	cur := 0
	for l := 0; l < line; l++ {
		idx := bytes.IndexByte(content[cur:], '\n')
		if idx < 0 {
			return len(content)
		}
		cur += idx + 1
	}

	// Find the end of the current line to isolate it for UTF-16 counting.
	lineStart := cur
	lineEnd := bytes.IndexByte(content[lineStart:], '\n')
	if lineEnd < 0 {
		lineEnd = len(content)
	} else {
		lineEnd += lineStart
	}

	lineText := string(content[lineStart:lineEnd])
	var byteOffInLine int
	if end {
		byteOffInLine = utf16IndexEnd(lineText, character)
	} else {
		byteOffInLine = utf16Index(lineText, character)
	}

	return lineStart + byteOffInLine
}

// utf16Index returns the byte index of the n-th UTF-16 code unit in s.
// If n falls in the middle of a multi-unit character, it returns the start of that character.
// If n is out of range, it returns the length of s.
func utf16Index(s string, n int) int {
	return utf16IndexImpl(s, n, false)
}

// utf16IndexEnd returns the byte index of the n-th UTF-16 code unit in s.
// If n falls in the middle of a multi-unit character, it returns the end of that character.
// If n is out of range, it returns the length of s.
func utf16IndexEnd(s string, n int) int {
	return utf16IndexImpl(s, n, true)
}

func utf16IndexImpl(s string, n int, end bool) int {
	if n <= 0 {
		return 0
	}
	
	cur16 := 0
	for i, r := range s {
		l := utf16Len(r)
		if cur16+l > n {
			if end {
				return i + len(string(r))
			}
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
