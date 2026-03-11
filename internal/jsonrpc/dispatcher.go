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
}

// NewDispatcher creates a new Dispatcher.
func NewDispatcher(transport *Transport, logger *log.Logger) *Dispatcher {
	return &Dispatcher{
		handlers:  make(map[string]handlerEntry),
		transport: transport,
		logger:    logger,
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
			d.wg.Add(1)
			go func(req *Request) {
				defer d.wg.Done()
				d.handleAndRespond(ctx, req, entry.handler)
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
	if err := d.sendResponse(req.ID, result, herr); err != nil {
		d.logger.Printf("failed to write response for %s: %v", req.Method, err)
	}
}

func (d *Dispatcher) sendResponse(id *json.RawMessage, result any, herr error) error {
	var resp *Response
	if herr != nil {
		resp = NewErrorResponse(id, CodeInternalError, herr.Error())
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
