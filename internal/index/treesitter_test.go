package index

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractShortName(t *testing.T) {
	tests := []struct {
		sym      string
		expected string
	}{
		{"java/lang/String#", "String"},
		{"java/lang/String#length().", "length"},
		{"org/slf4j/LoggerFactory#getLogger(+1).", "getLogger"},
		{"com/example/Main#main([Ljava/lang/String;)V.", "main"},
		{"com/example/User#name:", "name"},
		{"com/example/package/ClassName#", "ClassName"},
		{"plainIdentifier", "plainIdentifier"},
		{"com/example/Test#`<init>`().", "Test"},
		{"com/example/Outer#Inner#", "Inner"},
		{"com/example/Outer#Inner#`<init>`().", "Inner"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.sym, func(t *testing.T) {
			got := ExtractShortName(tt.sym)
			if got != tt.expected {
				t.Errorf("ExtractShortName(%q) = %q, want %q", tt.sym, got, tt.expected)
			}
		})
	}
}

func TestFindSymbolLocation(t *testing.T) {
	tmpDir := t.TempDir()
	javaFile := filepath.Join(tmpDir, "Test.java")
	content := `package com.example;

public class Test {
    private String name;

    public Test(String name) {
        this.name = name;
    }

    public String getName() {
        return name;
    }

    public static class Inner {
        public Inner() {}
        public void doWork() {}
    }
}
`
	if err := os.WriteFile(javaFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		sym      string
		expectedRow int
		expectedCol int
	}{
		{"com/example/Test#", 2, 13},
		{"com/example/Test#name:", 3, 19},
		{"com/example/Test#getName().", 9, 18},
		// Constructor - name is "Test"
		{"com/example/Test#`<init>`().", 5, 11},
		// Inner class
		{"com/example/Test#Inner#", 13, 24},
		// Inner class constructor
		{"com/example/Test#Inner#`<init>`().", 14, 15},
		{"com/example/Test#Inner#doWork().", 15, 20},
	}

	for _, tt := range tests {
		t.Run(tt.sym, func(t *testing.T) {
			row, col := FindSymbolLocation(javaFile, tt.sym)
			if row != tt.expectedRow || col != tt.expectedCol {
				t.Errorf("FindSymbolLocation(%q) = %d:%d, want %d:%d", 
					tt.sym, row, col, tt.expectedRow, tt.expectedCol)
			}
		})
	}
}
