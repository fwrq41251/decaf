package index

import (
	"bytes"
	"log"
	"path/filepath"
	"testing"
)

func TestExternalSymbolResolution_EdgeCases(t *testing.T) {
	tmpDir := t.TempDir()
	
	// Create a JAR with nested classes and overloaded methods.
	jarPath := filepath.Join(tmpDir, "edge.jar")
	createDummyJar(t, jarPath, "com/example/Outer.java", `package com.example;
public class Outer {
    public void method() {}
    public void method(String s) {}
    
    public static class Inner {
        public void innerMethod() {}
    }
}
`)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	idx.AddDependencySource(jarPath)

	tests := []struct {
		name     string
		sym      string
		wantLine int
	}{
		{
			"Simple method",
			"com/example/Outer#method().",
			2, // 0-indexed line 3
		},
		{
			"Overloaded method (should probably fail to distinguish but check current behavior)",
			"com/example/Outer#method(+1).", // SemanticDB format for overloads
			2, // Currently it will always find the first one: line 3
		},
		{
			"Inner class",
			"com/example/Outer#Inner#",
			5, // 0-indexed line 6
		},
		{
			"Inner class method",
			"com/example/Outer#Inner#innerMethod().",
			6, // 0-indexed line 7
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := idx.resolveExternalSymbol(tt.sym)
			if s == nil {
				t.Errorf("failed to resolve %s", tt.sym)
				return
			}
			if s.Range == nil {
				t.Errorf("no range for %s", tt.sym)
				return
			}
			if int(s.Range.StartLine) != tt.wantLine {
				t.Errorf("%s: got line %d, want %d", tt.sym, s.Range.StartLine, tt.wantLine)
			}
		})
	}
}

func TestExternalSymbolResolution_DotsInSymbol(t *testing.T) {
	tmpDir := t.TempDir()
	jarPath := filepath.Join(tmpDir, "dots.jar")
	createDummyJar(t, jarPath, "com/example/Outer.java", `package com.example;
public class Outer {
    public static class Inner {}
}
`)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	idx.AddDependencySource(jarPath)

	// Simulate a symbol that uses dots instead of hashes for nesting (happens in some SDB versions/tools)
	sym := "com/example/Outer.Inner#"
	s := idx.resolveExternalSymbol(sym)
	if s == nil {
		// This is expected to fail with current implementation because it looks for com/example/Outer.Inner.java
		t.Logf("Current implementation failed to resolve symbol with dots as expected: %s", sym)
	} else {
		t.Logf("Successfully resolved (surprisingly): %s -> %s", sym, s.URI)
	}
}
