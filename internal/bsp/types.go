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

// CompileError represents a failure in the compilation process (user code errors).
type CompileError struct {
	StatusCode StatusCode
}

func (e *CompileError) Error() string {
	return "compilation failed"
}

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

// InverseSourcesParams is sent for "buildTarget/inverseSources".
type InverseSourcesParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// InverseSourcesResult is returned by "buildTarget/inverseSources".
type InverseSourcesResult struct {
	Targets []BuildTargetIdentifier `json:"targets"`
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

// LogMessageParams is sent by the build server for "build/logMessage".
type LogMessageParams struct {
	Type    MessageType `json:"type"`
	Task    *TaskID     `json:"task,omitempty"`
	Origin  string      `json:"origin,omitempty"`
	Message string      `json:"message"`
}

type MessageType int

const (
	MTError   MessageType = 1
	MTWarning MessageType = 2
	MTInfo    MessageType = 3
	MTLog     MessageType = 4
)

// TaskID identifies a build task.
type TaskID struct {
	ID     string  `json:"id"`
	Parent string  `json:"parent,omitempty"`
}

// TaskStartParams is sent for "build/taskStart".
type TaskStartParams struct {
	TaskID      TaskID      `json:"taskId"`
	EventTime   int64       `json:"eventTime,omitempty"`
	Message     string      `json:"message,omitempty"`
	DataKind    string      `json:"dataKind,omitempty"`
	Data        interface{} `json:"data,omitempty"`
}

// TaskProgressParams is sent for "build/taskProgress".
type TaskProgressParams struct {
	TaskID      TaskID      `json:"taskId"`
	EventTime   int64       `json:"eventTime,omitempty"`
	Message     string      `json:"message,omitempty"`
	Total       int64       `json:"total,omitempty"`
	Progress    int64       `json:"progress,omitempty"`
	Unit        string      `json:"unit,omitempty"`
	DataKind    string      `json:"dataKind,omitempty"`
	Data        interface{} `json:"data,omitempty"`
}

// TaskFinishParams is sent for "build/taskFinish".
type TaskFinishParams struct {
	TaskID      TaskID      `json:"taskId"`
	EventTime   int64       `json:"eventTime,omitempty"`
	Message     string      `json:"message,omitempty"`
	Status      StatusCode  `json:"status"`
	DataKind    string      `json:"dataKind,omitempty"`
	Data        interface{} `json:"data,omitempty"`
}
