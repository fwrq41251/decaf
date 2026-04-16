package bsp

import (
	"bytes"
	"context"
	"errors"
	"log"
	"testing"
)

func TestClientMethodsReturnErrNotConnectedWhenNotReady(t *testing.T) {
	client := NewClient(log.New(&bytes.Buffer{}, "[test] ", 0), nil, nil, nil, nil, nil, nil)

	if err := client.Compile(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Compile() error = %v, want ErrNotConnected", err)
	}

	if err := client.CompileTargets(context.Background(), []BuildTargetIdentifier{{URI: "target"}}); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("CompileTargets() error = %v, want ErrNotConnected", err)
	}

	if _, err := client.InverseSources(context.Background(), "file:///tmp/Test.java"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("InverseSources() error = %v, want ErrNotConnected", err)
	}

	if _, err := client.DependencySources(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("DependencySources() error = %v, want ErrNotConnected", err)
	}

	if _, err := client.JvmRunEnvironment(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("JvmRunEnvironment() error = %v, want ErrNotConnected", err)
	}
}
