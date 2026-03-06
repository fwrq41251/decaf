package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
)

// ErrExit is returned by a handler to signal the dispatcher to stop the loop.
var ErrExit = errors.New("exit requested")

// Handler processes a JSON-RPC request and returns a result or an error.
// For notifications, the return value is ignored.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Dispatcher routes incoming JSON-RPC messages to registered handlers by method name.
type Dispatcher struct {
	handlers  map[string]Handler
	transport *Transport
	logger    *log.Logger
}

// NewDispatcher creates a new Dispatcher.
func NewDispatcher(transport *Transport, logger *log.Logger) *Dispatcher {
	return &Dispatcher{
		handlers:  make(map[string]Handler),
		transport: transport,
		logger:    logger,
	}
}

// Register registers a handler for the given method name.
func (d *Dispatcher) Register(method string, handler Handler) {
	d.handlers[method] = handler
}

// Run reads messages from the transport in a loop and dispatches them.
// It blocks until the context is cancelled or an I/O error occurs.
func (d *Dispatcher) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req, err := d.transport.Read()
		if err != nil {
			// If the context was cancelled (e.g., by exit), treat as normal shutdown.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("reading message: %w", err)
		}

		d.logger.Printf("received: method=%s notification=%v", req.Method, req.IsNotification())

		handler, ok := d.handlers[req.Method]
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

		// Handle the request.
		result, herr := handler(ctx, req.Params)
		if errors.Is(herr, ErrExit) {
			return nil
		}
		if req.IsNotification() {
			continue
		}

		var resp *Response
		if herr != nil {
			resp = NewErrorResponse(req.ID, CodeInternalError, herr.Error())
		} else {
			resp, err = NewResponse(req.ID, result)
			if err != nil {
				resp = NewErrorResponse(req.ID, CodeInternalError, err.Error())
			}
		}

		if err := d.transport.Write(resp); err != nil {
			return fmt.Errorf("writing response: %w", err)
		}
	}
}
