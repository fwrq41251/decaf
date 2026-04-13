package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/fwrq41251/decaf/internal/bsp"
	"github.com/fwrq41251/decaf/internal/index"
	"github.com/fwrq41251/decaf/internal/jsonrpc"
	"github.com/fwrq41251/decaf/internal/setup"
	"github.com/fwrq41251/decaf/internal/uri"
)

func (h *Handler) handleInitialize(_ context.Context, params json.RawMessage) (any, error) {
	var p InitializeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	h.rootURI = p.RootURI
	h.clientCaps = p.Capabilities
	h.logger.Printf("initialize: rootURI=%s, processID=%v", p.RootURI, p.ProcessID)
	if p.Capabilities.TextDocument != nil && p.Capabilities.TextDocument.Completion != nil && p.Capabilities.TextDocument.Completion.CompletionItem != nil {
		h.logger.Printf("initialize: completion.snippetSupport=%v", p.Capabilities.TextDocument.Completion.CompletionItem.SnippetSupport)
	} else {
		h.logger.Printf("initialize: completion.snippetSupport=unknown")
	}

	caps := ServerCapabilities{
		TextDocumentSync: &TextDocumentSyncOptions{
			OpenClose: true,
			Change:    SyncIncremental,
			Save:      &SaveOptions{IncludeText: false},
		},
		DefinitionProvider:        true,
		ReferencesProvider:        true,
		HoverProvider:             true,
		CompletionProvider:        &CompletionOptions{TriggerCharacters: []string{"."}},
		SignatureHelpProvider:     &SignatureHelpOptions{TriggerCharacters: []string{"(", ","}},
		RenameProvider:            &RenameOptions{PrepareProvider: true},
		DocumentSymbolProvider:    true,
		DocumentHighlightProvider: true,
		ImplementationProvider:    true,
		WorkspaceSymbolProvider:   true,
		CodeActionProvider:        &CodeActionOptions{CodeActionKinds: []string{CodeActionSourceOrganizeImports, CodeActionQuickFix, "source"}},
		ExecuteCommandProvider:    &ExecuteCommandOptions{Commands: []string{"decaf.overrideMethod", "decaf.generateGetter", "decaf.generateSetter"}},
		InlayHintProvider:         true,
		CallHierarchyProvider:     true,
		TypeHierarchyProvider:     true,
	}

	return InitializeResult{
		Capabilities: caps,
		ServerInfo: &ServerInfo{
			Name:    "decaf",
			Version: "0.0.1",
		},
	}, nil
}

