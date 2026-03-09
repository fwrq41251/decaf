package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/fwrq41251/decaf/internal/bsp"
	"github.com/fwrq41251/decaf/internal/index"
	"github.com/fwrq41251/decaf/internal/jsonrpc"
	"github.com/fwrq41251/decaf/internal/setup"
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
}

// NewHandler creates a new LSP handler.
func NewHandler(logger *log.Logger, transport *jsonrpc.Transport) *Handler {
	h := &Handler{
		logger:    logger,
		exitCh:    make(chan struct{}),
		transport: transport,
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
	d.Register("initialize", h.handleInitialize)
	d.Register("initialized", h.handleInitialized)
	d.Register("shutdown", h.handleShutdown)
	d.Register("exit", h.handleExit)
	d.Register("textDocument/didOpen", h.handleDidOpen)
	d.Register("textDocument/didSave", h.handleDidSave)
	d.Register("textDocument/didClose", h.handleDidClose)
	d.Register("textDocument/definition", h.handleDefinition)
	d.Register("textDocument/references", h.handleReferences)
	d.Register("textDocument/hover", h.handleHover)
	d.Register("textDocument/documentSymbol", h.handleDocumentSymbol)
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
				Change:    SyncFull,
				Save:      &SaveOptions{IncludeText: false},
			},
			DefinitionProvider:     true,
			ReferencesProvider:     true,
			HoverProvider:          true,
			DocumentSymbolProvider: true,
		},
		ServerInfo: &ServerInfo{
			Name:    "decaf",
			Version: "0.0.1",
		},
	}, nil
}

func (h *Handler) handleInitialized(ctx context.Context, _ json.RawMessage) (any, error) {
	h.initialized = true
	h.logger.Println("client sent initialized notification")

	// Initialize SemanticDB index.
	sourceRoot := strings.TrimPrefix(h.rootURI, "file://")
	h.idx = index.NewIndex(h.logger, sourceRoot)

	// Full setup + compile in background.
	go func() {
		// Step 1: Auto-setup.
		h.showMessage(MessageTypeInfo, "decaf: setting up project...")
		s := setup.NewSetup(h.logger, sourceRoot)
		if err := s.Run(ctx); err != nil {
			h.logger.Printf("auto-setup failed: %v", err)
		}

		// Step 2: Connect to Bloop.
		h.showMessage(MessageTypeInfo, "decaf: connecting to Bloop...")
		if err := h.bspClient.Start(ctx, h.rootURI); err != nil {
			h.showMessage(MessageTypeError, fmt.Sprintf("decaf: failed to start Bloop: %v", err))
			return
		}

		// Step 3: Compile.
		h.showMessage(MessageTypeInfo, "decaf: compiling...")
		if err := h.bspClient.Compile(ctx); err != nil {
			h.showMessage(MessageTypeWarning, fmt.Sprintf("decaf: compilation failed: %v", err))
		}

		// Step 4: Index.
		h.reindex()
		h.showMessage(MessageTypeInfo, "decaf: ready")
	}()

	return nil, nil
}

func (h *Handler) handleShutdown(ctx context.Context, _ json.RawMessage) (any, error) {
	h.shutdown = true
	h.logger.Println("shutdown requested")
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
	h.logger.Printf("didOpen: %s", p.TextDocument.URI)
	return nil, nil
}

func (h *Handler) handleDidSave(ctx context.Context, params json.RawMessage) (any, error) {
	var p DidSaveTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	h.logger.Printf("didSave: %s — triggering compile", p.TextDocument.URI)

	go func() {
		if err := h.bspClient.Compile(ctx); err != nil {
			h.logger.Printf("compile on save failed: %v", err)
			return
		}
		h.reindex()
	}()

	return nil, nil
}

func (h *Handler) handleDidClose(_ context.Context, params json.RawMessage) (any, error) {
	var p DidCloseTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	h.logger.Printf("didClose: %s", p.TextDocument.URI)
	return nil, nil
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
	locations := make([]LSPLocation, 0, len(refs))
	for _, r := range refs {
		if r.Range == nil {
			continue
		}
		locations = append(locations, LSPLocation{
			URI: h.toFileURI(r.URI),
			Range: Range{
				Start: Position{Line: int(r.Range.StartLine), Character: int(r.Range.StartCharacter)},
				End:   Position{Line: int(r.Range.EndLine), Character: int(r.Range.EndCharacter)},
			},
		})
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
	if strings.HasPrefix(relURI, "file://") {
		return relURI
	}
	sourceRoot := strings.TrimPrefix(h.rootURI, "file://")
	return "file://" + filepath.Join(sourceRoot, relURI)
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
