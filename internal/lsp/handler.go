package lsp

import (
	"context"
	"errors"
	"fmt"
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
	dispatcher  *jsonrpc.Dispatcher
	idx         *index.Index

	// docs stores in-memory overlay of open document contents.
	docs *docStore

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

	// diagnosticsMu protects diagnostics map.
	diagnosticsMu sync.Mutex
	// diagnostics stores current diagnostics per URI and build target.
	// URI -> TargetURI -> []Diagnostic
	diagnostics map[string]map[string][]Diagnostic

	// indexReady is closed once the initial index load (and full build if needed) completes.
	indexReady chan struct{}

	// tasksMu protects activeTasks map.
	tasksMu     sync.Mutex
	activeTasks map[string]*progress

	// clientCaps stores the client capabilities received during initialization.
	clientCaps ClientCapabilities
}

// NewHandler creates a new LSP handler.
func NewHandler(logger *log.Logger, transport *jsonrpc.Transport) *Handler {
	h := &Handler{
		logger:      logger,
		exitCh:      make(chan struct{}),
		transport:   transport,
		docs:        newDocStore(),
		diagnostics: make(map[string]map[string][]Diagnostic),
		indexReady:  make(chan struct{}),
		activeTasks: make(map[string]*progress),
	}
	h.bspClient = bsp.NewClient(logger, h.handleBSPDiagnostics, func() {
		if !h.shutdown.Load() {
			h.showMessage(MessageTypeError, "decaf: Bloop build server disconnected. Please restart your editor.")
		}
	})
	h.bspClient.SetHandlers(h.handleBSPLogMessage, h.handleBSPTaskStart, h.handleBSPTaskProgress, h.handleBSPTaskFinish)
	return h
}

// ExitCh returns a channel that is closed when the "exit" notification is received.
func (h *Handler) ExitCh() <-chan struct{} {
	return h.exitCh
}

// Close cleans up resources (Bloop process, index) if they haven't been
// cleaned up already by a normal shutdown/exit sequence.
func (h *Handler) Close(ctx context.Context) {
	if h.shutdown.Load() {
		return
	}
	h.shutdown.Store(true)
	h.bgMu.Lock()
	if h.backgroundCancel != nil {
		h.backgroundCancel()
	}
	h.bgMu.Unlock()
	if h.idx != nil {
		h.idx.Close()
	}
	if err := h.bspClient.Shutdown(ctx); err != nil {
		h.logger.Printf("bloop shutdown error during cleanup: %v", err)
	}
}

// RegisterAll registers all LSP handlers on the dispatcher.
func (h *Handler) RegisterAll(d *jsonrpc.Dispatcher) {
	h.dispatcher = d
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

// waitIndexReady blocks until the initial index is ready or the context is cancelled.
func (h *Handler) waitIndexReady(ctx context.Context) bool {
	select {
	case <-h.indexReady:
		return true
	case <-ctx.Done():
		return false
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
	docURI := h.toFileURI(bspDiag.TextDocument.URI)
	targetURI := bspDiag.BuildTarget.URI

	h.diagnosticsMu.Lock()
	if h.diagnostics[docURI] == nil {
		h.diagnostics[docURI] = make(map[string][]Diagnostic)
	}

	if bspDiag.Reset {
		h.diagnostics[docURI][targetURI] = []Diagnostic{}
	}

	for _, d := range bspDiag.Diagnostics {
		h.diagnostics[docURI][targetURI] = append(h.diagnostics[docURI][targetURI], Diagnostic{
			Range: Range{
				Start: Position{Line: d.Range.Start.Line, Character: d.Range.Start.Character},
				End:   Position{Line: d.Range.End.Line, Character: d.Range.End.Character},
			},
			Severity: d.Severity,
			Message:  d.Message,
			Source:   "bloop",
		})
	}

	// Merge all targets for this document to send a complete view to the editor.
	merged := []Diagnostic{}
	seen := make(map[string]bool)
	for _, targetDiags := range h.diagnostics[docURI] {
		for _, d := range targetDiags {
			// Basic deduplication.
			key := fmt.Sprintf("%d:%d-%d:%d:%d:%s",
				d.Range.Start.Line, d.Range.Start.Character,
				d.Range.End.Line, d.Range.End.Character,
				d.Severity, d.Message)
			if !seen[key] {
				merged = append(merged, d)
				seen[key] = true
			}
		}
	}
	h.diagnosticsMu.Unlock()

	params := PublishDiagnosticsParams{
		URI:         docURI,
		Diagnostics: merged,
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

// clearDiagnostics removes stored diagnostics for a URI and publishes an empty
// diagnostic list to the client so stale markers are cleared.
func (h *Handler) clearDiagnostics(uri string) {
	h.diagnosticsMu.Lock()
	_, exists := h.diagnostics[uri]
	delete(h.diagnostics, uri)
	h.diagnosticsMu.Unlock()

	if !exists {
		return
	}

	notification, err := jsonrpc.NewNotification("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: []Diagnostic{},
	})
	if err != nil {
		return
	}
	_ = h.transport.WriteRequest(notification)
}

func (h *Handler) handleBSPLogMessage(params bsp.LogMessageParams) {
	notification, err := jsonrpc.NewNotification("window/logMessage", LogMessageParams{
		Type:    int(params.Type),
		Message: params.Message,
	})
	if err != nil {
		return
	}
	_ = h.transport.WriteRequest(notification)
}

func (h *Handler) handleBSPTaskStart(params bsp.TaskStartParams) {
	// Title defaults to message or "bloop task"
	title := params.Message
	if title == "" {
		title = "bloop task"
	}

	h.bgMu.Lock()
	ctx := h.backgroundCtx
	h.bgMu.Unlock()
	if ctx == nil {
		return
	}

	// Create progress outside the lock to avoid holding it during network I/O.
	prog := h.beginProgress(ctx, "decaf", title)
	if prog != nil {
		h.tasksMu.Lock()
		h.activeTasks[params.TaskID.ID] = prog
		h.tasksMu.Unlock()
	}
}

func (h *Handler) handleBSPTaskProgress(params bsp.TaskProgressParams) {
	h.tasksMu.Lock()
	prog, ok := h.activeTasks[params.TaskID.ID]
	h.tasksMu.Unlock()

	if !ok {
		return
	}

	var pct *int
	if params.Total > 0 {
		p := int(params.Progress * 100 / params.Total)
		pct = &p
	}
	prog.report(params.Message, pct)
}

func (h *Handler) handleBSPTaskFinish(params bsp.TaskFinishParams) {
	h.tasksMu.Lock()
	prog, ok := h.activeTasks[params.TaskID.ID]
	delete(h.activeTasks, params.TaskID.ID)
	h.tasksMu.Unlock()

	if ok {
		msg := params.Message
		if msg == "" {
			if params.Status == bsp.StatusOK {
				msg = "done"
			} else {
				msg = "failed"
			}
		}
		prog.end(msg)
	}

	// If this was a compile task, trigger reindex.
	if params.DataKind == "compile-report" {
		h.logger.Printf("BSP compile task finished (%s), triggering reindex", params.TaskID.ID)
		h.reindex()
	}
}

func (h *Handler) logCompileError(msg string, err error) {
	var ce *bsp.CompileError
	if errors.As(err, &ce) {
		h.logger.Printf("%s: compilation failed (user code error)", msg)
	} else {
		h.logger.Printf("%s: infrastructure error: %v", msg, err)
	}
}
