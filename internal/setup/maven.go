package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// bloopInstall runs the appropriate bloop export command for the build tool.
func (s *Setup) bloopInstall(ctx context.Context, buildTool string) error {
	bloopDir := filepath.Join(s.workspaceDir, ".bloop")

	// Skip if .bloop/ already exists and has config files.
	if entries, err := os.ReadDir(bloopDir); err == nil {
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".json" {
				s.logger.Printf(".bloop/ already contains config files, skipping bloopInstall")
				return nil
			}
		}
	}

	switch buildTool {
	case "maven":
		return s.mavenBloopInstall(ctx)
	default:
		return fmt.Errorf("unsupported build tool: %s", buildTool)
	}
}

func (s *Setup) mavenBloopInstall(ctx context.Context) error {
	s.logger.Println("running maven bloopInstall...")

	cmd := exec.CommandContext(ctx, "mvn",
		"ch.epfl.scala:bloop-maven-plugin:bloopInstall",
		"-DdownloadSources=true",
	)
	cmd.Dir = s.workspaceDir
	cmd.Stdout = os.Stderr // show maven output to user
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mvn bloopInstall failed: %w", err)
	}

	s.logger.Println("bloopInstall completed")
	return nil
}
