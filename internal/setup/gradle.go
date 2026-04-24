package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// gradleCmd returns the Gradle executable to use: the project wrapper if
// present, otherwise the bare "gradle" command from PATH.
func (s *Setup) gradleCmd() string {
	if runtime.GOOS == "windows" {
		bat := filepath.Join(s.workspaceDir, "gradlew.bat")
		if _, err := os.Stat(bat); err == nil {
			return bat
		}
	} else {
		wrapper := filepath.Join(s.workspaceDir, "gradlew")
		if _, err := os.Stat(wrapper); err == nil {
			return wrapper
		}
	}
	return "gradle"
}

func (s *Setup) gradleBloopInstall(ctx context.Context) error {
	s.logger.Println("running gradle bloopInstall...")

	gradle := s.gradleCmd()
	cmd := exec.CommandContext(ctx, gradle, "bloopInstall",
		"--console=plain",
	)
	cmd.Dir = s.workspaceDir
	cmd.Env = SanitizeJavaEnv(os.Environ(), "")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gradle bloopInstall failed: %w", err)
	}

	s.logger.Println("bloopInstall completed")
	return nil
}
