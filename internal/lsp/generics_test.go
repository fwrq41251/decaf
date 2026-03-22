package lsp

import (
	"testing"

	"github.com/fwrq41251/decaf/internal/index"
)

func TestSplitGenericName(t *testing.T) {
	tests := []struct {
		input    string
		wantBase string
		wantArgs []string
	}{
		{"List", "List", nil},
		{"List<String>", "List", []string{"String"}},
		{"Map<String, Integer>", "Map", []string{"String", "Integer"}},
		{"Map<String, List<Integer>>", "Map", []string{"String", "List<Integer>"}},
		{"int", "int", nil},
	}

	for _, tt := range tests {
		base, args := splitGenericName(tt.input)
		if base != tt.wantBase {
			t.Errorf("splitGenericName(%q) base = %q, want %q", tt.input, base, tt.wantBase)
		}
		if len(args) != len(tt.wantArgs) {
			t.Errorf("splitGenericName(%q) args = %v, want %v", tt.input, args, tt.wantArgs)
			continue
		}
		for i, a := range args {
			if a != tt.wantArgs[i] {
				t.Errorf("splitGenericName(%q) args[%d] = %q, want %q", tt.input, i, a, tt.wantArgs[i])
			}
		}
	}
}

func TestSubstituteTypeParams(t *testing.T) {
	// Simulate: List<String>.get() → returns E → substitutes to String
	idx := setupGenericIndex(t)

	owner := &index.TypeExpr{
		Sym:  "java/util/List#",
		Args: []*index.TypeExpr{{Sym: "java/lang/String#"}},
	}

	// get() returns E, which is "java/util/List#[E]"
	retType := &index.TypeExpr{Sym: "java/util/List#[E]"}

	result := substituteTypeParams(retType, owner, idx)
	if result == nil || result.Sym != "java/lang/String#" {
		t.Errorf("expected String#, got %v", result)
	}
}

func TestSubstituteTypeParams_Nested(t *testing.T) {
	// Simulate: Map<String, Integer>.entrySet() → returns Set<Entry<K,V>>
	// → substituted to Set<Entry<String, Integer>>
	idx := setupGenericIndex(t)

	owner := &index.TypeExpr{
		Sym: "java/util/Map#",
		Args: []*index.TypeExpr{
			{Sym: "java/lang/String#"},
			{Sym: "java/lang/Integer#"},
		},
	}

	// Return type: Set<Entry<K, V>> where K and V are Map's type params
	retType := &index.TypeExpr{
		Sym: "java/util/Set#",
		Args: []*index.TypeExpr{
			{
				Sym: "java/util/Map.Entry#",
				Args: []*index.TypeExpr{
					{Sym: "java/util/Map#[K]"},
					{Sym: "java/util/Map#[V]"},
				},
			},
		},
	}

	result := substituteTypeParams(retType, owner, idx)
	if result == nil || result.Sym != "java/util/Set#" {
		t.Fatalf("expected Set#, got %v", result)
	}
	if len(result.Args) != 1 {
		t.Fatalf("expected 1 arg, got %d", len(result.Args))
	}
	entry := result.Args[0]
	if entry.Sym != "java/util/Map.Entry#" {
		t.Fatalf("expected Map.Entry#, got %s", entry.Sym)
	}
	if len(entry.Args) != 2 {
		t.Fatalf("expected 2 args, got %d", len(entry.Args))
	}
	if entry.Args[0].Sym != "java/lang/String#" {
		t.Errorf("K not substituted: got %s", entry.Args[0].Sym)
	}
	if entry.Args[1].Sym != "java/lang/Integer#" {
		t.Errorf("V not substituted: got %s", entry.Args[1].Sym)
	}
}

func TestSubstituteTypeParams_NoArgs(t *testing.T) {
	// If owner has no type args, substitution is a no-op.
	idx := setupGenericIndex(t)

	owner := &index.TypeExpr{Sym: "java/util/List#"}
	retType := &index.TypeExpr{Sym: "java/util/List#[E]"}

	result := substituteTypeParams(retType, owner, idx)
	if result.Sym != "java/util/List#[E]" {
		t.Errorf("expected no substitution, got %s", result.Sym)
	}
}

func TestResolveParameterized(t *testing.T) {
	idx := setupGenericIndex(t)
	resolver := &typeResolver{
		idx:     idx,
		imports: []ImportSpec{{Path: "java.util.List"}, {Path: "java.lang.String"}},
	}

	te := resolver.resolveParameterized("List<String>")
	if te == nil {
		t.Fatal("resolveParameterized returned nil")
	}
	if te.Sym != "java/util/List#" {
		t.Errorf("sym = %q, want java/util/List#", te.Sym)
	}
	if len(te.Args) != 1 || te.Args[0].Sym != "java/lang/String#" {
		t.Errorf("args = %v, want [{java/lang/String#}]", te.Args)
	}
}

// setupGenericIndex creates a minimal index with List<E> and Map<K,V> type params.
func setupGenericIndex(t *testing.T) *index.Index {
	t.Helper()
	idx := index.NewIndex(nil, t.TempDir())
	t.Cleanup(func() { idx.Close() })

	// We need to populate classTypeParams directly.
	// Use the test helper to set up the index state.
	idx.SetClassTypeParamsForTest("java/util/List#", []string{"java/util/List#[E]"})
	idx.SetClassTypeParamsForTest("java/util/Map#", []string{"java/util/Map#[K]", "java/util/Map#[V]"})

	// Add type definitions so resolver can find them.
	idx.AddDefinitionForTest("java/util/List#", "List")
	idx.AddDefinitionForTest("java/lang/String#", "String")
	idx.AddDefinitionForTest("java/lang/Integer#", "Integer")
	idx.AddDefinitionForTest("java/util/Map#", "Map")

	return idx
}
