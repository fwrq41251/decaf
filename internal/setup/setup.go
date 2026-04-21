package setup

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Setup handles automatic project setup: detect build tool, export to Bloop,
// download semanticdb-javac, and inject it into Bloop config.
type Setup struct {
	logger       *log.Logger
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
		return fmt.Errorf("no supported build tool found in %s (looking for pom.xml, build.gradle, build.gradle.kts)", s.workspaceDir)
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

// bloopInstall runs the appropriate bloop export command for the build tool.
func (s *Setup) bloopInstall(ctx context.Context, buildTool string) error {
	needsInstall, reason, err := s.shouldRunBloopInstall(buildTool)
	if err != nil {
		return err
	}
	if !needsInstall {
		s.logger.Printf("skipping bloopInstall: %s", reason)
		return nil
	}
	s.logger.Printf("running bloopInstall: %s", reason)

	switch buildTool {
	case "maven":
		return s.mavenBloopInstall(ctx)
	case "gradle":
		return s.gradleBloopInstall(ctx)
	default:
		return fmt.Errorf("unsupported build tool: %s", buildTool)
	}
}

func (s *Setup) detectBuildTool() string {
	if _, err := os.Stat(filepath.Join(s.workspaceDir, "pom.xml")); err == nil {
		return "maven"
	}
	for _, name := range []string{"build.gradle", "build.gradle.kts"} {
		if _, err := os.Stat(filepath.Join(s.workspaceDir, name)); err == nil {
			return "gradle"
		}
	}
	return ""
}

func (s *Setup) shouldRunBloopInstall(buildTool string) (bool, string, error) {
	bloopFiles, err := s.bloopConfigFiles()
	if err != nil {
		return false, "", fmt.Errorf("reading .bloop directory: %w", err)
	}
	if len(bloopFiles) == 0 {
		return true, ".bloop/ has no config files", nil
	}

	buildFiles := s.buildFilesForTool(buildTool)
	if len(buildFiles) == 0 {
		return true, "no recognized build files found", nil
	}

	latestBuildModTime := time.Time{}
	latestBuildFile := ""
	for _, path := range buildFiles {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, "", fmt.Errorf("stat build file %s: %w", path, err)
		}
		if info.ModTime().After(latestBuildModTime) {
			latestBuildModTime = info.ModTime()
			latestBuildFile = path
		}
	}
	if latestBuildFile == "" {
		return true, "no existing build files found", nil
	}

	oldestBloopModTime := time.Time{}
	oldestBloopFile := ""
	for i, path := range bloopFiles {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return true, fmt.Sprintf("bloop config disappeared: %s", path), nil
			}
			return false, "", fmt.Errorf("stat bloop config %s: %w", path, err)
		}
		if i == 0 || info.ModTime().Before(oldestBloopModTime) {
			oldestBloopModTime = info.ModTime()
			oldestBloopFile = path
		}
	}

	if latestBuildModTime.After(oldestBloopModTime) {
		return true, fmt.Sprintf("%s is newer than %s", filepath.Base(latestBuildFile), filepath.Base(oldestBloopFile)), nil
	}

	return false, ".bloop/ is up-to-date with build files", nil
}

func (s *Setup) bloopConfigFiles() ([]string, error) {
	bloopDir := filepath.Join(s.workspaceDir, ".bloop")
	entries, err := os.ReadDir(bloopDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			files = append(files, filepath.Join(bloopDir, e.Name()))
		}
	}
	return files, nil
}

func (s *Setup) buildFilesForTool(buildTool string) []string {
	switch buildTool {
	case "maven":
		return []string{filepath.Join(s.workspaceDir, "pom.xml")}
	case "gradle":
		return []string{
			filepath.Join(s.workspaceDir, "build.gradle"),
			filepath.Join(s.workspaceDir, "build.gradle.kts"),
			filepath.Join(s.workspaceDir, "settings.gradle"),
			filepath.Join(s.workspaceDir, "settings.gradle.kts"),
			filepath.Join(s.workspaceDir, "gradle.properties"),
		}
	default:
		return nil
	}
}

// DiscoverJavaHome returns the path to the JAVA_HOME by checking override, environment, or system PATH.
func (s *Setup) DiscoverJavaHome(override string) string {
	if override != "" {
		return override
	}
	if javaHome := os.Getenv("JAVA_HOME"); javaHome != "" {
		return javaHome
	}
	// Try to find java home from path.
	if path, err := exec.LookPath("java"); err == nil {
		if realPath, err := filepath.EvalSymlinks(path); err == nil {
			// java is usually in bin/java
			return filepath.Dir(filepath.Dir(realPath))
		}
	}
	return ""
}

// DiscoverJDKSource attempts to find the path to the JDK source and extracts it if it is a zip.
// If javaHomeOverride is provided, it uses that instead of detecting from environment.
func (s *Setup) DiscoverJDKSource(javaHomeOverride string) string {
	javaHome := s.DiscoverJavaHome(javaHomeOverride)
	if javaHome == "" {
		return ""
	}

	// Common locations for src.zip in modern JDKs.
	zipPaths := []string{
		filepath.Join(javaHome, "lib", "src.zip"),
		filepath.Join(javaHome, "src.zip"),
	}

	var srcZip string
	for _, p := range zipPaths {
		if _, err := os.Stat(p); err == nil {
			srcZip = p
			break
		}
	}

	if srcZip == "" {
		return ""
	}

	// No longer extract the entire src.zip. The indexer will extract files on demand
	// using findAndExtractFromJar.
	return srcZip
}
