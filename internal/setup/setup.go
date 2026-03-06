package setup

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// Setup handles automatic project setup: detect build tool, export to Bloop,
// download semanticdb-javac, and inject it into Bloop config.
type Setup struct {
	logger     *log.Logger
	workspaceDir string // absolute path of the workspace root
}

// NewSetup creates a new Setup for the given workspace directory.
func NewSetup(logger *log.Logger, workspaceDir string) *Setup {
	return &Setup{
		logger:       logger,
		workspaceDir: workspaceDir,
	}
}

// Run performs the full setup sequence.
// Returns the path to the semanticdb-javac jar (for reference) or error.
func (s *Setup) Run(ctx context.Context) error {
	// Step 1: Detect build tool.
	buildTool := s.detectBuildTool()
	if buildTool == "" {
		return fmt.Errorf("no supported build tool found in %s (looking for pom.xml)", s.workspaceDir)
	}
	s.logger.Printf("detected build tool: %s", buildTool)

	// Step 2: Export to Bloop (bloopInstall).
	if err := s.bloopInstall(ctx, buildTool); err != nil {
		return fmt.Errorf("bloopInstall failed: %w", err)
	}

	// Step 3: Download semanticdb-javac.
	jarPath, err := s.ensureSemanticDBJavac(ctx)
	if err != nil {
		return fmt.Errorf("downloading semanticdb-javac: %w", err)
	}
	s.logger.Printf("semanticdb-javac jar: %s", jarPath)

	// Step 4: Inject into Bloop config.
	if err := s.injectSemanticDB(jarPath); err != nil {
		return fmt.Errorf("injecting semanticdb-javac into bloop config: %w", err)
	}

	return nil
}

func (s *Setup) detectBuildTool() string {
	if _, err := os.Stat(filepath.Join(s.workspaceDir, "pom.xml")); err == nil {
		return "maven"
	}
	// Future: detect build.gradle, build.sbt, etc.
	return ""
}
