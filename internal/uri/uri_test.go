package uri

import (
	"runtime"
	"testing"
)

func TestFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/home/user/project", "file:///home/user/project"},
		{"/tmp/a/b.java", "file:///tmp/a/b.java"},
	}
	for _, tt := range tests {
		got := FromPath(tt.path)
		if got != tt.want {
			t.Errorf("FromPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestToPath(t *testing.T) {
	tests := []struct {
		uri  string
		want string
	}{
		{"file:///home/user/project", "/home/user/project"},
		{"file:///tmp/a/b.java", "/tmp/a/b.java"},
	}
	if runtime.GOOS == "windows" {
		tests = []struct {
			uri  string
			want string
		}{
			{"file:///C:/Users/project", "C:\\Users\\project"},
		}
	}
	for _, tt := range tests {
		got := ToPath(tt.uri)
		if got != tt.want {
			t.Errorf("ToPath(%q) = %q, want %q", tt.uri, got, tt.want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	paths := []string{
		"/home/user/project/src/Main.java",
		"/tmp/workspace",
	}
	for _, p := range paths {
		got := ToPath(FromPath(p))
		if got != p {
			t.Errorf("round-trip failed: %q -> %q", p, got)
		}
	}
}

func TestRel(t *testing.T) {
	tests := []struct {
		base, target, want string
	}{
		{"/home/user/project", "/home/user/project/src/Main.java", "src/Main.java"},
		{"file:///home/user/project", "file:///home/user/project/src/Main.java", "src/Main.java"},
		{"file:///home/user/project", "/home/user/project/src/Main.java", "src/Main.java"},
	}
	for _, tt := range tests {
		got := Rel(tt.base, tt.target)
		if got != tt.want {
			t.Errorf("Rel(%q, %q) = %q, want %q", tt.base, tt.target, got, tt.want)
		}
	}
}

func TestJoin(t *testing.T) {
	tests := []struct {
		base, rel, want string
	}{
		{"/home/user/project", "src/Main.java", "file:///home/user/project/src/Main.java"},
		{"file:///home/user/project", "src/Main.java", "file:///home/user/project/src/Main.java"},
	}
	for _, tt := range tests {
		got := Join(tt.base, tt.rel)
		if got != tt.want {
			t.Errorf("Join(%q, %q) = %q, want %q", tt.base, tt.rel, got, tt.want)
		}
	}
}

func TestIsURI(t *testing.T) {
	if !IsURI("file:///tmp/foo") {
		t.Error("expected file:///tmp/foo to be a URI")
	}
	if IsURI("/tmp/foo") {
		t.Error("expected /tmp/foo NOT to be a URI")
	}
}
