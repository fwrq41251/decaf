package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/fwrq41251/decaf/internal/bsp"
	"github.com/fwrq41251/decaf/internal/index"
	"github.com/fwrq41251/decaf/internal/jsonrpc"
	"github.com/fwrq41251/decaf/internal/setup"
	"github.com/fwrq41251/decaf/internal/uri"
)

// Handler holds the LSP handler state and methods.
type Handler struct {
	logger      *log.Logger
	initialized bool
	shutdown    bool
	exitCh      chan struct{}
	rootURI     string
	bspClient   *bsp.Client
	transport   *jsonrpc.Transport
	idx         *index.Index

	// docs stores in-memory overlay of open document contents.
	docs *docStore

	// debounceMu protects debounceTimer.
	debounceMu    sync.Mutex
	debounceTimer *time.Timer
	// backgroundCtx is used for background operations (compile, reindex).
	backgroundCtx context.Context
}

// NewHandler creates a new LSP handler.
func NewHandler(logger *log.Logger, transport *jsonrpc.Transport) *Handler {
	h := &Handler{
		logger:    logger,
		exitCh:    make(chan struct{}),
		transport: transport,
		docs:      newDocStore(),
	}
	h.bspClient = bsp.NewClient(logger, h.handleBSPDiagnostics)
	return h
}

// ExitCh returns a channel that is closed when the "exit" notification is received.
func (h *Handler) ExitCh() <-chan struct{} {
	return h.exitCh
}

// RegisterAll registers all LSP handlers on the dispatcher.
func (h *Handler) RegisterAll(d *jsonrpc.Dispatcher) {
	// Lifecycle — must run sequentially.
	d.Register("initialize", h.handleInitialize)
	d.Register("initialized", h.handleInitialized)
	d.Register("shutdown", h.handleShutdown)
	d.Register("exit", h.handleExit)

	// Notifications — sequential.
	d.Register("textDocument/didOpen", h.handleDidOpen)
	d.Register("textDocument/didChange", h.handleDidChange)
	d.Register("textDocument/didSave", h.handleDidSave)
	d.Register("textDocument/didClose", h.handleDidClose)
	d.Register("workspace/didChangeWatchedFiles", h.handleDidChangeWatchedFiles)

	// Read-only requests — safe to run concurrently.
	d.RegisterConcurrent("textDocument/definition", h.handleDefinition)
	d.RegisterConcurrent("textDocument/references", h.handleReferences)
	d.RegisterConcurrent("textDocument/hover", h.handleHover)
	d.RegisterConcurrent("textDocument/documentSymbol", h.handleDocumentSymbol)
	d.RegisterConcurrent("textDocument/documentHighlight", h.handleDocumentHighlight)
	d.RegisterConcurrent("textDocument/implementation", h.handleImplementation)
	d.RegisterConcurrent("workspace/symbol", h.handleWorkspaceSymbol)
	d.RegisterConcurrent("textDocument/completion", h.handleCompletion)
	d.RegisterConcurrent("textDocument/signatureHelp", h.handleSignatureHelp)
	d.RegisterConcurrent("textDocument/prepareRename", h.handlePrepareRename)

	// Code actions — concurrent (read-only analysis).
	d.RegisterConcurrent("textDocument/codeAction", h.handleCodeAction)

	// Rename mutates state — sequential.
	d.Register("textDocument/rename", h.handleRename)
}

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
			CodeActionProvider:        &CodeActionOptions{CodeActionKinds: []string{CodeActionSourceOrganizeImports}},
		},
		ServerInfo: &ServerInfo{
			Name:    "decaf",
			Version: "0.0.1",
		},
	}, nil
}

