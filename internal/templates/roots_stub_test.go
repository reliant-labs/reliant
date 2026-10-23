package templates_test

import (
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// serverRootStub is the ROOTS block `forge generate` appends to a graph-mode
// project's config_gen.k, rendered from the same template
// (project/config-roots.k.tmpl) for the default `server` root reaching
// PORT and the given credentials (the first one required). The config_gen.k
// stubs the env renders import carry it, as a generated module does.
func serverRootStub(t *testing.T, secrets ...string) string {
	t.Helper()
	q := func(ss []string) string {
		out := "["
		for i, s := range ss {
			if i > 0 {
				out += ", "
			}
			out += `"` + s + `"`
		}
		return out + "]"
	}
	required := []string{}
	if len(secrets) > 0 {
		required = secrets[:1]
	}
	type view struct{ Name, Args, Env, Secrets, Required string }
	out, err := templates.ProjectTemplates().Render("config-roots.k.tmpl", struct {
		Roots              []view
		ConfigType, EnvMap string
		Argv               bool
	}{Roots: []view{{
		Name: `"server"`, Args: `["server"]`, Env: `["PORT"]`, Secrets: q(secrets), Required: q(required),
	}}, ConfigType: "AppConfig", EnvMap: "appConfigEnvMap"})
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
