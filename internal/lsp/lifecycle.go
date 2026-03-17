package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	h.logger.Printf("initialize: rootURI=%s, processID=%v", p.RootURI, p.ProcessID)

	return InitializeResult{
		Capabilities: ServerCapabilities{
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
			CodeActionProvider:        &CodeActionOptions{CodeActionKinds: []string{CodeActionSourceOrganizeImports, CodeActionQuickFix}},
		},
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

	// Decide if we need a full build.
	needsFullBuild := !h.idx.HasFiles()

	// Full setup + compile in background if needed.
	go func() {
		prog := h.beginProgress("decaf", "initializing…")

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

		if envs, err := h.bspClient.JvmRunEnvironment(ctx); err == nil && len(envs) > 0 {
			for _, env := range envs {
				if env.JavaHome != "" {
					javaHome := uri.ToPath(env.JavaHome)
					if jdkSrc := setupHelper.DiscoverJDKSource(javaHome); jdkSrc != "" {
						h.logger.Printf("Refined JDK source from BSP: %s", jdkSrc)
						h.idx.SetJdkSourceRoot(jdkSrc)
						break
					}
				}
			}
		}

		if ctx.Err() != nil {
			prog.end("cancelled")
			return
		}

		if needsFullBuild {
			// Step 3: Full Compile.
			prog.report("compiling…", intPtr(70))
			if err := h.bspClient.Compile(ctx); err != nil {
				h.showMessage(MessageTypeWarning, fmt.Sprintf("decaf: compilation failed: %v", err))
			}
			if ctx.Err() != nil {
				prog.end("cancelled")
				return
			}
			prog.report("indexing…", intPtr(90))
			h.reindex()
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
