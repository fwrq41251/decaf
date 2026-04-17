package bsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fwrq41251/decaf/internal/jsonrpc"
	"github.com/fwrq41251/decaf/internal/uri"
)

// DiagnosticsHandler is called when the build server publishes diagnostics.
type DiagnosticsHandler func(params PublishDiagnosticsParams)

// LogMessageHandler is called when the build server sends a log message.
type LogMessageHandler func(params LogMessageParams)

// TaskStartHandler is called when a task starts.
type TaskStartHandler func(params TaskStartParams)

// TaskProgressHandler is called when a task makes progress.
type TaskProgressHandler func(params TaskProgressParams)

// TaskFinishHandler is called when a task finishes.
type TaskFinishHandler func(params TaskFinishParams)

// Client manages the connection to a BSP build server (Bloop).
type Client struct {
	logger        *log.Logger
	transport     *jsonrpc.Transport
	conn          net.Conn // underlying socket connection
	cmd           *exec.Cmd
	ready         atomic.Bool
	nextID        atomic.Int64
	pending       map[int64]chan *jsonrpc.Response
	pendingMu     sync.Mutex
	onDiagnostics  DiagnosticsHandler
	onLogMessage   LogMessageHandler
	onTaskStart    TaskStartHandler
	onTaskProgress TaskProgressHandler
	onTaskFinish   TaskFinishHandler
	onDisconnect   func()
	targets        []BuildTarget
	socketDir      string // temp directory containing the unix socket
	exitErr        chan error
	}

var ErrNotConnected = errors.New("bsp client not connected")


// NewClient creates a new BSP client.
func NewClient(logger *log.Logger, onDiagnostics DiagnosticsHandler, onDisconnect func(), onLog LogMessageHandler, onStart TaskStartHandler, onProgress TaskProgressHandler, onFinish TaskFinishHandler) *Client {
	return &Client{
		logger:         logger,
		pending:        make(map[int64]chan *jsonrpc.Response),
		onDiagnostics:  onDiagnostics,
		onDisconnect:   onDisconnect,
		onLogMessage:   onLog,
		onTaskStart:    onStart,
		onTaskProgress: onProgress,
		onTaskFinish:   onFinish,
	}
}

// Start launches the Bloop BSP server and performs the initialize handshake.
func (c *Client) Start(ctx context.Context, rootURI string) error {
	c.ready.Store(false)
	sourceRoot := uri.ToPath(rootURI)

	bloopExe, err := exec.LookPath("bloop")
	if err != nil {
		return fmt.Errorf("bloop not found in PATH: %w", err)
	}

	// Create a private temp directory for the socket to restrict permissions.
	socketDir, err := os.MkdirTemp("", "decaf-bloop-*")
	if err != nil {
		return fmt.Errorf("creating socket directory: %w", err)
	}
	socketPath := filepath.Join(socketDir, "bsp.socket")
	c.socketDir = socketDir
	cleanup := true
	defer func() {
		if cleanup {
			if c.conn != nil {
				c.conn.Close()
			}
			if c.cmd != nil && c.cmd.Process != nil {
				c.cmd.Process.Kill()
			}
			os.RemoveAll(socketDir)
			c.socketDir = ""
		}
	}()

	c.logger.Printf("starting bloop bsp using socket: %s", socketPath)
	
	// Start bloop bsp process.
	c.cmd = exec.CommandContext(ctx, bloopExe, "bsp", "--protocol", "local", "--socket", socketPath)
	c.cmd.Dir = sourceRoot
	c.cmd.Env = os.Environ()
	
	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("starting bloop process: %w", err)
	}

	c.exitErr = make(chan error, 1)
	go func() {
		err := c.cmd.Wait()
		c.logger.Printf("bloop process exited: %v", err)
		c.exitErr <- err
		close(c.exitErr)
	}()

	// Wait for socket to be created (with timeout).
	start := time.Now()
	var conn net.Conn
	var dialErr error
	for time.Since(start) < 5*time.Second {
		select {
		case err := <-c.exitErr:
			return fmt.Errorf("bloop process exited before socket was ready: %w", err)
		default:
		}
		conn, dialErr = net.Dial("unix", socketPath)
		if dialErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if dialErr != nil {
		return fmt.Errorf("failed to connect to bloop socket %s after 5s: %w", socketPath, dialErr)
	}

	c.logger.Println("connected to bloop bsp socket")
	c.conn = conn
	c.transport = jsonrpc.NewTransport(conn, conn)

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
	c.ready.Store(true)
	c.logger.Printf("found %d build targets", len(c.targets))
	cleanup = false
	return nil
}

func (c *Client) IsReady() bool {
	return c.ready.Load()
}

// Compile triggers compilation of all known build targets.
func (c *Client) Compile(ctx context.Context) error {
	if !c.IsReady() {
		return ErrNotConnected
	}
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

	return c.CompileTargets(ctx, ids)
}

// CompileTargets triggers compilation of specific build targets.
func (c *Client) CompileTargets(ctx context.Context, targets []BuildTargetIdentifier) error {
	if !c.IsReady() {
		return ErrNotConnected
	}
	if len(targets) == 0 {
		return nil
	}
	var result CompileResult
	if err := c.call(ctx, "buildTarget/compile", CompileParams{Targets: targets}, &result); err != nil {
		return fmt.Errorf("buildTarget/compile failed: %w", err)
	}
	c.logger.Printf("compile finished: statusCode=%d (%d targets)", result.StatusCode, len(targets))
	if result.StatusCode != StatusOK {
		return &CompileError{StatusCode: result.StatusCode}
	}
	return nil
}

