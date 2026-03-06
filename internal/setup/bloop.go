package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// bloopConfig represents the subset of .bloop/*.json we need to modify.
type bloopConfig struct {
	Version string       `json:"version"`
	Project bloopProject `json:"project"`
}

type bloopProject struct {
	Name       string          `json:"name"`
	Directory  string          `json:"directory"`
	Sources    []string        `json:"sources"`
	Dependencies []string      `json:"dependencies"`
	Classpath  []string        `json:"classpath"`
	Out        string          `json:"out"`
	ClassesDir string          `json:"classesDir"`
	Java       *bloopJava      `json:"java,omitempty"`

	// Preserve all other fields.
	Extra map[string]json.RawMessage `json:"-"`
}

type bloopJava struct {
	Options []string `json:"options"`
}

// injectSemanticDB modifies all .bloop/*.json files to add semanticdb-javac.
func (s *Setup) injectSemanticDB(jarPath string) error {
	bloopDir := filepath.Join(s.workspaceDir, ".bloop")
	entries, err := os.ReadDir(bloopDir)
	if err != nil {
		return fmt.Errorf("reading .bloop directory: %w", err)
	}

	modified := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		configPath := filepath.Join(bloopDir, e.Name())
		if err := s.injectIntoConfig(configPath, jarPath); err != nil {
			s.logger.Printf("warning: failed to inject into %s: %v", e.Name(), err)
			continue
		}
		modified++
	}

	s.logger.Printf("injected semanticdb-javac into %d bloop config(s)", modified)
	return nil
}

const semanticdbPluginPrefix = "-Xplugin:semanticdb"

func (s *Setup) injectIntoConfig(configPath, jarPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}

	// Parse as generic JSON to preserve unknown fields.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing %s: %w", configPath, err)
	}

	projectRaw, ok := raw["project"]
	if !ok {
		return fmt.Errorf("no 'project' field in %s", configPath)
	}

	var project map[string]json.RawMessage
	if err := json.Unmarshal(projectRaw, &project); err != nil {
		return fmt.Errorf("parsing project in %s: %w", configPath, err)
	}

	// Get classesDir for targetroot.
	var classesDir string
	if cd, ok := project["classesDir"]; ok {
		json.Unmarshal(cd, &classesDir)
	}

	// Get or create java.options.
	var javaObj map[string]json.RawMessage
	if javaRaw, ok := project["java"]; ok {
		json.Unmarshal(javaRaw, &javaObj)
	}
	if javaObj == nil {
		javaObj = make(map[string]json.RawMessage)
	}

	var options []string
	if optsRaw, ok := javaObj["options"]; ok {
		json.Unmarshal(optsRaw, &options)
	}

	// Check if already injected.
	for _, opt := range options {
		if strings.HasPrefix(opt, semanticdbPluginPrefix) {
			s.logger.Printf("semanticdb already configured in %s, skipping", filepath.Base(configPath))
			return nil
		}
	}

	// Add semanticdb-javac options.
	// -processorpath: tells javac where to find the annotation processor
	// -Xplugin:semanticdb: activates the javac plugin
	// -sourceroot: workspace root for relativizing URIs
	// -targetroot: where to write .semanticdb files
	options = append(options,
		fmt.Sprintf("-processorpath:%s", jarPath),
		fmt.Sprintf("%s -sourceroot:%s -targetroot:%s",
			semanticdbPluginPrefix, s.workspaceDir, classesDir),
	)

	optsJSON, _ := json.Marshal(options)
	javaObj["options"] = optsJSON
	javaJSON, _ := json.Marshal(javaObj)
	project["java"] = javaJSON
	projectJSON, _ := json.Marshal(project)
	raw["project"] = projectJSON

	output, err := json.MarshalIndent(raw, "", "    ")
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, output, 0644)
}
