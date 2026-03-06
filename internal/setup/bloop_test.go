package setup

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInjectSemanticDB(t *testing.T) {
	tmpDir := t.TempDir()
	bloopDir := filepath.Join(tmpDir, ".bloop")
	os.MkdirAll(bloopDir, 0755)

	// Create a minimal bloop config.
	config := map[string]any{
		"version": "1.4.0",
		"project": map[string]any{
			"name":       "root",
			"directory":  tmpDir,
			"sources":    []string{filepath.Join(tmpDir, "src/main/java")},
			"dependencies": []string{},
			"classpath":  []string{"/some/lib.jar"},
			"out":        filepath.Join(tmpDir, ".bloop/root"),
			"classesDir": filepath.Join(tmpDir, ".bloop/root/classes"),
			"java": map[string]any{
				"options": []string{"-source", "11", "-target", "11"},
			},
		},
	}

	data, _ := json.MarshalIndent(config, "", "    ")
	configPath := filepath.Join(bloopDir, "root.json")
	os.WriteFile(configPath, data, 0644)

	// Run injection.
	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	s := NewSetup(logger, tmpDir)
	jarPath := "/fake/path/semanticdb-javac-0.10.0.jar"

	if err := s.injectSemanticDB(jarPath); err != nil {
		t.Fatalf("injectSemanticDB failed: %v", err)
	}

	// Read back and verify.
	modified, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]json.RawMessage
	json.Unmarshal(modified, &result)
	var project map[string]json.RawMessage
	json.Unmarshal(result["project"], &project)
	var java map[string]json.RawMessage
	json.Unmarshal(project["java"], &java)
	var options []string
	json.Unmarshal(java["options"], &options)

	// Should contain original options.
	if options[0] != "-source" || options[1] != "11" {
		t.Fatalf("original options lost: %v", options)
	}

	// Should contain processorpath.
	foundProcessor := false
	foundPlugin := false
	for _, opt := range options {
		if strings.Contains(opt, "-processorpath:") && strings.Contains(opt, jarPath) {
			foundProcessor = true
		}
		if strings.HasPrefix(opt, "-Xplugin:semanticdb") {
			foundPlugin = true
		}
	}

	if !foundProcessor {
		t.Fatalf("missing -processorpath in options: %v", options)
	}
	if !foundPlugin {
		t.Fatalf("missing -Xplugin:semanticdb in options: %v", options)
	}

	// Running again should be idempotent.
	if err := s.injectSemanticDB(jarPath); err != nil {
		t.Fatalf("second injectSemanticDB failed: %v", err)
	}

	modified2, _ := os.ReadFile(configPath)
	var result2 map[string]json.RawMessage
	json.Unmarshal(modified2, &result2)
	var project2 map[string]json.RawMessage
	json.Unmarshal(result2["project"], &project2)
	var java2 map[string]json.RawMessage
	json.Unmarshal(project2["java"], &java2)
	var options2 []string
	json.Unmarshal(java2["options"], &options2)

	if len(options2) != len(options) {
		t.Fatalf("injection not idempotent: first=%d options, second=%d options", len(options), len(options2))
	}
}

func TestInjectSemanticDB_NoJavaSection(t *testing.T) {
	tmpDir := t.TempDir()
	bloopDir := filepath.Join(tmpDir, ".bloop")
	os.MkdirAll(bloopDir, 0755)

	// Config without java section.
	config := map[string]any{
		"version": "1.4.0",
		"project": map[string]any{
			"name":       "root",
			"directory":  tmpDir,
			"sources":    []string{},
			"dependencies": []string{},
			"classpath":  []string{},
			"out":        filepath.Join(tmpDir, ".bloop/root"),
			"classesDir": filepath.Join(tmpDir, ".bloop/root/classes"),
		},
	}

	data, _ := json.MarshalIndent(config, "", "    ")
	os.WriteFile(filepath.Join(bloopDir, "root.json"), data, 0644)

	logger := log.New(&bytes.Buffer{}, "[test] ", 0)
	s := NewSetup(logger, tmpDir)

	if err := s.injectSemanticDB("/fake/jar.jar"); err != nil {
		t.Fatalf("injectSemanticDB failed: %v", err)
	}
}