// InverseSources returns the build targets that contain the given source file.
func (c *Client) InverseSources(ctx context.Context, fileURI string) ([]BuildTargetIdentifier, error) {
	if !c.IsReady() {
		return nil, ErrNotConnected
	}
	var result InverseSourcesResult
	params := InverseSourcesParams{TextDocument: TextDocumentIdentifier{URI: fileURI}}
	if err := c.call(ctx, "buildTarget/inverseSources", params, &result); err != nil {
		return nil, fmt.Errorf("buildTarget/inverseSources failed: %w", err)
	}
	return result.Targets, nil
}

// DependencySources returns the source JARs for all known build targets.
func (c *Client) DependencySources(ctx context.Context) ([]DependencySourcesItem, error) {
	if !c.IsReady() {
		return nil, ErrNotConnected
	}
	if len(c.targets) == 0 {
		return nil, nil
	}

	ids := make([]BuildTargetIdentifier, 0, len(c.targets))
	for _, t := range c.targets {
		ids = append(ids, t.ID)
	}

	var result DependencySourcesResult
	if err := c.call(ctx, "buildTarget/dependencySources", DependencySourcesParams{Targets: ids}, &result); err != nil {
		return nil, fmt.Errorf("buildTarget/dependencySources failed: %w", err)
	}
	return result.Items, nil
}

// JvmRunEnvironment returns the JVM runtime environment (including javaHome) for all known build targets.
func (c *Client) JvmRunEnvironment(ctx context.Context) ([]JvmEnvironmentItem, error) {
	if !c.IsReady() {
		return nil, ErrNotConnected
	}
	if len(c.targets) == 0 {
		return nil, nil
	}

	ids := make([]BuildTargetIdentifier, 0, len(c.targets))
	for _, t := range c.targets {
		ids = append(ids, t.ID)
	}

	var result JvmRunEnvironmentResult
	if err := c.call(ctx, "buildTarget/jvmRunEnvironment", JvmRunEnvironmentParams{Targets: ids}, &result); err != nil {
		return nil, fmt.Errorf("buildTarget/jvmRunEnvironment failed: %w", err)
	}
	return result.Items, nil
}

// Shutdown sends build/shutdown and build/exit to Bloop, then closes the connection.
func (c *Client) Shutdown(ctx context.Context) error {
	c.ready.Store(false)
	if c.socketDir != "" {
		defer os.RemoveAll(c.socketDir)
	}
	if c.transport == nil {
		return nil
	}
	_ = c.call(ctx, "build/shutdown", nil, nil)
	_ = c.notify("build/exit")

	// Close the socket so readLoop unblocks and exits.
	if c.conn != nil {
		c.conn.Close()
	}

	c.clearPending()

	if c.cmd != nil && c.exitErr != nil {
		select {
		case <-c.exitErr:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Stop the Bloop daemon to free resources.
	if bloopExe, err := exec.LookPath("bloop"); err == nil {
		cmd := exec.CommandContext(ctx, bloopExe, "exit")
		if out, err := cmd.CombinedOutput(); err != nil {
			c.logger.Printf("bloop exit failed: %v (output: %s)", err, string(out))
		} else {
			c.logger.Println("bloop daemon stopped")
		}
	}

	return nil
}

func (c *Client) clearPending() {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for id, ch := range c.pending {
		select {
		case ch <- nil: // signal connection closed
		default:
		}
		delete(c.pending, id)
	}
}

// call sends a JSON-RPC request and waits for the response.
func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	if c.transport == nil {
		return ErrNotConnected
	}
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
	case resp, ok := <-ch:
		if !ok || resp == nil {
			return fmt.Errorf("BSP connection closed")
		}
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
	if c.transport == nil {
		return nil
	}
	return c.transport.WriteRequest(req)
}

// readLoop reads messages from Bloop and dispatches them.
func (c *Client) readLoop() {
	defer c.clearPending()
	for {
		body, err := c.transport.ReadRaw()
		if err != nil {
			c.ready.Store(false)
			c.logger.Printf("bsp read error: %v", err)
			if c.onDisconnect != nil {
				c.onDisconnect()
			}
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
				ch <- &resp
			}
			c.pendingMu.Unlock()
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
	case "build/logMessage":
		var req struct {
			Params LogMessageParams `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			c.logger.Printf("bsp decode logMessage error: %v", err)
			return
		}
		if c.onLogMessage != nil {
			c.onLogMessage(req.Params)
		}
	case "build/taskStart":
		var req struct {
			Params TaskStartParams `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			c.logger.Printf("bsp decode taskStart error: %v", err)
			return
		}
		if c.onTaskStart != nil {
			c.onTaskStart(req.Params)
		}
	case "build/taskProgress":
		var req struct {
			Params TaskProgressParams `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			c.logger.Printf("bsp decode taskProgress error: %v", err)
			return
		}
		if c.onTaskProgress != nil {
			c.onTaskProgress(req.Params)
		}
	case "build/taskFinish":
		var req struct {
			Params TaskFinishParams `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			c.logger.Printf("bsp decode taskFinish error: %v", err)
			return
		}
		if c.onTaskFinish != nil {
			c.onTaskFinish(req.Params)
		}
	default:
		c.logger.Printf("bsp <- unhandled notification: %s", method)
	}
}
