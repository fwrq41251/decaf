package index

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"testing"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"google.golang.org/protobuf/proto"
)

func TestIndexLoadAndQuery(t *testing.T) {
	// Create a temp directory with a .semanticdb file.
	tmpDir := t.TempDir()
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	if err := os.MkdirAll(sdbDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Build a synthetic SemanticDB payload for "src/Main.java".
	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{
			{
				Schema:   sdb.Schema_SEMANTICDB4,
				Uri:      "src/Main.java",
				Language: sdb.Language_JAVA,
				Symbols: []*sdb.SymbolInformation{
					{
						Symbol:      "com/example/Main#",
						DisplayName: "Main",
						Kind:        sdb.SymbolInformation_CLASS,
						Language:    sdb.Language_JAVA,
					},
					{
						Symbol:      "com/example/Main#main().",
						DisplayName: "main",
						Kind:        sdb.SymbolInformation_METHOD,
						Language:    sdb.Language_JAVA,
					},
				},
				Occurrences: []*sdb.SymbolOccurrence{
					{
						Symbol: "com/example/Main#",
						Role:   sdb.SymbolOccurrence_DEFINITION,
						Range:  &sdb.Range{StartLine: 2, StartCharacter: 13, EndLine: 2, EndCharacter: 17},
					},
					{
						Symbol: "com/example/Main#main().",
						Role:   sdb.SymbolOccurrence_DEFINITION,
						Range:  &sdb.Range{StartLine: 3, StartCharacter: 23, EndLine: 3, EndCharacter: 27},
					},
					{
						Symbol: "com/example/Main#",
						Role:   sdb.SymbolOccurrence_REFERENCE,
						Range:  &sdb.Range{StartLine: 10, StartCharacter: 8, EndLine: 10, EndCharacter: 12},
					},
				},
			},
		},
	}

	data, err := proto.Marshal(docs)
	if err != nil {
		t.Fatal(err)
	}

	sdbFile := filepath.Join(sdbDir, "Main.java.semanticdb")
	if err := os.WriteFile(sdbFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	// Create and load index.
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	if err := idx.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Test Definition: clicking on "Main" reference at line 10, col 9
	// should resolve to the definition at line 2, col 13.
	defs := idx.Definition("file://"+tmpDir+"/src/Main.java", 10, 9)
	if len(defs) == 0 {
		t.Fatal("expected at least one definition for Main")
	}
	if defs[0].Name != "Main" {
		t.Fatalf("expected definition name 'Main', got %q", defs[0].Name)
	}
	if defs[0].Range == nil {
		t.Fatal("expected definition to have a range")
	}
	if defs[0].Range.StartLine != 2 || defs[0].Range.StartCharacter != 13 {
		t.Fatalf("expected definition at 2:13, got %d:%d",
			defs[0].Range.StartLine, defs[0].Range.StartCharacter)
	}

	// Test References: clicking on "Main" definition at line 2, col 14
	// should find the reference at line 10.
	refs := idx.References("file://"+tmpDir+"/src/Main.java", 2, 14)
	if len(refs) == 0 {
		t.Fatal("expected at least one reference for Main")
	}
	found := false
	for _, r := range refs {
		if r.Range != nil && r.Range.StartLine == 10 {
			found = true
		}
	}
	if !found {
		t.Fatal("expected to find reference at line 10")
	}

	// Test AllSymbols.
	all := idx.AllSymbols()
	if len(all) != 2 {
		t.Fatalf("expected 2 symbols, got %d", len(all))
	}
}
