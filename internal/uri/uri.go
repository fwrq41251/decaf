// Package uri provides consistent conversions between file:// URIs and
// filesystem paths. All helpers produce forward-slash URIs and
// platform-native paths, avoiding the common pitfalls of manual
// string manipulation (double slashes on Windows, back-slash leaks, etc.).
package uri

import (
	"net/url"
	"path"
	"path/filepath"
	"strings"
)

// FromPath converts an absolute filesystem path to a file:// URI.
//   /home/user/project  →  file:///home/user/project
//   C:\Users\project    →  file:///C:/Users/project
func FromPath(absPath string) string {
	p := filepath.ToSlash(absPath)
	if !strings.HasPrefix(p, "/") {
		// Windows: C:/foo → /C:/foo
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// ToPath converts a file:// URI to an absolute filesystem path using the
// platform's native separator.
//   file:///home/user/project  →  /home/user/project
//   file:///C:/Users/project   →  C:\Users\project  (on Windows)
func ToPath(uri string) string {
	if !IsURI(uri) {
		return filepath.FromSlash(uri)
	}

	u, err := url.Parse(uri)
	if err != nil {
		return filepath.FromSlash(strings.TrimPrefix(uri, "file://"))
	}

	p := u.Path
	if u.Host != "" {
		p = "//" + u.Host + p
	}

	// On Windows the URI looks like file:///C:/..., which leaves /C:/...
	// in u.Path — strip the leading slash before the drive letter.
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

// Rel returns the relative (forward-slash) path of target with respect to
// base. Both arguments may be file:// URIs or absolute paths.
// If the relative path cannot be computed, the target is returned as-is
// with forward slashes.
func Rel(base, target string) string {
	basePath := ToPath(base)
	targetPath := ToPath(target)
	rel, err := filepath.Rel(basePath, targetPath)
	if err != nil {
		return filepath.ToSlash(targetPath)
	}
	return filepath.ToSlash(rel)
}

// Join combines a base path (or URI) with relative path segments and
// returns a file:// URI.
func Join(base string, rel string) string {
	basePath := ToPath(base)
	return FromPath(filepath.Join(basePath, filepath.FromSlash(path.Clean(rel))))
}

// IsURI reports whether s looks like a file:// URI.
func IsURI(s string) bool {
	return strings.HasPrefix(s, "file://")
}
