package lsp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fwrq41251/decaf/internal/bsp"
	"github.com/fwrq41251/decaf/internal/index"
	"github.com/fwrq41251/decaf/internal/setup"
	"github.com/fwrq41251/decaf/internal/uri"
)

type workspaceService struct {
	handler *Handler

	// debounceMu protects debounceTimer and pendingURIs.
	debounceMu    sync.Mutex
	debounceTimer *time.Timer
	pendingURIs   []string
	// compileMu ensures only one compilation/reindex cycle runs at a time.
	compileMu sync.Mutex
	// bgMu protects backgroundCtx and backgroundCancel.
	bgMu             sync.Mutex
	backgroundCtx    context.Context
	backgroundCancel context.CancelFunc
	bgWg             sync.WaitGroup
}

func newWorkspaceService(h *Handler) *workspaceService {
	return &workspaceService{handler: h}
}

func (ws *workspaceService) start(ctx context.Context) {
	h := ws.handler

	ctx, cancel := context.WithCancel(ctx)
	ws.bgMu.Lock()
	ws.backgroundCtx = ctx
	ws.backgroundCancel = cancel
	ws.bgMu.Unlock()
	h.logger.Println("client sent initialized notification")

	sourceRoot := uri.ToPath(h.rootURI)
	h.setIndexForTest(index.NewIndex(h.logger, sourceRoot))

	setupHelper := setup.NewSetup(h.logger, sourceRoot)
	if jdkSrc := setupHelper.DiscoverJDKSource(""); jdkSrc != "" {
		h.logger.Printf("Initially discovered JDK source: %s", jdkSrc)
		h.index().SetJdkSourceRoot(jdkSrc)
	}

	ws.reindex()
	h.index().LogStatsSnapshot("after initial workspace index")

	needsFullBuild := !h.index().HasFiles()
	if !needsFullBuild {
		h.closeIndexReady()
	}

	ws.bgWg.Add(1)
	go func() {
		defer ws.bgWg.Done()
		if needsFullBuild {
			defer h.closeIndexReady()
		}

		h.registerFileWatchers(ctx)
		prog := h.beginProgress(ctx, "decaf", "initializing…")

		if needsFullBuild {
			h.logger.Println("No indexed files found, starting full setup and compilation...")
			prog.report("setting up project…", intPtr(10))
			setupHelper = setup.NewSetup(h.logger, sourceRoot)
			if err := setupHelper.Run(ctx); err != nil {
				h.logger.Printf("auto-setup failed: %v", err)
			}
		}

		if ctx.Err() != nil {
			prog.end("cancelled")
			return
		}

		prog.report("connecting to Bloop…", intPtr(30))
		if err := h.buildClient().Start(ctx, h.rootURI); err != nil {
			h.showMessage(MessageTypeError, fmt.Sprintf("decaf: failed to start Bloop: %v", err))
			prog.end("failed to connect")
			return
		}

		if ctx.Err() != nil {
			prog.end("cancelled")
			return
		}

		prog.report("fetching dependencies…", intPtr(50))
		if items, err := h.buildClient().DependencySources(ctx); err == nil {
			for _, item := range items {
				for _, src := range item.Sources {
					if strings.HasSuffix(src, ".jar") {
						h.index().AddDependencySource(uri.ToPath(src))
					}
				}
			}
		}

		if ctx.Err() != nil {
			prog.end("cancelled")
			return
		}

		var classpathJARs []string
		var discoveredJavaHome string
		if envs, err := h.buildClient().JvmRunEnvironment(ctx); err == nil && len(envs) > 0 {
			for _, env := range envs {
				if env.JavaHome != "" {
					discoveredJavaHome = uri.ToPath(env.JavaHome)
					if jdkSrc := setupHelper.DiscoverJDKSource(discoveredJavaHome); jdkSrc != "" {
						h.logger.Printf("Refined JDK source from BSP: %s", jdkSrc)
						h.index().SetJdkSourceRoot(jdkSrc)
					}
				}
				for _, cp := range env.Classpath {
					p := uri.ToPath(cp)
					if strings.HasSuffix(p, ".jar") {
						classpathJARs = append(classpathJARs, p)
					}
				}
			}
		}

		if discoveredJavaHome == "" {
			discoveredJavaHome = setupHelper.DiscoverJavaHome("")
		}

		classpathJARs = append(classpathJARs, discoverJDKJmods(h.logger, discoveredJavaHome)...)

		if ctx.Err() != nil {
			prog.end("cancelled")
			return
		}

		h.index().LogStatsSnapshot("before dependency indexing")
		if len(classpathJARs) > 0 {
			prog.report("indexing dependencies…", intPtr(55))
			h.index().IndexClasspathJARs(classpathJARs)
		}
		h.index().LogStatsSnapshot("after dependency indexing")

		if needsFullBuild {
			prog.report("compiling…", intPtr(70))
			if err := h.buildClient().Compile(ctx); err != nil {
				var ce *bsp.CompileError
				if errors.As(err, &ce) {
					h.showMessage(MessageTypeWarning, "decaf: initial compilation failed (check your code for errors)")
				} else {
					h.showMessage(MessageTypeError, fmt.Sprintf("decaf: BSP infrastructure error: %v", err))
				}
			}
			if ctx.Err() != nil {
				prog.end("cancelled")
				return
			}
			prog.report("indexing…", intPtr(90))
			ws.reindex()
			h.index().LogStatsSnapshot("after compile + reindex")
		} else {
			h.logger.Println("Existing index found, running compile for diagnostics…")
			prog.report("compiling for diagnostics…", intPtr(70))
			if err := h.buildClient().Compile(ctx); err != nil {
				h.logCompileError("diagnostics compile", err)
			}
		}

		prog.end("ready")
	}()
}

