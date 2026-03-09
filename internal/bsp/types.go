package bsp

// InitializeBuildParams is sent to the build server on "build/initialize".
type InitializeBuildParams struct {
	DisplayName  string       `json:"displayName"`
	Version      string       `json:"version"`
	BSPVersion   string       `json:"bspVersion"`
	RootURI      string       `json:"rootUri"`
	Capabilities BuildClientCapabilities `json:"capabilities"`
}

type BuildClientCapabilities struct {
	LanguageIDs []string `json:"languageIds"`
}

// InitializeBuildResult is returned by the build server.
type InitializeBuildResult struct {
	DisplayName  string                  `json:"displayName"`
	Version      string                  `json:"version"`
	BSPVersion   string                  `json:"bspVersion"`
	Capabilities BuildServerCapabilities `json:"capabilities"`
}

type BuildServerCapabilities struct {
	CompileProvider *CompileProvider `json:"compileProvider,omitempty"`
	RunProvider     *RunProvider    `json:"runProvider,omitempty"`
	TestProvider    *TestProvider   `json:"testProvider,omitempty"`
}

type CompileProvider struct {
	LanguageIDs []string `json:"languageIds"`
}

type RunProvider struct {
	LanguageIDs []string `json:"languageIds"`
}

type TestProvider struct {
	LanguageIDs []string `json:"languageIds"`
}

// BuildTargetIdentifier identifies a build target.
type BuildTargetIdentifier struct {
	URI string `json:"uri"`
}

// CompileParams is sent for "buildTarget/compile".
type CompileParams struct {
	Targets []BuildTargetIdentifier `json:"targets"`
}

// CompileResult is returned by "buildTarget/compile".
type CompileResult struct {
	StatusCode StatusCode `json:"statusCode"`
}

type StatusCode int

const (
	StatusOK        StatusCode = 1
	StatusError     StatusCode = 2
	StatusCancelled StatusCode = 3
)

// WorkspaceBuildTargetsResult is returned by "workspace/buildTargets".
type WorkspaceBuildTargetsResult struct {
	Targets []BuildTarget `json:"targets"`
}

type BuildTarget struct {
	ID           BuildTargetIdentifier `json:"id"`
	DisplayName  string                `json:"displayName,omitempty"`
	LanguageIDs  []string              `json:"languageIds"`
	Capabilities BuildTargetCapabilities `json:"capabilities"`
}

type BuildTargetCapabilities struct {
	CanCompile bool `json:"canCompile"`
	CanTest    bool `json:"canTest"`
	CanRun     bool `json:"canRun"`
}

// DependencySourcesParams is sent for "buildTarget/dependencySources".
type DependencySourcesParams struct {
	Targets []BuildTargetIdentifier `json:"targets"`
}

// DependencySourcesResult is returned by "buildTarget/dependencySources".
type DependencySourcesResult struct {
	Items []DependencySourcesItem `json:"items"`
}

type DependencySourcesItem struct {
	Target  BuildTargetIdentifier `json:"target"`
	Sources []string              `json:"sources"`
}

// JvmRunEnvironmentParams is sent for "buildTarget/jvmRunEnvironment".
type JvmRunEnvironmentParams struct {
	Targets []BuildTargetIdentifier `json:"targets"`
}

// JvmRunEnvironmentResult is returned by "buildTarget/jvmRunEnvironment".
type JvmRunEnvironmentResult struct {
	Items []JvmEnvironmentItem `json:"items"`
}

type JvmEnvironmentItem struct {
	Target             BuildTargetIdentifier `json:"target"`
	Classpath          []string              `json:"classpath"`
	JvmOptions         []string              `json:"jvmOptions"`
	WorkingDirectory   string                `json:"workingDirectory"`
	EnvironmentVariables map[string]string     `json:"environmentVariables"`
	JavaHome           string                `json:"javaHome,omitempty"`
	JavaVersion        string                `json:"javaVersion,omitempty"`
}

// PublishDiagnosticsParams is sent by the build server as a notification.
type PublishDiagnosticsParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	BuildTarget  BuildTargetIdentifier  `json:"buildTarget"`
	Diagnostics  []Diagnostic           `json:"diagnostics"`
	Reset        bool                   `json:"reset"`
}

type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

type Diagnostic struct {
	Range    Range    `json:"range"`
	Severity int      `json:"severity,omitempty"`
	Message  string   `json:"message"`
	Source   string   `json:"source,omitempty"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}
