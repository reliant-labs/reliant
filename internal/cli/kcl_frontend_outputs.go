package cli

import (
	"encoding/json"
	"path/filepath"
	"strconv"

	"github.com/reliant-labs/forge/internal/config"
)

// frontendOutputsDArg is the `-D frontend_outputs=<json>` binding: each
// frontend forge.yaml declares, by name, mapped to its production build shape
// ("static", "standalone" or "server": config.FrontendConfig.EffectiveOutput,
// with a Vite or Expo build always "static").
//
// The render reads it to refuse a frontend bound to a static runtime
// (forge.OnHosted, forge.OnBucket, forge.OnFirebase) whose build is a server
// rather than a static export (kcl/render.k _not_static_export). forge.yaml
// is the one declaration of that fact — it is what generates the frontend's
// next.config — so the KCL never re-declares it; this projects it in.
//
// "" when there is nothing to bind: no readable forge.yaml, or no frontends.
// The render then judges nothing, exactly as a plain `kcl run` does — a
// broken forge.yaml is reported by the commands that load it, not by turning
// every render into a failure about something else.
func frontendOutputsDArg(projectDir string) string {
	cfg, err := loadProjectConfigFrom(filepath.Join(projectDir, defaultProjectConfigFile))
	if err != nil || cfg == nil {
		return ""
	}
	return frontendOutputsBinding(cfg.Frontends)
}

// frontendOutputsBinding is the pure half of frontendOutputsDArg. The JSON is
// passed as a QUOTED KCL string (strconv.Quote), the image_digests contract:
// KCL types it as `str` and render.k json.decodes it.
func frontendOutputsBinding(frontends []config.FrontendConfig) string {
	if len(frontends) == 0 {
		return ""
	}
	outputs := make(map[string]string, len(frontends))
	for _, fe := range frontends {
		if fe.Name == "" {
			continue
		}
		// A Vite or Expo build is static whatever `output:` says; only a
		// Next.js frontend's declared shape is passed through.
		shape := fe.EffectiveOutput()
		if fe.StaticExport() {
			shape = config.FrontendOutputStatic
		}
		outputs[fe.Name] = shape
	}
	if len(outputs) == 0 {
		return ""
	}
	// json.Marshal sorts map keys, so the binding is byte-stable across runs.
	b, err := json.Marshal(outputs)
	if err != nil {
		return ""
	}
	return "frontend_outputs=" + strconv.Quote(string(b))
}
