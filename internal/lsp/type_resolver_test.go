package lsp

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/fwrq41251/decaf/internal/index"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"google.golang.org/protobuf/proto"
)

func setupResolverIndex(t *testing.T, syms []*sdb.SymbolInformation) *index.Index {
	t.Helper()

	tmpDir := t.TempDir()
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	os.MkdirAll(sdbDir, 0755)

	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{
			{
				Uri:     "src/Test.java",
				Symbols: syms,
			},
		},
	}
	data, err := proto.Marshal(docs)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sdbDir, "Test.java.semanticdb"), data, 0644)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := index.NewIndex(logger, tmpDir)
	if err := idx.Load(); err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestTypeResolver_ExplicitImport(t *testing.T) {
	idx := setupResolverIndex(t, []*sdb.SymbolInformation{
		{Symbol: "java/util/List#", DisplayName: "List", Kind: sdb.SymbolInformation_INTERFACE},
	})
	r := &typeResolver{
		idx: idx,
		imports: []ImportSpec{
			{Path: "java.util.List"},
		},
	}
	got := r.resolve("List")
	if got != "java/util/List#" {
		t.Fatalf("expected 'java/util/List#', got %q", got)
	}
}

func TestTypeResolver_JavaLang(t *testing.T) {
	idx := setupResolverIndex(t, []*sdb.SymbolInformation{
		{Symbol: "java/lang/String#", DisplayName: "String", Kind: sdb.SymbolInformation_CLASS},
	})
	r := &typeResolver{
		idx:     idx,
		imports: nil,
	}
	got := r.resolve("String")
	if got != "java/lang/String#" {
		t.Fatalf("expected 'java/lang/String#', got %q", got)
	}
}

func TestTypeResolver_WildcardImport(t *testing.T) {
	idx := setupResolverIndex(t, []*sdb.SymbolInformation{
		{Symbol: "java/util/ArrayList#", DisplayName: "ArrayList", Kind: sdb.SymbolInformation_CLASS},
	})
	r := &typeResolver{
		idx: idx,
		imports: []ImportSpec{
			{Path: "java.util.*", Wildcard: true},
		},
	}
	got := r.resolve("ArrayList")
	if got != "java/util/ArrayList#" {
		t.Fatalf("expected 'java/util/ArrayList#', got %q", got)
	}
}

func TestTypeResolver_SamePackage(t *testing.T) {
	idx := setupResolverIndex(t, []*sdb.SymbolInformation{
		{Symbol: "com/example/Foo#", DisplayName: "Foo", Kind: sdb.SymbolInformation_CLASS},
	})
	r := &typeResolver{
		idx:     idx,
		imports: nil,
		pkg:     "com.example",
	}
	got := r.resolve("Foo")
	if got != "com/example/Foo#" {
		t.Fatalf("expected 'com/example/Foo#', got %q", got)
	}
}

func TestTypeResolver_Primitive(t *testing.T) {
	idx := setupResolverIndex(t, nil)
	r := &typeResolver{idx: idx}
	got := r.resolve("int")
	if got != "" {
		t.Fatalf("expected empty for primitive, got %q", got)
	}
}

func TestTypeResolver_Unknown(t *testing.T) {
	idx := setupResolverIndex(t, nil)
	r := &typeResolver{idx: idx}
	got := r.resolve("NonExistent")
	if got != "" {
		t.Fatalf("expected empty for unknown type, got %q", got)
	}
}

func TestTypeResolver_InnerClassExplicitImport(t *testing.T) {
	idx := setupResolverIndex(t, []*sdb.SymbolInformation{
		{Symbol: "com/example/Outer$Inner#", DisplayName: "Inner", Kind: sdb.SymbolInformation_CLASS},
	})
	r := &typeResolver{
		idx: idx,
		imports: []ImportSpec{
			{Path: "com.example.Outer.Inner"},
		},
	}
	got := r.resolve("Inner")
	if got != "com/example/Outer$Inner#" {
		t.Fatalf("expected 'com/example/Outer$Inner#', got %q", got)
	}
}

func TestTypeResolver_InnerClassFQN(t *testing.T) {
	idx := setupResolverIndex(t, []*sdb.SymbolInformation{
		{Symbol: "com/example/Outer$Inner#", DisplayName: "Inner", Kind: sdb.SymbolInformation_CLASS},
	})
	r := &typeResolver{idx: idx}
	got := r.resolve("com.example.Outer.Inner")
	if got != "com/example/Outer$Inner#" {
		t.Fatalf("expected 'com/example/Outer$Inner#', got %q", got)
	}
}

func TestTypeResolver_InnerClassSamePackage(t *testing.T) {
	idx := setupResolverIndex(t, []*sdb.SymbolInformation{
		{Symbol: "com/example/Outer$Inner#", DisplayName: "Inner", Kind: sdb.SymbolInformation_CLASS},
	})
	r := &typeResolver{
		idx: idx,
		pkg: "com.example",
	}
	// "Inner" alone won't match same-package "com/example/Inner#", but
	// the global fallback (step 5) should find it as the only match.
	got := r.resolve("Inner")
	if got != "com/example/Outer$Inner#" {
		t.Fatalf("expected 'com/example/Outer$Inner#', got %q", got)
	}
}

func TestTypeResolver_InnerClassWildcardImport(t *testing.T) {
	idx := setupResolverIndex(t, []*sdb.SymbolInformation{
		{Symbol: "com/example/Outer$Inner#", DisplayName: "Inner", Kind: sdb.SymbolInformation_CLASS},
	})
	r := &typeResolver{
		idx: idx,
		imports: []ImportSpec{
			{Path: "com.example.Outer.*", Wildcard: true},
		},
	}
	got := r.resolve("Inner")
	if got != "com/example/Outer$Inner#" {
		t.Fatalf("expected 'com/example/Outer$Inner#', got %q", got)
	}
}

func TestFqnToSymbolVariants(t *testing.T) {
	variants := fqnToSymbolVariants("com.example.Outer.Inner")
	expected := []string{
		"com/example/Outer/Inner#",
		"com/example/Outer$Inner#",
		"com/example$Outer$Inner#",
		"com$example$Outer$Inner#",
	}
	if len(variants) != len(expected) {
		t.Fatalf("expected %d variants, got %d: %v", len(expected), len(variants), variants)
	}
	for i, v := range variants {
		if v != expected[i] {
			t.Fatalf("variant[%d]: expected %q, got %q", i, expected[i], v)
		}
	}
}
