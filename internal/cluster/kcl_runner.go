package cluster

import "context"

// KCLRunner is the environment evaluation boundary. The CLI supplies its
// isolated kcl eval transport; library callers retain the embedded renderer.
type KCLRunner func(ctx context.Context, projectDir, source, env string, options []string) ([]byte, error)

type kclRunnerKey struct{}

// WithKCLRunner scopes a renderer to one command, never a process-global hook.
func WithKCLRunner(ctx context.Context, run KCLRunner) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, kclRunnerKey{}, run)
}
