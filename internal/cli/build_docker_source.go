package cli

// DockerBuild's cross-repo sources: `context` and `dockerfile` are each a
// path OR a forge.GitSource (kcl/schema.k `str | GitSource`), so a deploy
// repo can declare workloads whose code lives in other repositories.
//
// Resolution goes through the one seam every GitSource uses
// (frontendSourceResolver: the shared repo+ref cache and the project's
// .forge/source-overrides.yaml). A project that declares no pin constructs no
// resolver and builds byte-identically.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/config"
)

// dockerBuildJSON is DockerBuild's wire shape with the two union fields left
// raw. The embedded pointer's own `context` / `dockerfile` fields are shadowed
// by the shallower ones here, which is how encoding/json resolves the clash.
type dockerBuildJSON struct {
	*dockerBuildPlain
	Context    json.RawMessage `json:"context,omitempty"`
	Dockerfile json.RawMessage `json:"dockerfile,omitempty"`
}

// dockerBuildPlain has DockerBuild's fields and none of its methods, so the
// JSON methods below do not recurse.
type dockerBuildPlain DockerBuild

// UnmarshalJSON decodes the rendered DockerBuild, splitting `context` and
// `dockerfile` into their path or GitSource member.
func (d *DockerBuild) UnmarshalJSON(data []byte) error {
	raw := dockerBuildJSON{dockerBuildPlain: (*dockerBuildPlain)(d)}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var err error
	if d.Context, d.ContextSource, err = decodePathOrSource(raw.Context); err != nil {
		return fmt.Errorf("DockerBuild.context: %w", err)
	}
	if d.Dockerfile, d.DockerfileSource, err = decodePathOrSource(raw.Dockerfile); err != nil {
		return fmt.Errorf("DockerBuild.dockerfile: %w", err)
	}
	return nil
}

// MarshalJSON writes the declaration back — the pin, never the directory it
// resolved to on this machine.
func (d DockerBuild) MarshalJSON() ([]byte, error) {
	out := dockerBuildJSON{dockerBuildPlain: (*dockerBuildPlain)(&d)}
	var err error
	if out.Context, err = encodePathOrSource(d.Context, d.ContextSource); err != nil {
		return nil, err
	}
	if out.Dockerfile, err = encodePathOrSource(d.Dockerfile, d.DockerfileSource); err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// decodePathOrSource reads a `str | GitSource` value. null and absent are the
// empty path.
func decodePathOrSource(raw json.RawMessage) (string, *config.GitSource, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil, nil
	}
	if raw[0] == '"' {
		var p string
		err := json.Unmarshal(raw, &p)
		return p, nil, err
	}
	// The same strict string-or-pin decoding named build_contexts use.
	var bc config.BuildContext
	if err := json.Unmarshal(raw, &bc); err != nil {
		return "", nil, err
	}
	return "", bc.Source, nil
}

func encodePathOrSource(path string, src *config.GitSource) (json.RawMessage, error) {
	switch {
	case src != nil:
		return json.Marshal(src)
	case path != "":
		return json.Marshal(path)
	}
	return nil, nil
}

// HasGitSource reports whether the build reads code or its Dockerfile from
// another repository.
func (d *DockerBuild) HasGitSource() bool {
	return d != nil && (d.ContextSource != nil || d.DockerfileSource != nil)
}

// resolveDockerBuildSources materializes a DockerBuild's context and
// Dockerfile pins and records the checkout the context resolved to. The
// declaration is kept; only the invocation-local fields are set. A failed
// fetch is an error, never a fallback to the project root — building the
// deploy repo's own tree in place of the pinned one would ship the wrong
// code under the right name.
//
// Dockerfile rule: a GitSource dockerfile is that file. A string dockerfile
// is project-root relative, EXCEPT under a GitSource context, where it is
// relative to the resolved context — docker's own default (`<context>/
// Dockerfile`), and what a deploy repo building another repo's image means.
// A deploy repo that wants its OWN Dockerfile over another repo's code keeps
// the context in-repo and names the other repo as a build_contexts entry.
func resolveDockerBuildSources(ctx context.Context, projectDir, workload string, d *DockerBuild) error {
	if !d.HasGitSource() {
		return nil
	}
	if d.ContextSource != nil && d.Context != "" {
		return fmt.Errorf("DockerBuild.context is both a path and a GitSource")
	}
	resolver, err := frontendSourceResolver(projectDir)
	if err != nil {
		return err
	}
	if src := d.ContextSource; src != nil && d.ContextDir == "" {
		if err := src.Validate(); err != nil {
			return fmt.Errorf("DockerBuild.context: %w", err)
		}
		res, err := resolver.Resolve(ctx, toGitSource(src))
		if err != nil {
			return fmt.Errorf("DockerBuild.context %s: %w", toGitSource(src), err)
		}
		if st, err := os.Stat(res.Dir); err != nil || !st.IsDir() {
			return fmt.Errorf("DockerBuild.context %s resolved to %s, which is not a directory", toGitSource(src), res.Dir)
		}
		d.ContextDir = res.Dir
		// The checkout as git reports it — HEAD and dirty flag — so a cached
		// pin records its commit and an override records what it really is.
		d.Built = captureDockerSource(ctx, workload, res.Dir, projectDir)
		if d.Built != nil && res.Overridden {
			fmt.Printf("[build] %s: building %s from a local override (%s) — NOT the pinned ref\n",
				workload, toGitSource(src), describeShellSource(d.Built))
		}
	}
	switch {
	case d.DockerfileSource != nil && d.DockerfilePath == "":
		src := d.DockerfileSource
		if err := src.Validate(); err != nil {
			return fmt.Errorf("DockerBuild.dockerfile: %w", err)
		}
		if src.Subdir == "" {
			return fmt.Errorf("DockerBuild.dockerfile %s: set subdir to the Dockerfile's path in the repository (e.g. subdir = \"Dockerfile\")", toGitSource(src))
		}
		res, err := resolver.Resolve(ctx, toGitSource(src))
		if err != nil {
			return fmt.Errorf("DockerBuild.dockerfile %s: %w", toGitSource(src), err)
		}
		if st, err := os.Stat(res.Dir); err != nil || st.IsDir() {
			return fmt.Errorf("DockerBuild.dockerfile %s resolved to %s, which is not a file", toGitSource(src), res.Dir)
		}
		d.DockerfilePath = res.Dir
	case d.ContextDir != "" && d.DockerfileSource == nil && d.DockerfilePath == "":
		rel := d.Dockerfile
		if rel == "" {
			rel = "Dockerfile"
		}
		if filepath.IsAbs(rel) || strings.HasPrefix(filepath.ToSlash(filepath.Clean(rel)), "../") {
			return fmt.Errorf("DockerBuild.dockerfile %q must be relative to the GitSource context %s", rel, toGitSource(d.ContextSource))
		}
		path := filepath.Join(d.ContextDir, filepath.FromSlash(rel))
		// A pinned build never skips: a Dockerfile missing from the pinned
		// tree is a typo or a moved file, and building nothing would leave
		// an old image (or none) deployed under this workload.
		if st, err := os.Stat(path); err != nil || st.IsDir() {
			return fmt.Errorf("DockerBuild.dockerfile %q is not a file in the GitSource context %s (resolved to %s)", rel, toGitSource(d.ContextSource), d.ContextDir)
		}
		d.DockerfilePath = path
	}
	return nil
}

