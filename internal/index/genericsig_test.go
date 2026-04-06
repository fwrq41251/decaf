package index

import "testing"

func TestParseClassGenericSig(t *testing.T) {
	tests := []struct {
		name        string
		sig         string
		classSym    string
		wantParams  []string
		wantParents []struct {
			sym      string
			firstArg string // Sym of the first type arg, or "" if no args
		}
	}{
		{
			name:       "List<E>",
			sig:        "<E:Ljava/lang/Object;>Ljava/lang/Object;Ljava/util/Collection<TE;>;",
			classSym:   "java/util/List#",
			wantParams: []string{"E"},
			wantParents: []struct{ sym, firstArg string }{
				{"java/lang/Object#", ""},
				{"java/util/Collection#", "java/util/List#[E]"},
			},
		},
		{
			name:       "Map<K,V>",
			sig:        "<K:Ljava/lang/Object;V:Ljava/lang/Object;>Ljava/lang/Object;",
			classSym:   "java/util/Map#",
			wantParams: []string{"K", "V"},
			wantParents: []struct{ sym, firstArg string }{
				{"java/lang/Object#", ""},
			},
		},
		{
			name:       "ArrayList<E> extends AbstractList<E>",
			sig:        "<E:Ljava/lang/Object;>Ljava/util/AbstractList<TE;>;Ljava/util/List<TE;>;",
			classSym:   "java/util/ArrayList#",
			wantParams: []string{"E"},
			wantParents: []struct{ sym, firstArg string }{
				{"java/util/AbstractList#", "java/util/ArrayList#[E]"},
				{"java/util/List#", "java/util/ArrayList#[E]"},
			},
		},
		{
			name:       "non-generic class",
			sig:        "Ljava/lang/Object;Ljava/io/Serializable;",
			classSym:   "com/example/Foo#",
			wantParams: nil,
			wantParents: []struct{ sym, firstArg string }{
				{"java/lang/Object#", ""},
				{"java/io/Serializable#", ""},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := parseClassGenericSig(tt.sig, tt.classSym)
			if info == nil {
				t.Fatal("parseClassGenericSig returned nil")
			}

			// Check type params.
			if len(info.typeParams) != len(tt.wantParams) {
				t.Fatalf("typeParams = %v, want %v", info.typeParams, tt.wantParams)
			}
			for i, p := range info.typeParams {
				if p != tt.wantParams[i] {
					t.Errorf("typeParams[%d] = %q, want %q", i, p, tt.wantParams[i])
				}
			}

			// Check parents.
			if len(info.parents) != len(tt.wantParents) {
				t.Fatalf("parents count = %d, want %d", len(info.parents), len(tt.wantParents))
			}
			for i, wp := range tt.wantParents {
				if info.parents[i].Sym != wp.sym {
					t.Errorf("parents[%d].Sym = %q, want %q", i, info.parents[i].Sym, wp.sym)
				}
				if wp.firstArg != "" {
					if len(info.parents[i].Args) == 0 {
						t.Errorf("parents[%d].Args is empty, want %q", i, wp.firstArg)
					} else if info.parents[i].Args[0].Sym != wp.firstArg {
						t.Errorf("parents[%d].Args[0].Sym = %q, want %q", i, info.parents[i].Args[0].Sym, wp.firstArg)
					}
				}
			}
		})
	}
}

