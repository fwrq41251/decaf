package index

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	if defs[0].Range.IsEmpty() {
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
		if !r.Range.IsEmpty() && r.Range.StartLine == 10 {
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

func writeSDB(t *testing.T, path string, docs *sdb.TextDocuments) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	data, err := proto.Marshal(docs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestMembersOfType(t *testing.T) {
	tmpDir := t.TempDir()
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Foo.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "com/example/Foo#", DisplayName: "Foo", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "com/example/Foo#bar().", DisplayName: "bar", Kind: sdb.SymbolInformation_METHOD},
				{Symbol: "com/example/Foo#baz().", DisplayName: "baz", Kind: sdb.SymbolInformation_METHOD},
			},
		}},
	}
	writeSDB(t, filepath.Join(sdbDir, "Foo.java.semanticdb"), docs)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	if err := idx.Load(); err != nil {
		t.Fatal(err)
	}

	members := idx.MembersOfType("com/example/Foo#")
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}
	names := map[string]bool{}
	for _, m := range members {
		names[m.Name] = true
	}
	if !names["bar"] || !names["baz"] {
		t.Fatalf("expected bar and baz, got %v", names)
	}
}

func TestLoad_PopulatesVisibilityFromSemanticDBAccess(t *testing.T) {
	tmpDir := t.TempDir()
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Foo.java",
			Symbols: []*sdb.SymbolInformation{
				{
					Symbol:      "com/example/Foo#",
					DisplayName: "Foo",
					Kind:        sdb.SymbolInformation_CLASS,
					Properties:  int32(sdb.SymbolInformation_FINAL) | int32(sdb.SymbolInformation_SEALED),
					Access:      &sdb.Access{SealedValue: &sdb.Access_PublicAccess{PublicAccess: &sdb.PublicAccess{}}},
				},
				{
					Symbol:      "com/example/Foo#secret().",
					DisplayName: "secret",
					Kind:        sdb.SymbolInformation_METHOD,
					Properties:  int32(sdb.SymbolInformation_OVERRIDE),
					Access:      &sdb.Access{SealedValue: &sdb.Access_PrivateAccess{PrivateAccess: &sdb.PrivateAccess{}}},
				},
				{
					Symbol:      "com/example/Foo#value.",
					DisplayName: "value",
					Kind:        sdb.SymbolInformation_FIELD,
				},
			},
		}},
	}
	writeSDB(t, filepath.Join(sdbDir, "Foo.java.semanticdb"), docs)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	if err := idx.Load(); err != nil {
		t.Fatal(err)
	}

	members := idx.MembersOfType("com/example/Foo#")
	got := map[string]Visibility{}
	for _, m := range members {
		got[m.Name] = m.Visibility
	}
	if got["secret"] != VisibilityPrivate {
		t.Fatalf("secret visibility = %v, want private", got["secret"])
	}
	if got["value"] != VisibilityPackagePrivate {
		t.Fatalf("value visibility = %v, want package-private", got["value"])
	}

	classDef := idx.SymbolDefinition("com/example/Foo#")
	if classDef == nil || classDef.Visibility != VisibilityPublic {
		t.Fatalf("class visibility = %+v, want public", classDef)
	}
	if classDef == nil || !classDef.IsFinal || !classDef.IsSealed {
		t.Fatalf("class modifiers = %+v, want final+sealed", classDef)
	}
	for _, m := range members {
		if m.Name == "secret" && !m.IsOverride {
			t.Fatalf("secret modifiers = %+v, want override", m)
		}
	}
}

func TestMembersOfType_Inherited(t *testing.T) {
	tmpDir := t.TempDir()
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Parent.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "com/example/Parent#", DisplayName: "Parent", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "com/example/Parent#parentMethod().", DisplayName: "parentMethod", Kind: sdb.SymbolInformation_METHOD},
				{
					Symbol: "com/example/Child#", DisplayName: "Child", Kind: sdb.SymbolInformation_CLASS,
					Signature: &sdb.Signature{
						SealedValue: &sdb.Signature_ClassSignature{
							ClassSignature: &sdb.ClassSignature{
								Parents: []*sdb.Type{
									{SealedValue: &sdb.Type_TypeRef{TypeRef: &sdb.TypeRef{Symbol: "com/example/Parent#"}}},
								},
							},
						},
					},
				},
				{Symbol: "com/example/Child#childMethod().", DisplayName: "childMethod", Kind: sdb.SymbolInformation_METHOD},
			},
		}},
	}
	writeSDB(t, filepath.Join(sdbDir, "Parent.java.semanticdb"), docs)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	if err := idx.Load(); err != nil {
		t.Fatal(err)
	}

	members := idx.MembersOfType("com/example/Child#")
	names := map[string]bool{}
	for _, m := range members {
		names[m.Name] = true
	}
	if !names["childMethod"] {
		t.Fatal("expected childMethod in members")
	}
	if !names["parentMethod"] {
		t.Fatal("expected inherited parentMethod in members")
	}
}

