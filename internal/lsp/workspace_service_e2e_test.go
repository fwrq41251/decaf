package lsp

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fwrq41251/decaf/internal/bsp"
	"github.com/fwrq41251/decaf/internal/index"
	"github.com/fwrq41251/decaf/internal/jsonrpc"
	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"google.golang.org/protobuf/proto"
)

type fakeBuildClient struct {
	mu sync.Mutex

	ready bool

	startCalls          int
	compileCalls        int
	compileTargetsCalls int
	shutdownCalls       int

	lastRootURI string
	lastTargets []bsp.BuildTargetIdentifier

	startHook          func(context.Context, string) error
	compileHook        func(context.Context) error
	compileTargetsHook func(context.Context, []bsp.BuildTargetIdentifier) error
	inverseSourcesHook func(context.Context, string) ([]bsp.BuildTargetIdentifier, error)
	dependencyHook     func(context.Context) ([]bsp.DependencySourcesItem, error)
	jvmHook            func(context.Context) ([]bsp.JvmEnvironmentItem, error)
}

func (f *fakeBuildClient) Start(ctx context.Context, rootURI string) error {
	f.mu.Lock()
	f.startCalls++
	f.lastRootURI = rootURI
	f.ready = true
	hook := f.startHook
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, rootURI)
	}
	return nil
}

func (f *fakeBuildClient) Shutdown(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdownCalls++
	f.ready = false
	return nil
}

func (f *fakeBuildClient) IsReady() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready
}

func (f *fakeBuildClient) Compile(ctx context.Context) error {
	f.mu.Lock()
	f.compileCalls++
	hook := f.compileHook
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx)
	}
	return nil
}

func (f *fakeBuildClient) CompileTargets(ctx context.Context, targets []bsp.BuildTargetIdentifier) error {
	f.mu.Lock()
	f.compileTargetsCalls++
	f.lastTargets = append([]bsp.BuildTargetIdentifier(nil), targets...)
	hook := f.compileTargetsHook
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, targets)
	}
	return nil
}

func (f *fakeBuildClient) InverseSources(ctx context.Context, fileURI string) ([]bsp.BuildTargetIdentifier, error) {
	f.mu.Lock()
	hook := f.inverseSourcesHook
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, fileURI)
	}
	return nil, nil
}

func (f *fakeBuildClient) DependencySources(ctx context.Context) ([]bsp.DependencySourcesItem, error) {
	f.mu.Lock()
	hook := f.dependencyHook
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx)
	}
	return nil, nil
}

func (f *fakeBuildClient) JvmRunEnvironment(ctx context.Context) ([]bsp.JvmEnvironmentItem, error) {
	f.mu.Lock()
	hook := f.jvmHook
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx)
	}
	return nil, nil
}

func (f *fakeBuildClient) snapshot() fakeBuildClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *f
	cp.lastTargets = append([]bsp.BuildTargetIdentifier(nil), f.lastTargets...)
	return cp
}

func newWorkspaceTestHandler(t *testing.T, rootDir string) (*Handler, *fakeBuildClient) {
	t.Helper()
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	h := NewHandler(logger, jsonrpc.NewTransport(&bytes.Buffer{}, &bytes.Buffer{}))
	h.rootURI = "file://" + filepath.ToSlash(rootDir)
	fake := &fakeBuildClient{}
	h.setBuildClientForTest(fake)
	t.Cleanup(func() {
		h.Close(context.Background())
	})
	return h, fake
}

func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

