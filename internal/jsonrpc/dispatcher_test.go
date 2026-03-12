package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
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