func TestTypeOfSymbol(t *testing.T) {
	tmpDir := t.TempDir()
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Foo.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "com/example/Foo#", DisplayName: "Foo", Kind: sdb.SymbolInformation_CLASS},
				{
					Symbol: "com/example/Foo#items.", DisplayName: "items", Kind: sdb.SymbolInformation_FIELD,
					Signature: &sdb.Signature{
						SealedValue: &sdb.Signature_ValueSignature{
							ValueSignature: &sdb.ValueSignature{
								Tpe: &sdb.Type{
									SealedValue: &sdb.Type_TypeRef{
										TypeRef: &sdb.TypeRef{Symbol: "java/util/List#"},
									},
								},
							},
						},
					},
				},
				{
					Symbol: "com/example/Foo#getName().", DisplayName: "getName", Kind: sdb.SymbolInformation_METHOD,
					Signature: &sdb.Signature{
						SealedValue: &sdb.Signature_MethodSignature{
							MethodSignature: &sdb.MethodSignature{
								ReturnType: &sdb.Type{
									SealedValue: &sdb.Type_TypeRef{
										TypeRef: &sdb.TypeRef{Symbol: "java/lang/String#"},
									},
								},
							},
						},
					},
				},
			},
		}},
	}
	writeSDB(t, filepath.Join(sdbDir, "Foo.java.semanticdb"), docs)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	if err := idx.Load(); err != nil {
		t.Fatal(err)
	}

	// Field type
	fieldType := idx.TypeOfSymbol("com/example/Foo#items.")
	if fieldType != "java/util/List#" {
		t.Fatalf("expected field type 'java/util/List#', got %q", fieldType)
	}

	// Method return type
	methodType := idx.TypeOfSymbol("com/example/Foo#getName().")
	if methodType != "java/lang/String#" {
		t.Fatalf("expected method return type 'java/lang/String#', got %q", methodType)
	}
}

func TestIndexDocument_NormalizesEnumConstantDisplayName(t *testing.T) {
	tmpDir := t.TempDir()
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/MyEnum.java",
			Symbols: []*sdb.SymbolInformation{
				{
					Symbol:      "com/example/MyEnum#",
					DisplayName: "MyEnum",
					Kind:        sdb.SymbolInformation_CLASS,
					Properties:  int32(sdb.SymbolInformation_ENUM),
				},
				{
					Symbol:      "com/example/MyEnum#A.",
					DisplayName: `A("x")`,
					Kind:        sdb.SymbolInformation_UNKNOWN_KIND,
					Properties:  int32(sdb.SymbolInformation_ENUM) | int32(sdb.SymbolInformation_STATIC) | int32(sdb.SymbolInformation_FINAL),
					Signature: &sdb.Signature{
						SealedValue: &sdb.Signature_ValueSignature{
							ValueSignature: &sdb.ValueSignature{
								Tpe: &sdb.Type{
									SealedValue: &sdb.Type_TypeRef{
										TypeRef: &sdb.TypeRef{Symbol: "com/example/MyEnum#"},
									},
								},
							},
						},
					},
				},
			},
		}},
	}
	writeSDB(t, filepath.Join(sdbDir, "MyEnum.java.semanticdb"), docs)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	if err := idx.Load(); err != nil {
		t.Fatal(err)
	}

	members := idx.MembersOfType("com/example/MyEnum#")
	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(members))
	}
	if members[0].Name != "A" {
		t.Fatalf("member name = %q, want %q", members[0].Name, "A")
	}
	if members[0].Kind != sdb.SymbolInformation_FIELD {
		t.Fatalf("member kind = %v, want FIELD", members[0].Kind)
	}
	if members[0].Signature == nil || members[0].Signature.Label != "A: MyEnum" {
		t.Fatalf("member signature = %+v, want label %q", members[0].Signature, "A: MyEnum")
	}
}