func (h *Handler) handleInitialized(ctx context.Context, _ json.RawMessage) (any, error) {
	h.initialized.Store(true)
	ctx, cancel := context.WithCancel(ctx)
	h.bgMu.Lock()
	h.backgroundCtx = ctx
	h.backgroundCancel = cancel
	h.bgMu.Unlock()
	h.logger.Println("client sent initialized notification")

	// Register file watchers for .java files so we detect branch switches etc.
	h.registerFileWatchers()

	// Initialize SemanticDB index.
	sourceRoot := uri.ToPath(h.rootURI)
	h.idx = index.NewIndex(h.logger, sourceRoot)

	// Discover JDK source for goto definition fallback (initial detection).
	setupHelper := setup.NewSetup(h.logger, sourceRoot)
	if jdkSrc := setupHelper.DiscoverJDKSource(""); jdkSrc != "" {
		h.logger.Printf("Initially discovered JDK source: %s", jdkSrc)
		h.idx.SetJdkSourceRoot(jdkSrc)
	}

	// Initial scan of existing .semanticdb files.
	h.reindex()
	h.idx.LogStatsSnapshot("after initial workspace index")

	// Decide if we need a full build.
	needsFullBuild := !h.idx.HasFiles()

	if !needsFullBuild {
		// Index already has data from existing .semanticdb files; signal ready immediately.
		h.closeIndexReady()
	}

	// Full setup + compile in background if needed.
	go func() {
		if needsFullBuild {
			defer h.closeIndexReady()
		}
		prog := h.beginProgress(ctx, "decaf", "initializing…")

		if needsFullBuild {
			h.logger.Println("No indexed files found, starting full setup and compilation...")

			// Step 1: Auto-setup.
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

		// Step 2: Connect to Bloop.
		prog.report("connecting to Bloop…", intPtr(30))
		if err := h.bspClient.Start(ctx, h.rootURI); err != nil {
			h.showMessage(MessageTypeError, fmt.Sprintf("decaf: failed to start Bloop: %v", err))
			prog.end("failed to connect")
			return
		}

		if ctx.Err() != nil {
			prog.end("cancelled")
			return
		}

		// Step 2.5: Fetch dependency sources and JVM environment.
		prog.report("fetching dependencies…", intPtr(50))
		if items, err := h.bspClient.DependencySources(ctx); err == nil {
			for _, item := range items {
				for _, src := range item.Sources {
					if strings.HasSuffix(src, ".jar") {
						h.idx.AddDependencySource(uri.ToPath(src))
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
		if envs, err := h.bspClient.JvmRunEnvironment(ctx); err == nil && len(envs) > 0 {
			for _, env := range envs {
				if env.JavaHome != "" {
					discoveredJavaHome = uri.ToPath(env.JavaHome)
					if jdkSrc := setupHelper.DiscoverJDKSource(discoveredJavaHome); jdkSrc != "" {
						h.logger.Printf("Refined JDK source from BSP: %s", jdkSrc)
						h.idx.SetJdkSourceRoot(jdkSrc)
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

		// Collect JDK jmod files for indexing (JDK 9+).
		classpathJARs = append(classpathJARs, discoverJDKJmods(h.logger, discoveredJavaHome)...)

		if ctx.Err() != nil {
			prog.end("cancelled")
			return
		}

		h.idx.LogStatsSnapshot("before dependency indexing")

		// Index classpath JARs for third-party dependency completion/hover.
		if len(classpathJARs) > 0 {
			prog.report("indexing dependencies…", intPtr(55))
			h.idx.IndexClasspathJARs(classpathJARs)
		}
		h.idx.LogStatsSnapshot("after dependency indexing")

		if needsFullBuild {
			// Step 3: Full Compile.
			prog.report("compiling…", intPtr(70))
			if err := h.bspClient.Compile(ctx); err != nil {
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
			h.reindex()
			h.idx.LogStatsSnapshot("after compile + reindex")
		} else {
			h.logger.Println("Existing index found, skipping initial full compilation.")
		}

		prog.end("ready")
	}()

	return nil, nil
}

func (h *Handler) handleShutdown(ctx context.Context, _ json.RawMessage) (any, error) {
	h.shutdown.Store(true)
	h.logger.Println("shutdown requested")
	h.bgMu.Lock()
	if h.backgroundCancel != nil {
		h.backgroundCancel()
	}
	h.bgMu.Unlock()
	if h.idx != nil {
		h.idx.Close()
	}
	if err := h.bspClient.Shutdown(ctx); err != nil {
		h.logger.Printf("bloop shutdown error: %v", err)
	}
	return nil, nil
}

func (h *Handler) handleExit(_ context.Context, _ json.RawMessage) (any, error) {
	h.logger.Println("exit notification received")
	close(h.exitCh)
	return nil, jsonrpc.ErrExit
}

func (h *Handler) registerFileWatchers() {
	registration := map[string]any{
		"registrations": []map[string]any{
			{
				"id":     "decaf-file-watcher",
				"method": "workspace/didChangeWatchedFiles",
				"registerOptions": DidChangeWatchedFilesRegistrationOptions{
					Watchers: []FileSystemWatcher{
						{
							GlobPattern: "**/*.java",
							Kind:        WatchKindCreate | WatchKindChange | WatchKindDelete,
						},
					},
				},
			},
		},
	}

	req, err := jsonrpc.NewRequestWithID(
		fmt.Sprintf("%d", progressSeq.Add(1)),
		"client/registerCapability",
		registration,
	)
	if err != nil {
		h.logger.Printf("failed to create registerCapability request: %v", err)
		return
	}
	if err := h.transport.WriteRequest(req); err != nil {
		h.logger.Printf("failed to send registerCapability: %v", err)
		return
	}
	h.logger.Println("registered file watcher for **/*.java")
}

// discoverJDKJmods finds JDK jmod files (JDK 9+) in the given JAVA_HOME.
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
