package cli

// THE ENV DOCUMENT as an env-declaration source (internal/envdoc).
//
// An env declared by deploy/envs/<env>.yaml is read at the two render seams
// every command already goes through — renderKCLRaw here (the entity view)
// and cluster.RenderManifestsWithOverrides (the apply stream) — which return
// the document, validated and lowered, in place of a KCL evaluation. Nothing
// downstream knows the difference. This file holds the few cli-side facts
// that are about WHETHER an env is declared, which used to mean "its
// deploy/kcl/<env>/main.k exists".

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/envdoc"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// loadEnvDocument is the render of env when an env document declares it: the
// `{"output": …}` bytes a KCL evaluation would return. ok is false when there
// is no document (render the KCL). dArgs are the `-D` bindings the KCL
// render would have received; the image tag among them (a build's
// re-render binds one) is bound for the document too, with the release and a
// release snapshot's image pins from ctx.
func loadEnvDocument(ctx context.Context, projectDir, env string, dArgs ...string) ([]byte, bool, error) {
	opts := envdoc.Options{Release: kclrender.Release(ctx)}
	opts.WorkloadPins, opts.Immutable = kclrender.SnapshotPins(ctx)
	for _, a := range dArgs {
		if v, ok := strings.CutPrefix(a, "image_tag="); ok {
			if u, err := strconv.Unquote(v); err == nil {
				v = u
			}
			opts.ImageTag = v
		}
	}
	return envdoc.Load(projectDir, env, opts)
}

// envHasDeclaration reports whether env is declared in the project: its KCL
// entrypoint mainK exists, or an env document declares it. The "environment
// not found" checks ask this instead of stat'ing main.k.
func envHasDeclaration(projectDir, env, mainK string) bool {
	if mainK != "" {
		if _, err := os.Stat(mainK); err == nil {
			return true
		}
	}
	p, err := envdoc.Path(projectDir, env)
	return err == nil && p != ""
}

// envDocumentDeclaration is the part of an env document `forge obs init`
// needs: the project it names and its monitoring, in forge.yaml's
// MonitoringConfig vocabulary so init reads one shape whichever file declared
// the env.
type envDocumentDeclaration struct {
	Project    string
	Monitoring *config.MonitoringConfig
}

// readEnvDocumentDeclaration reads env's document, validated and lowered;
// ok is false when the env has none.
func readEnvDocumentDeclaration(projectDir, env string) (envDocumentDeclaration, bool, error) {
	out, ok, err := envdoc.Load(projectDir, env, envdoc.Options{})
	if !ok || err != nil {
		return envDocumentDeclaration{}, ok, err
	}
	var doc struct {
		Output struct {
			Project       string `json:"project"`
			Observability *struct {
				Enabled     bool `json:"enabled"`
				Destination *struct {
					Endpoint              string `json:"endpoint"`
					TokenFromControlPlane bool   `json:"token_from_control_plane"`
				} `json:"destination"`
			} `json:"observability"`
			ControlPlane *struct {
				Endpoint string `json:"endpoint"`
			} `json:"control_plane"`
		} `json:"output"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return envDocumentDeclaration{}, true, err
	}
	d := envDocumentDeclaration{Project: doc.Output.Project}
	if o := doc.Output.Observability; o != nil {
		m := &config.MonitoringConfig{Enabled: o.Enabled}
		if dst := o.Destination; dst != nil && !dst.TokenFromControlPlane {
			m.Endpoint = dst.Endpoint
		}
		if cp := doc.Output.ControlPlane; cp != nil {
			m.ControlPlane = cp.Endpoint
		}
		d.Monitoring = m
	}
	return d, true, nil
}

// envDocumentRel is env's document path relative to the project, with forward
// slashes on every OS, for messages ("" when there is none).
func envDocumentRel(projectDir, env string) string {
	p, err := envdoc.Path(projectDir, env)
	if err != nil || p == "" {
		return ""
	}
	if r, rerr := filepath.Rel(projectDir, p); rerr == nil {
		return filepath.ToSlash(r)
	}
	return p
}

// envDocumentsProject is the project the env documents under projectDir name,
// "" when there are none, one does not parse, or they disagree.
func envDocumentsProject(projectDir string) string {
	envs, err := envdoc.List(projectDir)
	if err != nil {
		return ""
	}
	project := ""
	for _, env := range envs {
		p, err := envdoc.Path(projectDir, env)
		if err != nil || p == "" {
			return ""
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return ""
		}
		doc, err := envdoc.Parse(raw, filepath.Ext(p) == ".json")
		if err != nil {
			return ""
		}
		name, _ := doc["project"].(string)
		if name == "" || (project != "" && name != project) {
			return ""
		}
		project = name
	}
	return project
}