func TestTypeBySimpleName(t *testing.T) {
	tmpDir := t.TempDir()
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Types.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "com/example/Foo#", DisplayName: "Foo", Kind: sdb.SymbolInformation_CLASS},
				{Symbol: "com/example/Bar#", DisplayName: "Bar", Kind: sdb.SymbolInformation_INTERFACE},
				{Symbol: "com/example/Foo#baz().", DisplayName: "baz", Kind: sdb.SymbolInformation_METHOD},
			},
		}},
	}
	writeSDB(t, filepath.Join(sdbDir, "Types.java.semanticdb"), docs)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	if err := idx.Load(); err != nil {
		t.Fatal(err)
	}

	foos := idx.TypeBySimpleName("Foo")
	if len(foos) != 1 {
		t.Fatalf("expected 1 result for Foo, got %d", len(foos))
	}
	if foos[0].Symbol != "com/example/Foo#" {
		t.Fatalf("expected symbol 'com/example/Foo#', got %q", foos[0].Symbol)
	}

	bars := idx.TypeBySimpleName("Bar")
	if len(bars) != 1 {
		t.Fatalf("expected 1 result for Bar, got %d", len(bars))
	}

	// Method should not appear in TypeBySimpleName
	bazs := idx.TypeBySimpleName("baz")
	if len(bazs) != 0 {
		t.Fatalf("expected 0 results for method 'baz', got %d", len(bazs))
	}
}

func TestSemanticDB_StaticFlag(t *testing.T) {
	tmpDir := t.TempDir()
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Foo.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "com/example/Foo#", DisplayName: "Foo", Kind: sdb.SymbolInformation_CLASS},
				{
					Symbol:      "com/example/Foo#getInstance().",
					DisplayName: "getInstance",
					Kind:        sdb.SymbolInformation_METHOD,
					Properties:  int32(sdb.SymbolInformation_STATIC),
				},
				{
					Symbol:      "com/example/Foo#getName().",
					DisplayName: "getName",
					Kind:        sdb.SymbolInformation_METHOD,
				},
			},
		}},
	}
	writeSDB(t, filepath.Join(sdbDir, "Foo.java.semanticdb"), docs)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	if err := idx.Load(); err != nil {
		t.Fatal(err)
	}

	members := idx.MembersOfType("com/example/Foo#")
	staticMap := make(map[string]bool)
	for _, m := range members {
		staticMap[m.Name] = m.IsStatic
	}

	if !staticMap["getInstance"] {
		t.Error("getInstance should have IsStatic=true")
	}
	if staticMap["getName"] {
		t.Error("getName should have IsStatic=false")
	}
}

func TestExtractOwner(t *testing.T) {
	tests := []struct {
		sym  string
		want string
	}{
		{"com/example/Foo#bar().", "com/example/Foo#"},
		{"com/example/Foo#", ""},
		{"com/example/Foo#Inner#method().", "com/example/Foo#Inner#"},
		{"com/example/Foo#items.", "com/example/Foo#"},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractOwner(tt.sym)
		if got != tt.want {
			t.Errorf("extractOwner(%q) = %q, want %q", tt.sym, got, tt.want)
		}
	}
}

