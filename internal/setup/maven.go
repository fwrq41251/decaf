package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

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
