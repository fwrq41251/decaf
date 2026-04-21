package bsp

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
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

func TestTailBufferKeepsRecentOutput(t *testing.T) {
	var buf tailBuffer

	prefix := strings.Repeat("a", bloopStartupLogLimit/2)
	middle := strings.Repeat("b", bloopStartupLogLimit/2)
	suffix := strings.Repeat("c", 32)

	if _, err := buf.Write([]byte(prefix)); err != nil {
		t.Fatalf("Write(prefix) failed: %v", err)
	}
	if _, err := buf.Write([]byte(middle)); err != nil {
		t.Fatalf("Write(middle) failed: %v", err)
	}
	if _, err := buf.Write([]byte(suffix)); err != nil {
		t.Fatalf("Write(suffix) failed: %v", err)
	}

	got := buf.String()
	want := (prefix + middle + suffix)[len(prefix+middle+suffix)-bloopStartupLogLimit:]
	if len(got) != bloopStartupLogLimit {
		t.Fatalf("String() length = %d, want %d", len(got), bloopStartupLogLimit)
	}
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