func TestIncrementalIndex(t *testing.T) {
	tmpDir := t.TempDir()
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	defer idx.Close()

	// --- First load: one file with class Foo (full scan, starts watcher) ---
	fooDocs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Schema: sdb.Schema_SEMANTICDB4, Uri: "src/Foo.java", Language: sdb.Language_JAVA,
			Symbols: []*sdb.SymbolInformation{{
				Symbol: "com/example/Foo#", DisplayName: "Foo", Kind: sdb.SymbolInformation_CLASS,
			}},
			Occurrences: []*sdb.SymbolOccurrence{{
				Symbol: "com/example/Foo#", Role: sdb.SymbolOccurrence_DEFINITION,
				Range: &sdb.Range{StartLine: 1, StartCharacter: 13, EndLine: 1, EndCharacter: 16},
			}},
		}},
	}
	writeSDB(t, filepath.Join(sdbDir, "Foo.java.semanticdb"), fooDocs)

	if err := idx.Load(); err != nil {
		t.Fatalf("first Load failed: %v", err)
	}
	if len(idx.AllSymbols()) != 1 {
		t.Fatalf("expected 1 symbol after first load, got %d", len(idx.AllSymbols()))
	}

	// --- Second load: no changes → should be a no-op via watcher ---
	if err := idx.Load(); err != nil {
		t.Fatalf("second Load (no-op) failed: %v", err)
	}
	if len(idx.AllSymbols()) != 1 {
		t.Fatalf("expected 1 symbol after no-op load, got %d", len(idx.AllSymbols()))
	}

	// --- Third load: add a new file Bar (watcher picks up create event) ---
	barDocs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Schema: sdb.Schema_SEMANTICDB4, Uri: "src/Bar.java", Language: sdb.Language_JAVA,
			Symbols: []*sdb.SymbolInformation{{
				Symbol: "com/example/Bar#", DisplayName: "Bar", Kind: sdb.SymbolInformation_CLASS,
			}},
			Occurrences: []*sdb.SymbolOccurrence{{
				Symbol: "com/example/Bar#", Role: sdb.SymbolOccurrence_DEFINITION,
				Range: &sdb.Range{StartLine: 1, StartCharacter: 13, EndLine: 1, EndCharacter: 16},
			}},
		}},
	}
	writeSDB(t, filepath.Join(sdbDir, "Bar.java.semanticdb"), barDocs)
	time.Sleep(100 * time.Millisecond) // let watcher process events

	if err := idx.Load(); err != nil {
		t.Fatalf("third Load (add Bar) failed: %v", err)
	}
	if len(idx.AllSymbols()) != 2 {
		t.Fatalf("expected 2 symbols after adding Bar, got %d", len(idx.AllSymbols()))
	}

	// --- Fourth load: modify Foo (watcher picks up write event) ---
	fooDocs2 := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Schema: sdb.Schema_SEMANTICDB4, Uri: "src/Foo.java", Language: sdb.Language_JAVA,
			Symbols: []*sdb.SymbolInformation{{
				Symbol: "com/example/FooRenamed#", DisplayName: "FooRenamed", Kind: sdb.SymbolInformation_CLASS,
			}},
			Occurrences: []*sdb.SymbolOccurrence{{
				Symbol: "com/example/FooRenamed#", Role: sdb.SymbolOccurrence_DEFINITION,
				Range: &sdb.Range{StartLine: 1, StartCharacter: 13, EndLine: 1, EndCharacter: 23},
			}},
		}},
	}
	writeSDB(t, filepath.Join(sdbDir, "Foo.java.semanticdb"), fooDocs2)
	time.Sleep(100 * time.Millisecond)

	if err := idx.Load(); err != nil {
		t.Fatalf("fourth Load (modify Foo) failed: %v", err)
	}
	all := idx.AllSymbols()
	if len(all) != 2 {
		t.Fatalf("expected 2 symbols after modifying Foo, got %d", len(all))
	}
	names := map[string]bool{}
	for _, s := range all {
		names[s.Name] = true
	}
	if names["Foo"] {
		t.Fatal("old symbol 'Foo' should have been removed")
	}
	if !names["FooRenamed"] || !names["Bar"] {
		t.Fatalf("expected FooRenamed and Bar, got %v", names)
	}

	// --- Fifth load: delete Bar (watcher picks up remove event) ---
	os.Remove(filepath.Join(sdbDir, "Bar.java.semanticdb"))
	time.Sleep(100 * time.Millisecond)

	if err := idx.Load(); err != nil {
		t.Fatalf("fifth Load (delete Bar) failed: %v", err)
	}
	if len(idx.AllSymbols()) != 1 {
		t.Fatalf("expected 1 symbol after deleting Bar, got %d", len(idx.AllSymbols()))
	}
	if idx.AllSymbols()[0].Name != "FooRenamed" {
		t.Fatalf("expected FooRenamed, got %s", idx.AllSymbols()[0].Name)
	}
}