func (h *Handler) handleInitialized(ctx context.Context, _ json.RawMessage) (any, error) {
	h.initialized = true
	h.backgroundCtx = ctx
	h.logger.Println("client sent initialized notification")

	// Register file watchers for .java files so we detect branch switches etc.
	h.registerFileWatchers()

	// Initialize SemanticDB index.
	sourceRoot := uri.ToPath(h.rootURI)
	h.idx = index.NewIndex(h.logger, sourceRoot)

	// Discover JDK source for goto definition fallback (initial detection).
	s := setup.NewSetup(h.logger, sourceRoot)
	if jdkSrc := s.DiscoverJDKSource(""); jdkSrc != "" {
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
			s := setup.NewSetup(h.logger, sourceRoot)
			if err := s.Run(ctx); err != nil {
				h.logger.Printf("auto-setup failed: %v", err)
			}
		}

		// Step 2: Connect to Bloop.
		prog.report("connecting to Bloop…", intPtr(30))
		if err := h.bspClient.Start(ctx, h.rootURI); err != nil {
			h.showMessage(MessageTypeError, fmt.Sprintf("decaf: failed to start Bloop: %v", err))
			prog.end("failed to connect")
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

		if envs, err := h.bspClient.JvmRunEnvironment(ctx); err == nil && len(envs) > 0 {
			for _, env := range envs {
				if env.JavaHome != "" {
					javaHome := uri.ToPath(env.JavaHome)
					if jdkSrc := s.DiscoverJDKSource(javaHome); jdkSrc != "" {
						h.logger.Printf("Refined JDK source from BSP: %s", jdkSrc)
						h.idx.SetJdkSourceRoot(jdkSrc)
						break
					}
				}
			}
		}

		if needsFullBuild {
			// Step 3: Full Compile.
			prog.report("compiling…", intPtr(70))
			if err := h.bspClient.Compile(ctx); err != nil {
				h.showMessage(MessageTypeWarning, fmt.Sprintf("decaf: compilation failed: %v", err))
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
	h.shutdown = true
	h.logger.Println("shutdown requested")
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

func (h *Handler) handleDidOpen(_ context.Context, params json.RawMessage) (any, error) {
	var p DidOpenTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	h.docs.Open(p.TextDocument.URI, p.TextDocument.Text)
	h.logger.Printf("didOpen: %s (version %d, %d bytes)", p.TextDocument.URI, p.TextDocument.Version, len(p.TextDocument.Text))
	return nil, nil
}

func (h *Handler) handleDidChange(_ context.Context, params json.RawMessage) (any, error) {
	var p DidChangeTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	h.docs.ApplyChanges(p.TextDocument.URI, p.ContentChanges)
	h.logger.Printf("didChange: %s (version %d, %d changes)", p.TextDocument.URI, p.TextDocument.Version, len(p.ContentChanges))
	return nil, nil
}

func (h *Handler) handleDidSave(ctx context.Context, params json.RawMessage) (any, error) {
	var p DidSaveTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	h.logger.Printf("didSave: %s — triggering compile", p.TextDocument.URI)

	go func() {
		prog := h.beginProgress("decaf", "compiling…")
		if err := h.bspClient.Compile(ctx); err != nil {
			h.logger.Printf("compile on save failed: %v", err)
			prog.end("compilation failed")
			return
		}
		prog.report("indexing…", nil)
		h.reindex()
		prog.end("done")
	}()

	return nil, nil
}

func (h *Handler) handleDidClose(_ context.Context, params json.RawMessage) (any, error) {
	var p DidCloseTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	h.docs.Close(p.TextDocument.URI)
	h.logger.Printf("didClose: %s", p.TextDocument.URI)
	return nil, nil
}

func (h *Handler) handleDidChangeWatchedFiles(_ context.Context, params json.RawMessage) (any, error) {
	var p DidChangeWatchedFilesParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	javaChanged := 0
	for _, e := range p.Changes {
		if strings.HasSuffix(e.URI, ".java") {
			javaChanged++
		}
	}

	if javaChanged == 0 {
		return nil, nil
	}

	h.logger.Printf("watched files changed: %d java file(s)", javaChanged)
	h.scheduleCompile()
	return nil, nil
}

// scheduleCompile debounces compilation — waits 500ms after the last call
// before triggering a compile + reindex cycle.
func (h *Handler) scheduleCompile() {
	h.debounceMu.Lock()
	defer h.debounceMu.Unlock()

	if h.debounceTimer != nil {
		h.debounceTimer.Stop()
	}

	h.debounceTimer = time.AfterFunc(500*time.Millisecond, func() {
		ctx := h.backgroundCtx
		if ctx == nil {
			return
		}
		prog := h.beginProgress("decaf", "compiling…")
		if err := h.bspClient.Compile(ctx); err != nil {
			h.logger.Printf("compile on file change failed: %v", err)
			prog.end("compilation failed")
			return
		}
		prog.report("indexing…", nil)
		h.reindex()
		prog.end("done")
	})
}

// registerFileWatchers dynamically registers file watchers with the client.
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

func (h *Handler) handleDefinition(_ context.Context, params json.RawMessage) (any, error) {
	var p TextDocumentPositionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return []LSPLocation{}, nil
	}

	defs := h.idx.Definition(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	locations := make([]LSPLocation, 0, len(defs))
	for _, d := range defs {
		if d.Range == nil {
			continue
		}
		locations = append(locations, LSPLocation{
			URI: h.toFileURI(d.URI),
			Range: Range{
				Start: Position{Line: int(d.Range.StartLine), Character: int(d.Range.StartCharacter)},
				End:   Position{Line: int(d.Range.EndLine), Character: int(d.Range.EndCharacter)},
			},
		})
	}

	h.logger.Printf("definition at %s:%d:%d -> %d results",
		p.TextDocument.URI, p.Position.Line, p.Position.Character, len(locations))
	return locations, nil
}

func (h *Handler) handleReferences(_ context.Context, params json.RawMessage) (any, error) {
	var p ReferenceParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return []LSPLocation{}, nil
	}

	refs := h.idx.References(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	
	// If includeDeclaration is true, add the definition to the results.
	if p.Context.IncludeDeclaration {
		defs := h.idx.Definition(p.TextDocument.URI, p.Position.Line, p.Position.Character)
		for _, d := range defs {
			if d.Range == nil {
				continue
			}
			// Convert Symbol to Occurrence-like structure for the loop below.
			refs = append(refs, index.Occurrence{
				URI:   d.URI,
				Range: d.Range,
			})
		}
		// Re-deduplicate since definition might already be in references or multiple files.
		// Note: We'd need to expose deduplicateOccurrences if we wanted to call it here, 
		// but the loop below already converts to LSPLocation, so we can just let the client 
		// handle it or deduplicate the final locations array.
	}

	locations := make([]LSPLocation, 0, len(refs))
	seen := make(map[string]bool)
	for _, r := range refs {
		if r.Range == nil {
			continue
		}
		loc := LSPLocation{
			URI: h.toFileURI(r.URI),
			Range: Range{
				Start: Position{Line: int(r.Range.StartLine), Character: int(r.Range.StartCharacter)},
				End:   Position{Line: int(r.Range.EndLine), Character: int(r.Range.EndCharacter)},
			},
		}
		// Final deduplication of LSP locations
		key := fmt.Sprintf("%s:%d:%d-%d:%d", loc.URI, loc.Range.Start.Line, loc.Range.Start.Character, loc.Range.End.Line, loc.Range.End.Character)
		if !seen[key] {
			seen[key] = true
			locations = append(locations, loc)
		}
	}

	h.logger.Printf("references at %s:%d:%d -> %d results",
		p.TextDocument.URI, p.Position.Line, p.Position.Character, len(locations))
	return locations, nil
}

func (h *Handler) handleHover(_ context.Context, params json.RawMessage) (any, error) {
	var p TextDocumentPositionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return nil, nil
	}

	sym := h.idx.Hover(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	if sym == nil {
		return nil, nil
	}

	content := formatHover(sym)
	result := HoverResult{
		Contents: MarkupContent{
			Kind:  "markdown",
			Value: content,
		},
	}

	return result, nil
}

func (h *Handler) handleDocumentSymbol(_ context.Context, params json.RawMessage) (any, error) {
	var p struct {
		TextDocument TextDocumentIdentifier `json:"textDocument"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return []DocumentSymbol{}, nil
	}

	symbols := h.idx.FileSymbols(p.TextDocument.URI)
	result := buildDocumentSymbols(symbols)
	return result, nil
}

func (h *Handler) handleDocumentHighlight(_ context.Context, params json.RawMessage) (any, error) {
	var p TextDocumentPositionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return []DocumentHighlight{}, nil
	}

	occs := h.idx.FileOccurrencesOf(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	highlights := make([]DocumentHighlight, 0, len(occs))
	for _, occ := range occs {
		if occ.Range == nil {
			continue
		}
		highlights = append(highlights, DocumentHighlight{
			Range: sdbRangeToLSP(occ.Range),
			Kind:  HighlightText,
		})
	}

	return highlights, nil
}

func (h *Handler) handleImplementation(_ context.Context, params json.RawMessage) (any, error) {
	var p TextDocumentPositionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return []LSPLocation{}, nil
	}

	impls := h.idx.Implementations(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	locations := make([]LSPLocation, 0, len(impls))
	for _, d := range impls {
		if d.Range == nil {
			continue
		}
		locations = append(locations, LSPLocation{
			URI:   h.toFileURI(d.URI),
			Range: sdbRangeToLSP(d.Range),
		})
	}

	return locations, nil
}

func (h *Handler) handleWorkspaceSymbol(_ context.Context, params json.RawMessage) (any, error) {
	var p WorkspaceSymbolParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil || p.Query == "" {
		return []SymbolInformation{}, nil
	}

	symbols := h.idx.SearchSymbols(p.Query)
	result := make([]SymbolInformation, 0, len(symbols))
	for _, s := range symbols {
		if s.Range == nil {
			continue
		}
		result = append(result, SymbolInformation{
			Name: s.Name,
			Kind: sdbKindToLSP(s.Kind),
			Location: LSPLocation{
				URI:   h.toFileURI(s.URI),
				Range: sdbRangeToLSP(s.Range),
			},
		})
	}

	return result, nil
}

func (h *Handler) handleCompletion(_ context.Context, params json.RawMessage) (any, error) {
	var p CompletionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return CompletionList{}, nil
	}

	// Extract the word prefix at the cursor position from the symbol index.
	// We use a simple approach: find the symbol at cursor, or search by empty prefix.
	prefix := ""
	if p.Context != nil && p.Context.TriggerCharacter == "." {
		prefix = ""
	}

	symbols := h.idx.CompletionSymbols(p.TextDocument.URI, prefix)
	items := make([]CompletionItem, 0, len(symbols))
	for _, s := range symbols {
		item := CompletionItem{
			Label:      s.Name,
			Kind:       sdbKindToCompletionKind(s.Kind),
			InsertText: s.Name,
		}
		if s.Signature != nil {
			item.Detail = s.Signature.Label
		}
		items = append(items, item)
	}

	h.logger.Printf("completion at %s:%d:%d -> %d items",
		p.TextDocument.URI, p.Position.Line, p.Position.Character, len(items))
	return CompletionList{IsIncomplete: true, Items: items}, nil
}

func (h *Handler) handleSignatureHelp(_ context.Context, params json.RawMessage) (any, error) {
	var p SignatureHelpParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return nil, nil
	}

	sym := h.idx.SymbolSignature(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	if sym == nil || sym.Signature == nil {
		return nil, nil
	}

	sigInfo := formatSignatureHelp(sym)
	if sigInfo == nil {
		return nil, nil
	}

	return SignatureHelp{
		Signatures:      []SignatureInformation{*sigInfo},
		ActiveSignature: 0,
		ActiveParameter: 0,
	}, nil
}

func (h *Handler) handleRename(_ context.Context, params json.RawMessage) (any, error) {
	var p RenameParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return nil, nil
	}

	_, occs := h.idx.RenameOccurrences(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	if len(occs) == 0 {
		return nil, nil
	}

	changes := make(map[string][]TextEdit)
	for _, occ := range occs {
		if occ.Range == nil {
			continue
		}
		uri := h.toFileURI(occ.URI)
		changes[uri] = append(changes[uri], TextEdit{
			Range:   sdbRangeToLSP(occ.Range),
			NewText: p.NewName,
		})
	}

	h.logger.Printf("rename at %s:%d:%d -> %d files affected",
		p.TextDocument.URI, p.Position.Line, p.Position.Character, len(changes))
	return WorkspaceEdit{Changes: changes}, nil
}

func (h *Handler) handleCodeAction(_ context.Context, params json.RawMessage) (any, error) {
	var p CodeActionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return []CodeAction{}, nil
	}

	// Only respond when the client requests source.organizeImports (or requests all).
	wantOrganize := len(p.Context.Only) == 0
	for _, kind := range p.Context.Only {
		if kind == CodeActionSourceOrganizeImports || kind == "source" {
			wantOrganize = true
			break
		}
	}
	if !wantOrganize {
		return []CodeAction{}, nil
	}

	overlay, _ := h.docs.Get(p.TextDocument.URI)
	edit := organizeImports(p.TextDocument.URI, h.idx, overlay)
	if edit == nil {
		return []CodeAction{}, nil
	}

	return []CodeAction{
		{
			Title: "Organize Imports",
			Kind:  CodeActionSourceOrganizeImports,
			Edit:  edit,
		},
	}, nil
}

func (h *Handler) handlePrepareRename(_ context.Context, params json.RawMessage) (any, error) {
	var p PrepareRenameParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if h.idx == nil {
		return nil, nil
	}

	sym := h.idx.Hover(p.TextDocument.URI, p.Position.Line, p.Position.Character)
	if sym == nil || sym.Range == nil {
		return nil, nil
	}

	return map[string]any{
		"range":       sdbRangeToLSP(sym.Range),
		"placeholder": sym.Name,
	}, nil
}

// showMessage sends a window/showMessage notification to the editor.
func (h *Handler) showMessage(msgType int, message string) {
	notification, err := jsonrpc.NewNotification("window/showMessage", ShowMessageParams{
		Type:    msgType,
		Message: message,
	})
	if err != nil {
		h.logger.Printf("failed to create showMessage notification: %v", err)
		return
	}
	if err := h.transport.WriteRequest(notification); err != nil {
		h.logger.Printf("failed to send showMessage: %v", err)
	}
}

func intPtr(n int) *int { return &n }

func (h *Handler) reindex() {
	if h.idx == nil {
		return
	}
	if err := h.idx.Load(); err != nil {
		h.logger.Printf("reindex failed: %v", err)
	}
}

// toFileURI converts a SemanticDB relative URI to an absolute file:// URI.
func (h *Handler) toFileURI(relURI string) string {
	if uri.IsURI(relURI) {
		return relURI
	}
	return uri.Join(h.rootURI, relURI)
}

// handleBSPDiagnostics converts BSP diagnostics to LSP diagnostics and publishes them.
func (h *Handler) handleBSPDiagnostics(bspDiag bsp.PublishDiagnosticsParams) {
	lspDiags := make([]Diagnostic, 0, len(bspDiag.Diagnostics))
	for _, d := range bspDiag.Diagnostics {
		lspDiags = append(lspDiags, Diagnostic{
			Range: Range{
				Start: Position{Line: d.Range.Start.Line, Character: d.Range.Start.Character},
				End:   Position{Line: d.Range.End.Line, Character: d.Range.End.Character},
			},
			Severity: d.Severity,
			Message:  d.Message,
			Source:   "bloop",
		})
	}

	params := PublishDiagnosticsParams{
		URI:         bspDiag.TextDocument.URI,
		Diagnostics: lspDiags,
	}

	notification, err := jsonrpc.NewNotification("textDocument/publishDiagnostics", params)
	if err != nil {
		h.logger.Printf("failed to create diagnostics notification: %v", err)
		return
	}

	if err := h.transport.WriteRequest(notification); err != nil {
		h.logger.Printf("failed to send diagnostics: %v", err)
	}
}