func writeSemanticDocument(t *testing.T, rootDir, docURI, symbol string) {
	t.Helper()
	srcPath := filepath.Join(rootDir, filepath.FromSlash(docURI))
	if err := os.MkdirAll(filepath.Dir(srcPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcPath, []byte("class Placeholder {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	docs := &sdb.TextDocuments{
		Documents: []*sdb.TextDocument{{
			Uri: docURI,
			Symbols: []*sdb.SymbolInformation{{
				Symbol:      symbol,
				DisplayName: "Main",
				Kind:        sdb.SymbolInformation_CLASS,
			}},
			Occurrences: []*sdb.SymbolOccurrence{{
				Symbol: symbol,
				Role:   sdb.SymbolOccurrence_DEFINITION,
				Range:  &sdb.Range{StartLine: 0, StartCharacter: 0, EndLine: 0, EndCharacter: 4},
			}},
		}},
	}

	data, err := proto.Marshal(docs)
	if err != nil {
		t.Fatal(err)
	}
	sdbPath := filepath.Join(rootDir, "META-INF", "semanticdb", "Main.java.semanticdb")
	if err := os.MkdirAll(filepath.Dir(sdbPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sdbPath, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceStart_ExistingIndexCompilesDiagnostics(t *testing.T) {
	rootDir := t.TempDir()
	writeSemanticDocument(t, rootDir, "src/Main.java", "com/example/Main#")

	h, fake := newWorkspaceTestHandler(t, rootDir)
	h.workspace.start(context.Background())

	waitForCondition(t, time.Second, func() bool {
		select {
		case <-h.indexReady:
			return true
		default:
			return false
		}
	}, "index was not marked ready")

	waitForCondition(t, time.Second, func() bool {
		return fake.snapshot().compileCalls == 1
	}, "expected diagnostics compile to run")

	snap := fake.snapshot()
	if snap.startCalls != 1 {
		t.Fatalf("startCalls = %d, want 1", snap.startCalls)
	}
	if h.index() == nil || !h.index().HasFiles() {
		t.Fatal("expected existing semanticdb files to remain indexed")
	}
}

func TestWorkspaceStart_FullCompileReindexesWorkspace(t *testing.T) {
	rootDir := t.TempDir()
	h, fake := newWorkspaceTestHandler(t, rootDir)
	fake.compileHook = func(context.Context) error {
		writeSemanticDocument(t, rootDir, "src/Main.java", "com/example/Main#")
		time.Sleep(50 * time.Millisecond)
		return nil
	}

	h.workspace.start(context.Background())

	waitForCondition(t, time.Second, func() bool {
		return fake.snapshot().compileCalls == 1
	}, "full compile did not run")

	waitForCondition(t, time.Second, func() bool {
		select {
		case <-h.indexReady:
			return true
		default:
			return false
		}
	}, "workspace did not signal ready after full compile")

	waitForCondition(t, time.Second, func() bool {
		return h.index() != nil && h.index().HasFiles()
	}, "workspace index was not populated after full compile")

	snap := fake.snapshot()
	if snap.startCalls != 1 {
		t.Fatalf("startCalls = %d, want 1", snap.startCalls)
	}
}

func TestWorkspaceRunCompileCycle_UsesTargetCompileAndReindexes(t *testing.T) {
	rootDir := t.TempDir()
	h, fake := newWorkspaceTestHandler(t, rootDir)

	idx := index.NewIndex(h.logger, rootDir)
	h.setIndexForTest(idx)
	t.Cleanup(func() { idx.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.workspace.bgMu.Lock()
	h.workspace.backgroundCtx = ctx
	h.workspace.bgMu.Unlock()

	target := bsp.BuildTargetIdentifier{URI: "target://main"}
	fake.ready = true
	fake.inverseSourcesHook = func(context.Context, string) ([]bsp.BuildTargetIdentifier, error) {
		return []bsp.BuildTargetIdentifier{target}, nil
	}
	fake.compileTargetsHook = func(context.Context, []bsp.BuildTargetIdentifier) error {
		writeSemanticDocument(t, rootDir, "src/Main.java", "com/example/Main#")
		time.Sleep(50 * time.Millisecond)
		return nil
	}

	h.workspace.debounceMu.Lock()
	h.workspace.pendingURIs = []string{"file://" + filepath.ToSlash(filepath.Join(rootDir, "src", "Main.java"))}
	h.workspace.debounceMu.Unlock()

	h.workspace.runCompileCycle()

	snap := fake.snapshot()
	if snap.compileTargetsCalls != 1 {
		t.Fatalf("compileTargetsCalls = %d, want 1", snap.compileTargetsCalls)
	}
	if snap.compileCalls != 0 {
		t.Fatalf("compileCalls = %d, want 0", snap.compileCalls)
	}
	if len(snap.lastTargets) != 1 || snap.lastTargets[0].URI != target.URI {
		t.Fatalf("lastTargets = %+v, want [%s]", snap.lastTargets, target.URI)
	}
	if !h.index().HasFiles() {
		t.Fatal("expected reindex after target compile to populate workspace symbols")
	}
}
