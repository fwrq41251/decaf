package server

import (
	"context"
	"errors"
	"io"
	"log"
	"os"

	"github.com/fwrq41251/decaf/internal/jsonrpc"
	"github.com/fwrq41251/decaf/internal/lsp"
)

// Server is the main LSP server that communicates with editors via JSON-RPC
// over stdin/stdout, and delegates build tasks to Bloop via BSP.
type Server struct {
	logger *log.Logger
}

func NewServer(logger *log.Logger) *Server {
	return &Server{logger: logger}
}

// Run starts the LSP server, reading JSON-RPC messages from stdin
// and writing responses to stdout.
func (s *Server) Run(ctx context.Context) error {
	s.logger.Println("decaf LSP server starting...")

	transport := jsonrpc.NewTransport(os.Stdin, os.Stdout)
	dispatcher := jsonrpc.NewDispatcher(transport, s.logger)

	handler := lsp.NewHandler(s.logger, transport)
	handler.RegisterAll(dispatcher)
	defer handler.Close(ctx)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Stop the dispatcher when exit is received.
	go func() {
		select {
		case <-handler.ExitCh():
			cancel()
		case <-ctx.Done():
		}
	}()

	err := dispatcher.Run(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) || ctx.Err() != nil {
			// Normal shutdown via stdin close or context cancellation (handler exit).
			s.logger.Println("decaf LSP server stopped")
			return nil
		}
		return err
	}
	return nil
}