func TestSymbolSignatures_Overloads(t *testing.T) {
	tmpDir := t.TempDir()
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")

	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: "src/Foo.java",
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "com/example/Foo#", DisplayName: "Foo", Kind: sdb.SymbolInformation_CLASS},
				{
					Symbol: "com/example/Foo#add().", DisplayName: "add", Kind: sdb.SymbolInformation_METHOD,
					Signature: &sdb.Signature{
						SealedValue: &sdb.Signature_MethodSignature{
							MethodSignature: &sdb.MethodSignature{
								ParameterLists: []*sdb.Scope{{
									Symlinks: []string{"com/example/Foo#add().(x)"},
								}},
							},
						},
					},
				},
				{
					Symbol: "com/example/Foo#add().", DisplayName: "add", Kind: sdb.SymbolInformation_METHOD,
					Signature: &sdb.Signature{
						SealedValue: &sdb.Signature_MethodSignature{
							MethodSignature: &sdb.MethodSignature{
								ParameterLists: []*sdb.Scope{{
									Symlinks: []string{"com/example/Foo#add().(x)", "com/example/Foo#add().(y)"},
								}},
							},
						},
					},
				},
			},
			Occurrences: []*sdb.SymbolOccurrence{
				{
					Symbol: "com/example/Foo#add().",
					Role:   sdb.SymbolOccurrence_REFERENCE,
					Range:  &sdb.Range{StartLine: 10, StartCharacter: 4, EndLine: 10, EndCharacter: 7},
				},
			},
		}},
	}
	writeSDB(t, filepath.Join(sdbDir, "Foo.java.semanticdb"), docs)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	if err := idx.Load(); err != nil {
		t.Fatal(err)
	}

	uri := "file://" + tmpDir + "/src/Foo.java"

	// SymbolSignatures should return both overloads.
	sigs := idx.SymbolSignatures(uri, 10, 5)
	if len(sigs) != 2 {
		t.Fatalf("expected 2 signatures, got %d", len(sigs))
	}
	for _, s := range sigs {
		if s.Name != "add" {
			t.Errorf("expected name 'add', got %q", s.Name)
		}
	}

	// SymbolSignature (singular) should return the first one.
	sig := idx.SymbolSignature(uri, 10, 5)
	if sig == nil {
		t.Fatal("expected non-nil SymbolSignature")
	}
	if sig.Name != "add" {
		t.Errorf("expected name 'add', got %q", sig.Name)
	}
}

func TestCompletionSymbols_UsesSimpleNameIndexes(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, t.TempDir())

	idx.AddDefinitionForTest("java/util/ArrayList#", "ArrayList")
	idx.AddDefinitionForTest("java/util/LinkedList#", "LinkedList")
	idx.AddMemberForTest("com/example/Foo#append().", "append", "src/Foo.java")
	idx.AddMemberForTest("com/example/Bar#append().", "append", "src/Bar.java")

	got := idx.CompletionSymbols("file://"+filepath.Join(idx.SourceRoot(), "src/Foo.java"), "apd")
	if len(got) != 2 {
		t.Fatalf("expected 2 append candidates, got %d: %+v", len(got), got)
	}
	if got[0].URI != "src/Foo.java" || !got[0].SameFile {
		t.Fatalf("expected same-file append first, got %+v", got[0])
	}
	for _, s := range got {
		if s.Name != "append" {
			t.Fatalf("unexpected candidate %+v", s)
		}
	}

	typeGot := idx.CompletionSymbols("", "arl")
	if len(typeGot) == 0 || typeGot[0].Name != "ArrayList" {
		t.Fatalf("expected ArrayList to match fuzzy type completion, got %+v", typeGot)
	}
}

func TestCompletionSymbols_PrefixMatchesBeatFuzzyMatchesBeforeCap(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, t.TempDir())

	for i := 0; i < 120; i++ {
		idx.AddDefinitionForTest(
			fmt.Sprintf("pkg/AStringThing%03d#", i),
			fmt.Sprintf("AStringThing%03d", i),
		)
	}
	idx.AddDefinitionForTest("java/lang/String#", "String")

	got := idx.CompletionSymbols("", "Str")
	if len(got) == 0 {
		t.Fatal("expected completion candidates")
	}
	if len(got) != 100 {
		t.Fatalf("expected capped results of 100, got %d", len(got))
	}
	if got[0].Name != "String" {
		t.Fatalf("expected String to rank ahead of fuzzy-only matches, got %+v", got[0])
	}

	foundString := false
	for _, s := range got {
		if s.Symbol == "java/lang/String#" {
			foundString = true
			break
		}
	}
	if !foundString {
		t.Fatalf("expected String to remain in capped results, got %+v", got[:5])
	}
}

