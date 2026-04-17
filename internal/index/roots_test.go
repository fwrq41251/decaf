package index

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
)

func TestDiscoverScanAndWatchRootsFromBloop(t *testing.T) {
	tmpDir := t.TempDir()
	classesDir := "target/root/classes"
	writeBloopConfig(t, tmpDir, "root.json", tmpDir, classesDir)

	idx := NewIndex(log.New(&bytes.Buffer{}, "[test] ", 0), tmpDir)
	scanRoots, watchRoots := idx.discoverScanAndWatchRoots()

	wantWatch := filepath.Join(tmpDir, classesDir)
	wantScan := filepath.Join(wantWatch, "META-INF", "semanticdb")
	if len(scanRoots) != 1 || scanRoots[0] != wantScan {
		t.Fatalf("scanRoots = %v, want [%s]", scanRoots, wantScan)
	}
	if len(watchRoots) != 1 || watchRoots[0] != wantWatch {
		t.Fatalf("watchRoots = %v, want [%s]", watchRoots, wantWatch)
	}
}

func TestLoadPrefersBloopSemanticDBRoots(t *testing.T) {
	tmpDir := t.TempDir()
	classesDir := filepath.Join(tmpDir, "target", "root", "classes")
	writeBloopConfig(t, tmpDir, "root.json", tmpDir, classesDir)

	realDocs := classDocs("src/Real.java", "com/example/Real#", "Real")
	writeSDB(t, filepath.Join(classesDir, "META-INF", "semanticdb", "Real.java.semanticdb"), realDocs)

	ignoredDocs := classDocs("src/Ignored.java", "com/example/Ignored#", "Ignored")
	writeSDB(t, filepath.Join(tmpDir, "META-INF", "semanticdb", "Ignored.java.semanticdb"), ignoredDocs)

	idx := NewIndex(log.New(&bytes.Buffer{}, "[test] ", 0), tmpDir)
	defer idx.Close()
	if err := idx.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	all := idx.AllSymbols()
	if len(all) != 1 || all[0].Name != "Real" {
		t.Fatalf("indexed symbols = %+v, want only Real", all)
	}
}

func TestLoadReconfiguresWatcherRootsWhenBloopAppears(t *testing.T) {
	tmpDir := t.TempDir()

	fooDocs := classDocs("src/Foo.java", "com/example/Foo#", "Foo")
	writeSDB(t, filepath.Join(tmpDir, "META-INF", "semanticdb", "Foo.java.semanticdb"), fooDocs)
	writeJavaSource(t, filepath.Join(tmpDir, "src", "Foo.java"), "class Foo {}")

	idx := NewIndex(log.New(&bytes.Buffer{}, "[test] ", 0), tmpDir)
	defer idx.Close()
	if err := idx.Load(); err != nil {
		t.Fatalf("initial Load failed: %v", err)
	}
	if all := idx.AllSymbols(); len(all) != 1 || all[0].Name != "Foo" {
		t.Fatalf("initial symbols = %+v, want only Foo", all)
	}

	classesDir := filepath.Join(tmpDir, "target", "root", "classes")
	writeBloopConfig(t, tmpDir, "root.json", tmpDir, classesDir)
	barDocs := classDocs("src/Bar.java", "com/example/Bar#", "Bar")
	writeSDB(t, filepath.Join(classesDir, "META-INF", "semanticdb", "Bar.java.semanticdb"), barDocs)

	if err := idx.Load(); err != nil {
		t.Fatalf("reconfigured Load failed: %v", err)
	}

	all := idx.AllSymbols()
	if len(all) != 1 || all[0].Name != "Bar" {
		t.Fatalf("symbols after root switch = %+v, want only Bar", all)
	}
}

func TestWatcherPathFiltering(t *testing.T) {
	tmpDir := t.TempDir()
	root := filepath.Join(tmpDir, "target", "root", "classes")

	w := &watcher{}
	w.setRoots([]string{root})

	if !w.shouldWatchDir(tmpDir) {
		t.Fatal("workspace ancestor should be watched so the configured root can appear later")
	}
	if !w.shouldWatchDir(filepath.Join(tmpDir, "target")) {
		t.Fatal("ancestor of configured root should be watched")
	}
	if !w.shouldWatchDir(filepath.Join(root, "META-INF")) {
		t.Fatal("directory inside configured root should be watched")
	}
	if w.shouldWatchDir(filepath.Join(tmpDir, "other")) {
		t.Fatal("unrelated sibling directory should not be watched")
	}
	if !w.shouldTrackFile(filepath.Join(root, "META-INF", "semanticdb", "Real.java.semanticdb")) {
		t.Fatal("semanticdb file inside configured root should be tracked")
	}
	if w.shouldTrackFile(filepath.Join(tmpDir, "META-INF", "semanticdb", "Ignored.java.semanticdb")) {
		t.Fatal("semanticdb file outside configured root should be ignored")
	}
}

func TestWatcherIgnoresSemanticDBOutsideConfiguredRoots(t *testing.T) {
	tmpDir := t.TempDir()
	classesDir := filepath.Join(tmpDir, "target", "root", "classes")
	writeBloopConfig(t, tmpDir, "root.json", tmpDir, classesDir)

	realDocs := classDocs("src/Real.java", "com/example/Real#", "Real")
	writeSDB(t, filepath.Join(classesDir, "META-INF", "semanticdb", "Real.java.semanticdb"), realDocs)

	idx := NewIndex(log.New(&bytes.Buffer{}, "[test] ", 0), tmpDir)
	defer idx.Close()
	if err := idx.Load(); err != nil {
		t.Fatalf("initial Load failed: %v", err)
	}

	ignoredDocs := classDocs("src/Ignored.java", "com/example/Ignored#", "Ignored")
	writeSDB(t, filepath.Join(tmpDir, "META-INF", "semanticdb", "Ignored.java.semanticdb"), ignoredDocs)
	time.Sleep(100 * time.Millisecond)

	if err := idx.Load(); err != nil {
		t.Fatalf("watcher Load failed: %v", err)
	}

	all := idx.AllSymbols()
	if len(all) != 1 || all[0].Name != "Real" {
		t.Fatalf("symbols after ignored update = %+v, want only Real", all)
	}
}

func writeBloopConfig(t *testing.T, workspaceDir, fileName, projectDir, classesDir string) {
	t.Helper()

	bloopDir := filepath.Join(workspaceDir, ".bloop")
	if err := os.MkdirAll(bloopDir, 0755); err != nil {
		t.Fatal(err)
	}

	config := map[string]any{
		"version": "1.4.0",
		"project": map[string]any{
			"name":       strings.TrimSuffix(fileName, filepath.Ext(fileName)),
			"directory":  projectDir,
			"classesDir": classesDir,
		},
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bloopDir, fileName), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func classDocs(uri, sym, name string) *sdb.TextDocuments {
	return &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Schema:   sdb.Schema_SEMANTICDB4,
			Uri:      uri,
			Language: sdb.Language_JAVA,
			Symbols: []*sdb.SymbolInformation{{
				Symbol:      sym,
				DisplayName: name,
				Kind:        sdb.SymbolInformation_CLASS,
			}},
			Occurrences: []*sdb.SymbolOccurrence{{
				Symbol: sym,
				Role:   sdb.SymbolOccurrence_DEFINITION,
				Range:  &sdb.Range{StartLine: 1, StartCharacter: 13, EndLine: 1, EndCharacter: int32(13 + len(name))},
			}},
		}},
	}
}

func writeJavaSource(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