func TestParseMethodGenericSig(t *testing.T) {
	tests := []struct {
		name     string
		sig      string
		classSym string
		params   []string
		wantSym  string
	}{
		{
			name:     "List.get returns E",
			sig:      "(I)TE;",
			classSym: "java/util/List#",
			params:   []string{"E"},
			wantSym:  "java/util/List#[E]",
		},
		{
			name:     "Map.get returns V",
			sig:      "(Ljava/lang/Object;)TV;",
			classSym: "java/util/Map#",
			params:   []string{"K", "V"},
			wantSym:  "java/util/Map#[V]",
		},
		{
			name:     "returns concrete type",
			sig:      "()Ljava/lang/String;",
			classSym: "com/example/Foo#",
			params:   nil,
			wantSym:  "java/lang/String#",
		},
		{
			name:     "returns parameterized type",
			sig:      "()Ljava/util/List<Ljava/lang/String;>;",
			classSym: "com/example/Foo#",
			params:   nil,
			wantSym:  "java/util/List#",
		},
		{
			name:     "returns void",
			sig:      "(Ljava/lang/Object;)V",
			classSym: "java/util/List#",
			params:   []string{"E"},
			wantSym:  "void",
		},
		{
			name:     "method with own type params",
			sig:      "<T:Ljava/lang/Object;>([TT;)Ljava/util/List<TT;>;",
			classSym: "java/util/Arrays#",
			params:   nil,
			wantSym:  "java/util/List#",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := parseMethodGenericSig(tt.sig, tt.classSym, tt.params)
			if info == nil {
				t.Fatal("parseMethodGenericSig returned nil")
			}
			if tt.sig == "<T:Ljava/lang/Object;>(TT;)Ljava/util/List<TT;>;" {
				if len(info.paramTypes) != 1 {
					t.Fatalf("expected 1 param type, got %+v", info.paramTypes)
				}
				if info.paramTypes[0] == nil || info.paramTypes[0].Sym != "com/example/Foo#[T]" {
					t.Fatalf("expected param type T, got %+v", info.paramTypes[0])
				}
			}
			if info.returnType == nil {
				t.Fatal("returnType is nil")
			}
			if info.returnType.Sym != tt.wantSym {
				t.Errorf("returnType.Sym = %q, want %q", info.returnType.Sym, tt.wantSym)
			}
		})
	}
}

func TestParseFieldGenericSig(t *testing.T) {
	tests := []struct {
		name     string
		sig      string
		classSym string
		params   []string
		wantSym  string
		wantArgs int
	}{
		{
			name:     "List<String> field",
			sig:      "Ljava/util/List<Ljava/lang/String;>;",
			classSym: "com/example/Foo#",
			params:   nil,
			wantSym:  "java/util/List#",
			wantArgs: 1,
		},
		{
			name:     "type variable field",
			sig:      "TE;",
			classSym: "java/util/ArrayList#",
			params:   []string{"E"},
			wantSym:  "java/util/ArrayList#[E]",
			wantArgs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := parseFieldGenericSig(tt.sig, tt.classSym, tt.params)
			if info == nil {
				t.Fatal("returned nil")
			}
			if info.returnType == nil {
				t.Fatal("returnType is nil")
			}
			if info.returnType.Sym != tt.wantSym {
				t.Errorf("Sym = %q, want %q", info.returnType.Sym, tt.wantSym)
			}
			if len(info.returnType.Args) != tt.wantArgs {
				t.Errorf("Args count = %d, want %d", len(info.returnType.Args), tt.wantArgs)
			}
		})
	}
}

func TestParseClassGenericSig_Empty(t *testing.T) {
	if info := parseClassGenericSig("", "com/example/Foo#"); info != nil {
		t.Error("expected nil for empty signature")
	}
}

func TestParseMethodGenericSig_Empty(t *testing.T) {
	if info := parseMethodGenericSig("", "com/example/Foo#", nil); info != nil {
		t.Error("expected nil for empty signature")
	}
}

func TestParseClassGenericSig_WildcardBounds(t *testing.T) {
	// Comparable<? super T>
	sig := "<T:Ljava/lang/Object;>Ljava/lang/Object;Ljava/lang/Comparable<-TT;>;"
	info := parseClassGenericSig(sig, "com/example/Foo#")
	if info == nil {
		t.Fatal("returned nil")
	}
	if len(info.typeParams) != 1 || info.typeParams[0] != "T" {
		t.Errorf("typeParams = %v, want [T]", info.typeParams)
	}
	// Comparable parent should have one type arg.
	if len(info.parents) < 2 {
		t.Fatalf("parents count = %d, want >= 2", len(info.parents))
	}
	comp := info.parents[1]
	if comp.Sym != "java/lang/Comparable#" {
		t.Errorf("parent sym = %q, want java/lang/Comparable#", comp.Sym)
	}
	if len(comp.Args) != 1 {
		t.Errorf("Comparable args = %d, want 1", len(comp.Args))
	}
}