func TestLocalSymbol_ScopedToFile(t *testing.T) {
	tmpDir := t.TempDir()
	sdbDir := filepath.Join(tmpDir, "META-INF", "semanticdb")

	// Two files each with a local variable using the same SemanticDB symbol "local0".
	fooDocs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Schema: sdb.Schema_SEMANTICDB4, Uri: "src/Foo.java", Language: sdb.Language_JAVA,
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "local0", DisplayName: "count", Kind: sdb.SymbolInformation_LOCAL},
			},
			Occurrences: []*sdb.SymbolOccurrence{
				{Symbol: "local0", Role: sdb.SymbolOccurrence_DEFINITION,
					Range: &sdb.Range{StartLine: 5, StartCharacter: 8, EndLine: 5, EndCharacter: 13}},
				{Symbol: "local0", Role: sdb.SymbolOccurrence_REFERENCE,
					Range: &sdb.Range{StartLine: 6, StartCharacter: 8, EndLine: 6, EndCharacter: 13}},
			},
		}},
	}
	barDocs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Schema: sdb.Schema_SEMANTICDB4, Uri: "src/Bar.java", Language: sdb.Language_JAVA,
			Symbols: []*sdb.SymbolInformation{
				{Symbol: "local0", DisplayName: "count", Kind: sdb.SymbolInformation_LOCAL},
			},
			Occurrences: []*sdb.SymbolOccurrence{
				{Symbol: "local0", Role: sdb.SymbolOccurrence_DEFINITION,
					Range: &sdb.Range{StartLine: 10, StartCharacter: 8, EndLine: 10, EndCharacter: 13}},
				{Symbol: "local0", Role: sdb.SymbolOccurrence_REFERENCE,
					Range: &sdb.Range{StartLine: 11, StartCharacter: 8, EndLine: 11, EndCharacter: 13}},
			},
		}},
	}

	writeSDB(t, filepath.Join(sdbDir, "Foo.java.semanticdb"), fooDocs)
	writeSDB(t, filepath.Join(sdbDir, "Bar.java.semanticdb"), barDocs)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, tmpDir)
	if err := idx.Load(); err != nil {
		t.Fatal(err)
	}

	fooURI := "file://" + tmpDir + "/src/Foo.java"

	// References from Foo.java should only return Foo.java occurrences.
	refs := idx.References(fooURI, 5, 9)
	for _, r := range refs {
		if r.URI != "src/Foo.java" {
			t.Fatalf("References returned occurrence from %s, want only src/Foo.java", r.URI)
		}
	}

	// RenameOccurrences from Foo.java should only return Foo.java occurrences.
	_, renameOccs := idx.RenameOccurrences(fooURI, 5, 9)
	if len(renameOccs) == 0 {
		t.Fatal("expected rename occurrences for local0 in Foo.java")
	}
	for _, o := range renameOccs {
		if o.URI != "src/Foo.java" {
			t.Fatalf("RenameOccurrences returned occurrence from %s, want only src/Foo.java", o.URI)
		}
	}
}

func TestSimpleTypeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Class symbols
		{"java/util/List#", "List"},
		{"com/example/Foo#", "Foo"},
		// Type parameters
		{"java/util/List#[E]", "E"},
		// Primitive types (passthrough)
		{"int", "int"},
		{"boolean", "boolean"},
		{"void", "void"},
		// Already-simple names
		{"String", "String"},
		// Inner classes
		{"com/example/Outer.Inner#", "Inner"},
		// Empty
		{"", ""},
	}
	for _, tt := range tests {
		got := SimpleTypeName(tt.input)
		if got != tt.want {
			t.Errorf("SimpleTypeName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCompletionMatchScore_Order(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "String", query: "string", want: MatchExact},
		{name: "String", query: "str", want: MatchPrefix},
		{name: "StringBuilder", query: "sb", want: MatchWordStart},
		{name: "My_StringBuilder", query: "sb", want: MatchWordStart},
		{name: "AStringThing", query: "string", want: MatchSubstring},
		{name: "AbstractList", query: "srt", want: MatchFuzzy},
		{name: "LocalDate", query: "xyz", want: MatchNone},
	}

	for _, tt := range tests {
		got := CompletionMatchScore(tt.name, tt.query)
		if got != tt.want {
			t.Fatalf("completionMatchScore(%q, %q) = %d, want %d", tt.name, tt.query, got, tt.want)
		}
	}
}

func TestCompletionSymbols_WordStartMatchesBeatSubstringAndFuzzy(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	idx := NewIndex(logger, t.TempDir())

	idx.AddDefinitionForTest("java/lang/StringBuilder#", "StringBuilder")
	idx.AddDefinitionForTest("pkg/MyStringBuffer#", "MyStringBuffer")
	idx.AddDefinitionForTest("pkg/Superb#", "Superb")

	got := idx.CompletionSymbols("", "sb")
	if len(got) < 2 {
		t.Fatalf("expected at least 2 completion candidates, got %+v", got)
	}
	if got[0].Name != "StringBuilder" {
		t.Fatalf("expected word-start match StringBuilder first, got %+v", got[0])
	}
	if got[1].Name != "MyStringBuffer" {
		t.Fatalf("expected substring match MyStringBuffer after StringBuilder, got %+v", got[1])
	}
}