// captureDockerSource records the checkout a pinned context resolved to. A
// var so tests that exercise resolution alone need not exec git.
var captureDockerSource = captureShellSource

// serviceDockerfile is the Dockerfile a DockerBuild builds with: the path a
// pin resolved to, else the declared path, else the project-root Dockerfile.
func serviceDockerfile(d *DockerBuild) string {
	switch {
	case d == nil:
		return "Dockerfile"
	case d.DockerfilePath != "":
		return d.DockerfilePath
	case d.Dockerfile != "":
		return d.Dockerfile
	}
	return "Dockerfile"
}

// dockerSourceWorkloads are the workloads whose DockerBuild context is a
// GitSource — the external checkouts a release must hold to their pins.
func dockerSourceWorkloads(e *KCLEntities) []WorkloadEntity {
	if e == nil {
		return nil
	}
	var out []WorkloadEntity
	for _, w := range e.Workloads {
		if d := w.Build.Docker; d != nil && d.ContextSource != nil {
			out = append(out, w)
		}
	}
	return out
}

// refuseDockerSourceOverrides refuses a release while any DockerBuild pin
// (context or dockerfile) resolves through .forge/source-overrides.yaml: the
// image would be built from a working copy no other machine can rebuild,
// which is the rule a pinned frontend already follows
// (addFrontendSourceArtifactsWith). An override is resolved without a fetch,
// and a pin the build already resolved is a cache hit.
func refuseDockerSourceOverrides(ctx context.Context, resolver pinResolver, version string, entities *KCLEntities) error {
	var refusals []string
	for _, w := range dockerPinnedWorkloads(entities) {
		d := w.Build.Docker
		for _, pin := range []*config.GitSource{d.ContextSource, d.DockerfileSource} {
			if pin == nil {
				continue
			}
			src := toGitSource(pin)
			res, err := resolver.Resolve(ctx, src)
			if err != nil {
				return fmt.Errorf("--release %s: workload %q: resolve pinned source %s: %w", version, w.Name, src, err)
			}
			if res.Overridden {
				refusals = append(refusals, fmt.Sprintf("  workload %q: %s → %s (local override)", w.Name, src, res.Dir))
			}
		}
	}
	if len(refusals) == 0 {
		return nil
	}
	return fmt.Errorf("--release %s: refusing to cut — a DockerBuild pin is served by a LOCAL SOURCE OVERRIDE, not its declared ref:\n%s\n"+
		"  A release must be rebuildable from its pins, and an override is a working copy no other machine has.\n"+
		"  Remove the entries from %s and run this command again",
		version, strings.Join(refusals, "\n"), filepath.Join(".forge", "source-overrides.yaml"))
}

// dockerPinnedWorkloads are the workloads whose DockerBuild context or
// Dockerfile is a GitSource.
func dockerPinnedWorkloads(e *KCLEntities) []WorkloadEntity {
	if e == nil {
		return nil
	}
	var out []WorkloadEntity
	for _, w := range e.Workloads {
		if w.Build.Docker.HasGitSource() {
			out = append(out, w)
		}
	}
	return out
}

// sourceForRecord is the checkout a build result records for the release
// ledger (built_from), nil when the build reads only this project.
func (d *DockerBuild) sourceForRecord() *buildtarget.Source {
	if d == nil {
		return nil
	}
	return d.Built
}
