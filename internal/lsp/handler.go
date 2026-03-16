package lsp

import (
	"context"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fwrq41251/decaf/internal/bsp"
	"github.com/fwrq41251/decaf/internal/index"
	"github.com/fwrq41251/decaf/internal/jsonrpc"
	"github.com/fwrq41251/decaf/internal/uri"
)

// Handler holds the LSP handler state and methods.
type Handler struct {
	logger      *log.Logger
	initialized atomic.Bool
	shutdown    atomic.Bool
	exitCh      chan struct{}
	rootURI     string
	bspClient   *bsp.Client
	transport   *jsonrpc.Transport
	idx         *index.Index

	// docs stores in-memory overlay of open document contents.
	docs *docStore

	// debounceMu protects debounceTimer and pendingURIs.
	debounceMu    sync.Mutex
	debounceTimer *time.Timer
	pendingURIs   []string
	// backgroundCtx is used for background operations (compile, reindex).
	backgroundCtx    context.Context
	backgroundCancel context.CancelFunc
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

// getFileContent returns the content of a file, preferring the in-memory overlay
// from docStore over reading from disk. Returns empty string on error.
func (h *Handler) getFileContent(fileURI string) string {
	if content, ok := h.docs.Get(fileURI); ok {
		return content
	}
	filePath := uri.ToPath(fileURI)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}
	return string(data)
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
