package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
)

// ErrExit is returned by a handler to signal the dispatcher to stop the loop.
var ErrExit = errors.New("exit requested")

// Handler processes a JSON-RPC request and returns a result or an error.
// For notifications, the return value is ignored.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

type handlerEntry struct {
	handler    Handler
	concurrent bool
}

// Dispatcher routes incoming JSON-RPC messages to registered handlers by method name.
// Handlers registered with RegisterConcurrent are dispatched in separate goroutines,
// while others are handled sequentially in the main loop.
type Dispatcher struct {
	handlers  map[string]handlerEntry
	transport *Transport
	logger    *log.Logger
	wg        sync.WaitGroup

	// cancelMu protects inflight.
	cancelMu sync.Mutex
	// inflight maps numeric request IDs to their cancel functions.
	inflight map[int64]context.CancelFunc
}

// NewDispatcher creates a new Dispatcher.
func NewDispatcher(transport *Transport, logger *log.Logger) *Dispatcher {
	return &Dispatcher{
		handlers:  make(map[string]handlerEntry),
		transport: transport,
		logger:    logger,
		inflight:  make(map[int64]context.CancelFunc),
	}
}

// Register registers a handler that runs sequentially in the main loop.
func (d *Dispatcher) Register(method string, handler Handler) {
	d.handlers[method] = handlerEntry{handler: handler, concurrent: false}
}

// RegisterConcurrent registers a handler that may run concurrently.
func (d *Dispatcher) RegisterConcurrent(method string, handler Handler) {
	d.handlers[method] = handlerEntry{handler: handler, concurrent: true}
}

// Run reads messages from the transport in a loop and dispatches them.
// It blocks until the context is cancelled or an I/O error occurs.
func (d *Dispatcher) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			d.wg.Wait()
			return ctx.Err()
		default:
		}

		req, err := d.transport.Read()
		if err != nil {
			// If the context was cancelled (e.g., by exit), treat as normal shutdown.
			if ctx.Err() != nil {
				d.wg.Wait()
				return ctx.Err()
			}
			d.wg.Wait()
			return fmt.Errorf("reading message: %w", err)
		}

		d.logger.Printf("received: method=%s notification=%v", req.Method, req.IsNotification())

		// Handle $/cancelRequest by cancelling the in-flight request.
		if req.Method == "$/cancelRequest" {
			d.handleCancelRequest(req.Params)
			continue
		}

		entry, ok := d.handlers[req.Method]
		if !ok {
			d.logger.Printf("no handler for method %q", req.Method)
			if !req.IsNotification() {
				resp := NewErrorResponse(req.ID, CodeMethodNotFound, fmt.Sprintf("method %q not found", req.Method))
				if err := d.transport.Write(resp); err != nil {
					return fmt.Errorf("writing error response: %w", err)
				}
			}
			continue
		}

		if entry.concurrent && !req.IsNotification() {
			reqCtx, cancel := context.WithCancel(ctx)
			reqID := d.trackRequest(req.ID, cancel)

			d.wg.Add(1)
			go func(req *Request) {
				defer d.wg.Done()
				defer d.untrackRequest(reqID)
				d.handleAndRespond(reqCtx, req, entry.handler)
			}(req)
		} else {
			// Sequential: lifecycle methods and notifications.
			result, herr := entry.handler(ctx, req.Params)
			if errors.Is(herr, ErrExit) {
				d.wg.Wait()
				return nil
			}
			if req.IsNotification() {
				continue
			}
			if err := d.sendResponse(req.ID, result, herr); err != nil {
				return err
			}
		}
	}
}

func (d *Dispatcher) handleAndRespond(ctx context.Context, req *Request, handler Handler) {
	result, herr := handler(ctx, req.Params)

	// If the context was cancelled (via $/cancelRequest), respond with RequestCancelled.
	if ctx.Err() != nil && herr == nil {
		herr = ctx.Err()
	}

	if err := d.sendResponse(req.ID, result, herr); err != nil {
		d.logger.Printf("failed to write response for %s: %v", req.Method, err)
	}
}

func (d *Dispatcher) sendResponse(id *json.RawMessage, result any, herr error) error {
	var resp *Response
	if herr != nil {
		code := CodeInternalError
		if errors.Is(herr, context.Canceled) {
			code = CodeRequestCancelled
		}
		resp = NewErrorResponse(id, code, herr.Error())
	} else {
		var err error
		resp, err = NewResponse(id, result)
		if err != nil {
			resp = NewErrorResponse(id, CodeInternalError, err.Error())
		}
	}
	if err := d.transport.Write(resp); err != nil {
		return fmt.Errorf("writing response: %w", err)
	}
	return nil
}

// handleCancelRequest processes a $/cancelRequest notification.
func (d *Dispatcher) handleCancelRequest(params json.RawMessage) {
	var p struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		d.logger.Printf("malformed $/cancelRequest: %v", err)
		return
	}

	var id int64
	if err := json.Unmarshal(p.ID, &id); err != nil {
		d.logger.Printf("$/cancelRequest: non-numeric id, ignoring")
		return
	}

	d.cancelMu.Lock()
	cancel, ok := d.inflight[id]
	d.cancelMu.Unlock()

	if ok {
		d.logger.Printf("cancelling request %d", id)
		cancel()
	} else {
		d.logger.Printf("$/cancelRequest for unknown id %d (already completed?)", id)
	}
}

// trackRequest stores a cancel function for a request ID and returns the numeric ID.
func (d *Dispatcher) trackRequest(rawID *json.RawMessage, cancel context.CancelFunc) int64 {
	if rawID == nil {
		return -1
	}
	var id int64
	if err := json.Unmarshal(*rawID, &id); err != nil {
		return -1
	}
	d.cancelMu.Lock()
	d.inflight[id] = cancel
	d.cancelMu.Unlock()
	return id
}

// untrackRequest removes a completed request from the inflight map.
func (d *Dispatcher) untrackRequest(id int64) {
	if id < 0 {
		return
	}
	d.cancelMu.Lock()
	delete(d.inflight, id)
	d.cancelMu.Unlock()
}
