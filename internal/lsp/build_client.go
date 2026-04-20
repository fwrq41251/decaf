package lsp

import (
	"context"

	"github.com/fwrq41251/decaf/internal/bsp"
)

type buildClient interface {
	Start(ctx context.Context, rootURI string) error
	Shutdown(ctx context.Context) error
	IsReady() bool
	Compile(ctx context.Context) error
	CompileTargets(ctx context.Context, targets []bsp.BuildTargetIdentifier) error
	InverseSources(ctx context.Context, fileURI string) ([]bsp.BuildTargetIdentifier, error)
	DependencySources(ctx context.Context) ([]bsp.DependencySourcesItem, error)
	JvmRunEnvironment(ctx context.Context) ([]bsp.JvmEnvironmentItem, error)
}
