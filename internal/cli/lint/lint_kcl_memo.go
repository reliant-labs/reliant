package lint

// One KCL render per env and argument set, per lint run.
//
// Six lanes judge the deploy tree from a render of each env: pdb-blocks-drain
// and flux-safe through cluster.RenderManifests, secrets and telemetry through
// kclrender.Run with the same arguments, and flux-safe (again),
// identity-share-with and static-export through a second argument set. Each
// rendered every env on its own, so a project with three envs paid for 21
// KCL evaluations where 6 distinct ones exist; on Ranger they were most of a
// scoped lint's wall time (pdb 2.1s, flux-safe 4.2s, identity-share 1.6s of
// 14s; friction #10).
//
// The memo lives in the run's context, never in a package variable: an
// in-process host (forge mcp, reliant) runs many lints in one process, and a
// render is only the same render within one run of one tree.

import (
	"bytes"
	"context"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// kclRenderMemo holds a run's KCL renders by their inputs.
type kclRenderMemo struct {
	mu      sync.Mutex
	renders map[string]kclRender
	// run is the renderer behind the memo: kclrender.Run, or a test's.
	run func(workDir, source string, dArgs []string) ([]byte, error)
}

type kclRender struct {
	out []byte
	err error
}

type kclRenderMemoKey struct{}

// withKCLRenderMemo returns ctx carrying a fresh render memo, and routes
// cluster.RenderManifests through it, unless ctx already carries one (a
// targeted run that names several lanes shares one memo across them).
func withKCLRenderMemo(ctx context.Context) context.Context {
	return withKCLRenderMemoUsing(ctx, kclrender.Run)
}

func withKCLRenderMemoUsing(ctx context.Context, run func(workDir, source string, dArgs []string) ([]byte, error)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(kclRenderMemoKey{}).(*kclRenderMemo); ok {
		return ctx
	}
	m := &kclRenderMemo{renders: map[string]kclRender{}, run: run}
	ctx = context.WithValue(ctx, kclRenderMemoKey{}, m)
	return cluster.WithKCLRunner(ctx, func(_ context.Context, projectDir, source, _ string, options []string) ([]byte, error) {
		return m.render(projectDir, source, options)
	})
}

// lintKCLRun is kclrender.Run through the run's memo, when ctx carries one.
func lintKCLRun(ctx context.Context, workDir, source string, dArgs []string) ([]byte, error) {
	if ctx != nil {
		if m, ok := ctx.Value(kclRenderMemoKey{}).(*kclRenderMemo); ok {
			return m.render(workDir, source, dArgs)
		}
	}
	return kclrender.Run(workDir, source, dArgs)
}

// render returns the memoized render of source under dArgs, rendering it on
// first use. A failed render is remembered too: the same inputs fail the
// same way. Each caller gets its own copy of the bytes.
func (m *kclRenderMemo) render(workDir, source string, dArgs []string) ([]byte, error) {
	key := workDir + "\x00" + source + "\x00" + strings.Join(dArgs, "\x00")
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.renders[key]
	if !ok {
		out, err := m.run(workDir, source, dArgs)
		r = kclRender{out: out, err: err}
		m.renders[key] = r
	}
	return bytes.Clone(r.out), r.err
}
