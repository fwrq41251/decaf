package bsp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/fwrq41251/decaf/internal/jsonrpc"
)

// DiagnosticsHandler is called when the build server publishes diagnostics.
type DiagnosticsHandler func(params PublishDiagnosticsParams)

// Client manages the connection to a BSP build server (Bloop).
type Client struct {
	logger     *log.Logger
	transport  *jsonrpc.Transport
	cmd        *exec.Cmd
	nextID     atomic.Int64
	pending    map[int64]chan *jsonrpc.Response
	pendingMu  sync.Mutex
	onDiagnostics DiagnosticsHandler
	targets    []BuildTarget
}

// NewClient creates a new BSP client.
func NewClient(logger *log.Logger, onDiagnostics DiagnosticsHandler) *Client {
	return &Client{
		logger:        logger,
		pending:       make(map[int64]chan *jsonrpc.Response),
		onDiagnostics: onDiagnostics,
	}
}

// Start launches the Bloop BSP server and performs the initialize handshake.
func (c *Client) Start(ctx context.Context, rootURI string) error {
	c.cmd = exec.CommandContext(ctx, "bloop", "bsp")
	stdin, err := c.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("starting bloop: %w", err)
	}
	c.logger.Println("bloop bsp process started")

	c.transport = jsonrpc.NewTransport(stdout, stdin)

	// Start reading responses/notifications from Bloop.
	go c.readLoop()

	// BSP handshake: build/initialize
	initParams := InitializeBuildParams{
		DisplayName: "decaf",
		Version:     "0.0.1",
		BSPVersion:  "2.1.1",
		RootURI:     rootURI,
		Capabilities: BuildClientCapabilities{
			LanguageIDs: []string{"java"},
		},
	}

	var initResult InitializeBuildResult
	if err := c.call(ctx, "build/initialize", initParams, &initResult); err != nil {
		return fmt.Errorf("build/initialize failed: %w", err)
	}
	c.logger.Printf("bloop initialized: %s %s (bsp %s)",
		initResult.DisplayName, initResult.Version, initResult.BSPVersion)

	// Send build/initialized notification.
	if err := c.notify("build/initialized"); err != nil {
		return fmt.Errorf("build/initialized failed: %w", err)
	}

	// Query build targets.
	var targetsResult WorkspaceBuildTargetsResult
	if err := c.call(ctx, "workspace/buildTargets", struct{}{}, &targetsResult); err != nil {
		return fmt.Errorf("workspace/buildTargets failed: %w", err)
	}
	c.targets = targetsResult.Targets
	c.logger.Printf("found %d build targets", len(c.targets))
	for _, t := range c.targets {
		c.logger.Printf("  target: %s (%s)", t.DisplayName, t.ID.URI)
	}

	return nil
}

// Compile triggers compilation of all known build targets.
func (c *Client) Compile(ctx context.Context) error {
	if len(c.targets) == 0 {
		c.logger.Println("no build targets to compile")
		return nil
	}

	ids := make([]BuildTargetIdentifier, 0, len(c.targets))
	for _, t := range c.targets {
		if t.Capabilities.CanCompile {
			ids = append(ids, t.ID)
		}
	}

	var result CompileResult
	if err := c.call(ctx, "buildTarget/compile", CompileParams{Targets: ids}, &result); err != nil {
		return fmt.Errorf("buildTarget/compile failed: %w", err)
	}
	c.logger.Printf("compile finished: statusCode=%d", result.StatusCode)
	return nil
}

// Shutdown sends build/shutdown and build/exit to Bloop.
func (c *Client) Shutdown(ctx context.Context) error {
	if c.transport == nil {
		return nil
	}
	_ = c.call(ctx, "build/shutdown", nil, nil)
	_ = c.notify("build/exit")
	if c.cmd != nil {
		return c.cmd.Wait()
	}
	return nil
}

// call sends a JSON-RPC request and waits for the response.
func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}

	idJSON, _ := json.Marshal(id)
	rawID := json.RawMessage(idJSON)

	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		ID:      &rawID,
		Method:  method,
		Params:  paramsJSON,
	}

	ch := make(chan *jsonrpc.Response, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	if err := c.transport.WriteRequest(req); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return err
	}

	c.logger.Printf("bsp -> %s (id=%d)", method, id)

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("BSP error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		if result != nil && resp.Result != nil {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return ctx.Err()
	}
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (c *Client) notify(method string) error {
	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  method,
	}
	return c.transport.WriteRequest(req)
}

// readLoop reads messages from Bloop and dispatches them.
func (c *Client) readLoop() {
	for {
		body, err := c.transport.ReadRaw()
		if err != nil {
			c.logger.Printf("bsp read error: %v", err)
			return
		}

		// Try to determine if this is a response or notification.
		var probe struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
		}
		if err := json.Unmarshal(body, &probe); err != nil {
			c.logger.Printf("bsp decode probe error: %v", err)
			continue
		}

		if probe.ID != nil && probe.Method == "" {
			// This is a response.
			var resp jsonrpc.Response
			if err := json.Unmarshal(body, &resp); err != nil {
				c.logger.Printf("bsp decode response error: %v", err)
				continue
			}
			var id int64
			if err := json.Unmarshal(*resp.ID, &id); err != nil {
				c.logger.Printf("bsp decode response id error: %v", err)
				continue
			}
			c.pendingMu.Lock()
			ch, ok := c.pending[id]
			if ok {
				delete(c.pending, id)
			}
			c.pendingMu.Unlock()
			if ok {
				ch <- &resp
			}
		} else if probe.Method != "" {
			// This is a notification.
			c.handleNotification(probe.Method, body)
		}
	}
}

func (c *Client) handleNotification(method string, body []byte) {
	switch method {
	case "build/publishDiagnostics":
		var req struct {
			Params PublishDiagnosticsParams `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			c.logger.Printf("bsp decode diagnostics error: %v", err)
			return
		}
		c.logger.Printf("bsp <- diagnostics: %s (%d items, reset=%v)",
			req.Params.TextDocument.URI, len(req.Params.Diagnostics), req.Params.Reset)
		if c.onDiagnostics != nil {
			c.onDiagnostics(req.Params)
		}
	default:
		c.logger.Printf("bsp <- unhandled notification: %s", method)
	}
}
