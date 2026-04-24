package setup

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShouldRunBloopInstallWhenNoBloopConfigs(t *testing.T) {
	tmpDir := t.TempDir()
	writeSetupFile(t, filepath.Join(tmpDir, "pom.xml"), "<project/>", time.Now())

	s := NewSetup(log.New(&bytes.Buffer{}, "[test] ", 0), tmpDir)
	got, reason, err := s.shouldRunBloopInstall("maven")
	if err != nil {
		t.Fatalf("shouldRunBloopInstall failed: %v", err)
	}
	if !got {
		t.Fatalf("shouldRunBloopInstall = false, want true (reason=%q)", reason)
	}
}

func TestShouldRunBloopInstallSkipsWhenBloopIsUpToDate(t *testing.T) {
	tmpDir := t.TempDir()
	now := time.Now()
	writeSetupFile(t, filepath.Join(tmpDir, "pom.xml"), "<project/>", now.Add(-2*time.Hour))
	writeSetupFile(t, filepath.Join(tmpDir, ".bloop", "app.json"), "{}", now.Add(-1*time.Hour))

	s := NewSetup(log.New(&bytes.Buffer{}, "[test] ", 0), tmpDir)
	got, reason, err := s.shouldRunBloopInstall("maven")
	if err != nil {
		t.Fatalf("shouldRunBloopInstall failed: %v", err)
	}
	if got {
		t.Fatalf("shouldRunBloopInstall = true, want false (reason=%q)", reason)
	}
}

func TestShouldRunBloopInstallWhenPomIsNewerThanBloop(t *testing.T) {
	tmpDir := t.TempDir()
	now := time.Now()
	writeSetupFile(t, filepath.Join(tmpDir, "pom.xml"), "<project/>", now)
	writeSetupFile(t, filepath.Join(tmpDir, ".bloop", "app.json"), "{}", now.Add(-1*time.Hour))

	s := NewSetup(log.New(&bytes.Buffer{}, "[test] ", 0), tmpDir)
	got, reason, err := s.shouldRunBloopInstall("maven")
	if err != nil {
		t.Fatalf("shouldRunBloopInstall failed: %v", err)
	}
	if !got {
		t.Fatalf("shouldRunBloopInstall = false, want true (reason=%q)", reason)
	}
}

func TestShouldRunBloopInstallWhenGradleBuildIsNewerThanBloop(t *testing.T) {
	tmpDir := t.TempDir()
	now := time.Now()
	writeSetupFile(t, filepath.Join(tmpDir, "build.gradle.kts"), "plugins {}", now)
	writeSetupFile(t, filepath.Join(tmpDir, ".bloop", "app.json"), "{}", now.Add(-1*time.Hour))

	s := NewSetup(log.New(&bytes.Buffer{}, "[test] ", 0), tmpDir)
	got, reason, err := s.shouldRunBloopInstall("gradle")
	if err != nil {
		t.Fatalf("shouldRunBloopInstall failed: %v", err)
	}
	if !got {
		t.Fatalf("shouldRunBloopInstall = false, want true (reason=%q)", reason)
	}
}

func TestSanitizeJavaEnvReplacesStaleJavaHome(t *testing.T) {
	tmpDir := t.TempDir()
	replacementJavaHome := filepath.Join(tmpDir, "jdk")
	if err := os.MkdirAll(filepath.Join(replacementJavaHome, "bin"), 0755); err != nil {
		t.Fatalf("mkdir replacement java home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(replacementJavaHome, "bin", "java"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write java binary: %v", err)
	}

	env := []string{
		"PATH=/usr/bin:/bin",
		"JAVA_HOME=" + filepath.Join(tmpDir, "missing-jdk"),
		"HOME=/tmp/home",
	}

	sanitized := SanitizeJavaEnv(env, replacementJavaHome)
	var javaHome string
	for _, entry := range sanitized {
		if strings.HasPrefix(entry, "JAVA_HOME=") {
			javaHome = strings.TrimPrefix(entry, "JAVA_HOME=")
		}
	}
	if javaHome != replacementJavaHome {
		t.Fatalf("JAVA_HOME = %q, want %q", javaHome, replacementJavaHome)
	}
}

func writeSetupFile(t *testing.T, path, content string, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}
