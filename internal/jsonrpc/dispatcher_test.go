package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

func msg(t *testing.T, id *int, method string, params any) string {
	t.Helper()
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		m["id"] = *id
	}
	if params != nil {
		m["params"] = params
	}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
}

func intP(n int) *int { return &n }

func TestCancelRequest(t *testing.T) {
	// A slow handler that respects context cancellation.
	started := make(chan struct{})
	slowHandler := func(ctx context.Context, _ json.RawMessage) (any, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return "should not reach", nil
		}
	}

	// Build input: request 1 (slow), then cancel request 1, then shutdown + exit.
	var input bytes.Buffer
	input.WriteString(msg(t, intP(1), "slow/op", nil))
	input.WriteString(msg(t, nil, "$/cancelRequest", map[string]int{"id": 1}))
	input.WriteString(msg(t, intP(2), "shutdown", nil))
	input.WriteString(msg(t, nil, "exit", nil))

	var output bytes.Buffer
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)

	transport := NewTransport(&input, &output)
	d := NewDispatcher(transport, logger)

	d.RegisterConcurrent("slow/op", slowHandler)
	d.Register("shutdown", func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, nil
	})
	d.Register("exit", func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, ErrExit
	})

	// Run dispatcher with a timeout to prevent test hangs.
	var runErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runErr = d.Run(context.Background())
	}()

	// Wait for the slow handler to actually start before proceeding.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("slow handler did not start in time")
	}

	wg.Wait()
	if runErr != nil {
		t.Fatalf("dispatcher error: %v", runErr)
	}

	// Read both responses. Order is non-deterministic because the cancelled
	// request runs in a goroutine while shutdown runs sequentially.
	outTransport := NewTransport(&output, nil)

	var responses []*Response
	for i := 0; i < 2; i++ {
		resp, err := outTransport.ReadResponse()
		if err != nil {
			t.Fatalf("reading response %d: %v", i+1, err)
		}
		responses = append(responses, resp)
	}

	var foundCancelled, foundShutdown bool
	for _, resp := range responses {
		var id int64
		if resp.ID != nil {
			json.Unmarshal(*resp.ID, &id)
		}
		if id == 1 {
			if resp.Error == nil || resp.Error.Code != CodeRequestCancelled {
				t.Fatalf("request 1: expected RequestCancelled error, got %+v", resp.Error)
			}
			foundCancelled = true
		}
		if id == 2 {
			if resp.Error != nil {
				t.Fatalf("request 2: expected success, got error: %s", resp.Error.Message)
			}
			foundShutdown = true
		}
	}
	if !foundCancelled {
		t.Fatal("did not find cancelled response for request 1")
	}
	if !foundShutdown {
		t.Fatal("did not find shutdown response for request 2")
	}
}

func TestCancelUnknownRequest(t *testing.T) {
	// Cancelling a non-existent ID should be a no-op (no crash).
	var input bytes.Buffer
	input.WriteString(msg(t, nil, "$/cancelRequest", map[string]int{"id": 999}))
	input.WriteString(msg(t, intP(1), "shutdown", nil))
	input.WriteString(msg(t, nil, "exit", nil))

	var output bytes.Buffer
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)

	transport := NewTransport(&input, &output)
	d := NewDispatcher(transport, logger)
	d.Register("shutdown", func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, nil
	})
	d.Register("exit", func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, ErrExit
	})

	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("dispatcher error: %v", err)
	}
}

func TestClearClientPending_SendsNilSentinel(t *testing.T) {
	transport := NewTransport(&bytes.Buffer{}, &bytes.Buffer{})
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	d := NewDispatcher(transport, logger)

	ch := make(chan *Response, 1)
	d.cancelMu.Lock()
	d.clientPending["test-1"] = ch
	d.cancelMu.Unlock()

	d.clearClientPending()

	select {
	case resp := <-ch:
		if resp != nil {
			t.Errorf("expected nil sentinel, got %+v", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("channel did not receive nil sentinel")
	}

	d.cancelMu.Lock()
	n := len(d.clientPending)
	d.cancelMu.Unlock()
	if n != 0 {
		t.Errorf("clientPending not cleared, has %d entries", n)
	}
}

func TestCall_ReturnsErrorOnNilSentinel(t *testing.T) {
	var output bytes.Buffer
	transport := NewTransport(&bytes.Buffer{}, &output)
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	d := NewDispatcher(transport, logger)

	callDone := make(chan error, 1)
	go func() {
		var result json.RawMessage
		callDone <- d.Call(context.Background(), "test/method", nil, &result)
	}()

	// Wait for Call to register itself in clientPending.
	deadline := time.Now().Add(2 * time.Second)
	for {
		d.cancelMu.Lock()
		n := len(d.clientPending)
		d.cancelMu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Call never registered in clientPending")
		}
		time.Sleep(time.Millisecond)
	}

	// Simulate dispatcher shutdown.
	d.clearClientPending()

	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("Call should have returned an error after nil sentinel")
		}
		if !strings.Contains(err.Error(), "dispatcher closed") {
			t.Errorf("expected 'dispatcher closed' error, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call did not return after nil sentinel")
	}
}

func TestCall_ReturnsErrorWhenDispatcherClosed(t *testing.T) {
	var output bytes.Buffer
	transport := NewTransport(&bytes.Buffer{}, &output)
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	d := NewDispatcher(transport, logger)

	d.clearClientPending()

	var result json.RawMessage
	err := d.Call(context.Background(), "test/method", nil, &result)
	if err == nil {
		t.Fatal("Call should fail after dispatcher shutdown starts")
	}
	if !strings.Contains(err.Error(), "dispatcher closed") {
		t.Fatalf("expected dispatcher closed error, got %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("Call should not write after shutdown, wrote %d bytes", output.Len())
	}
}

func TestRun_ShutdownUnblocksPendingCallBeforeWaiting(t *testing.T) {
	var input bytes.Buffer
	input.WriteString(msg(t, intP(1), "slow/op", nil))

	var output bytes.Buffer
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	transport := NewTransport(&input, &output)
	d := NewDispatcher(transport, logger)

	started := make(chan struct{})
	d.RegisterConcurrent("slow/op", func(_ context.Context, _ json.RawMessage) (any, error) {
		close(started)

		var result json.RawMessage
		err := d.Call(context.Background(), "client/op", nil, &result)
		if err == nil {
			t.Fatal("Call should be interrupted when dispatcher shuts down")
		}
		if !strings.Contains(err.Error(), "dispatcher closed") {
			t.Fatalf("expected dispatcher closed error, got %v", err)
		}
		return "ok", nil
	})

	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(context.Background())
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent handler did not start in time")
	}

	select {
	case err := <-runDone:
		if err == nil {
			t.Fatal("Run should return the transport read error")
		}
		if !strings.Contains(err.Error(), "reading message") {
			t.Fatalf("expected read error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run hung waiting for a handler blocked in Call")
	}
}