func (ws *workspaceService) close(ctx context.Context) {
	ws.bgMu.Lock()
	if ws.backgroundCancel != nil {
		ws.backgroundCancel()
	}
	ws.bgMu.Unlock()

	ws.debounceMu.Lock()
	if ws.debounceTimer != nil {
		ws.debounceTimer.Stop()
	}
	ws.pendingURIs = nil
	ws.debounceMu.Unlock()

	ws.bgWg.Wait()
	if ws.handler.index() != nil {
		ws.handler.index().Close()
	}
	if err := ws.handler.buildClient().Shutdown(ctx); err != nil {
		ws.handler.logger.Printf("bloop shutdown error during cleanup: %v", err)
	}
}

func (ws *workspaceService) shutdown(ctx context.Context) {
	ws.handler.logger.Println("shutdown requested")
	ws.close(ctx)
}

func (ws *workspaceService) reindex() {
	if ws.handler.index() == nil {
		return
	}
	if err := ws.handler.index().Load(); err != nil {
		ws.handler.logger.Printf("reindex failed: %v", err)
	}
}

func (ws *workspaceService) scheduleCompile(uris ...string) {
	ws.debounceMu.Lock()
	defer ws.debounceMu.Unlock()

	ws.pendingURIs = append(ws.pendingURIs, uris...)
	if ws.debounceTimer != nil {
		ws.debounceTimer.Stop()
	}
	ws.debounceTimer = time.AfterFunc(500*time.Millisecond, ws.runCompileCycle)
}

func (ws *workspaceService) backgroundContext() context.Context {
	ws.bgMu.Lock()
	defer ws.bgMu.Unlock()
	return ws.backgroundCtx
}

func (ws *workspaceService) runCompileCycle() {
	h := ws.handler
	totalStart := time.Now()

	ws.compileMu.Lock()
	defer ws.compileMu.Unlock()
	h.logger.Printf("[timing] acquired compileMu after %v", time.Since(totalStart))

	ws.bgMu.Lock()
	ctx := ws.backgroundCtx
	ws.bgMu.Unlock()
	if ctx == nil {
		return
	}

	ws.debounceMu.Lock()
	changedURIs := ws.pendingURIs
	ws.pendingURIs = nil
	ws.debounceMu.Unlock()

	if len(changedURIs) == 0 {
		return
	}

	if !h.buildClient().IsReady() {
		h.logger.Printf("skipping compile cycle for %d file(s): BSP client not ready", len(changedURIs))
		return
	}

	prog := h.beginProgress(ctx, "decaf", "compiling…")

	compiled := false
	t0 := time.Now()
	targets := ws.resolveTargets(ctx, changedURIs)
	h.logger.Printf("[timing] resolveTargets (%d URIs -> %d targets) took %v", len(changedURIs), len(targets), time.Since(t0))
	if len(targets) > 0 {
		t1 := time.Now()
		err := h.buildClient().CompileTargets(ctx, targets)
		h.logger.Printf("[timing] CompileTargets took %v", time.Since(t1))
		if err != nil {
			h.logCompileError("compile on file change", err)
		}
		compiled = true
	}

	if !compiled {
		t1 := time.Now()
		if err := h.buildClient().Compile(ctx); err != nil {
			h.logCompileError("full compile on file change", err)
		}
		h.logger.Printf("[timing] full Compile took %v", time.Since(t1))
	}

	prog.report("indexing…", nil)
	t2 := time.Now()
	ws.reindex()
	h.logger.Printf("[timing] reindex took %v", time.Since(t2))

	prog.end("done")
	h.logger.Printf("[timing] total compile+reindex cycle took %v", time.Since(totalStart))
}

func (ws *workspaceService) resolveTargets(ctx context.Context, uris []string) []bsp.BuildTargetIdentifier {
	seen := make(map[string]struct{})
	var targets []bsp.BuildTargetIdentifier
	for _, u := range uris {
		ts, err := ws.handler.buildClient().InverseSources(ctx, u)
		if err != nil {
			if errors.Is(err, bsp.ErrNotConnected) {
				ws.handler.logger.Printf("inverseSources skipped for %s: BSP client not ready", u)
				return nil
			}
			ws.handler.logger.Printf("inverseSources failed for %s: %v", u, err)
			return nil
		}
		for _, t := range ts {
			if _, ok := seen[t.URI]; !ok {
				seen[t.URI] = struct{}{}
				targets = append(targets, t)
			}
		}
	}
	return targets
}

func discoverJDKJmods(logger *log.Logger, javaHome string) []string {
	if javaHome == "" {
		return nil
	}

	jmodsDir := filepath.Join(javaHome, "jmods")
	entries, err := os.ReadDir(jmodsDir)
	if err != nil {
		return nil
	}

	var jmods []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jmod") {
			jmods = append(jmods, filepath.Join(jmodsDir, e.Name()))
		}
	}
	if len(jmods) > 0 {
		logger.Printf("discovered %d JDK jmod files in %s", len(jmods), jmodsDir)
	}
	return jmods
}
