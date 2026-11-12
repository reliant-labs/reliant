package cloud

import (
	"net/url"
	"os"
	"strings"
)

// DefaultConsoleURL is Reliant's forge console: where the dashboards, issues,
// releases and alerts of a project on Reliant's control plane are. It is the
// one place forge spells it; every printed link is built by Console.
const DefaultConsoleURL = "https://forge.reliantlabs.io"

// ConsoleURLEnv overrides the console base for every link forge prints (a
// self-hosted or staging console).
const ConsoleURLEnv = "FORGE_CONSOLE_URL"

// ConsoleBase resolves the console base URL: FORGE_CONSOLE_URL, then the
// project's own setting (forge.yaml monitoring.console_url; "" when it has
// none), then DefaultConsoleURL. The trailing slash is trimmed.
func ConsoleBase(configured string) string {
	for _, v := range []string{os.Getenv(ConsoleURLEnv), configured} {
		if v = strings.TrimRight(strings.TrimSpace(v), "/"); v != "" {
			return v
		}
	}
	return DefaultConsoleURL
}

// Console builds links into one project's console. The path vocabulary is
// the console's own (reliant frontends/web/src/components/Forge/Console/paths.ts):
// the project is the FORGE project name, and `?env=` is the one scope param.
type Console struct {
	// Base is ConsoleBase's answer.
	Base string
	// Project is the forge project name (forge.yaml `name`).
	Project string
}

// NewConsole is the console of project at base (ConsoleBase).
func NewConsole(base, project string) Console {
	return Console{Base: strings.TrimRight(base, "/"), Project: project}
}

// OK reports whether links can be built: a console with no project has no page.
func (c Console) OK() bool { return c.Base != "" && c.Project != "" }

func (c Console) link(path string, q url.Values) string {
	if !c.OK() {
		return ""
	}
	u := c.Base + "/forge/p/" + url.PathEscape(c.Project) + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	return u
}

func envQuery(env string) url.Values {
	if env == "" {
		return nil
	}
	return url.Values{"env": {env}}
}

// Home is the project's overview, scoped to env.
func (c Console) Home(env string) string { return c.link("", envQuery(env)) }

// Dashboards is the project's observability dashboards, scoped to env.
func (c Console) Dashboards(env string) string {
	return c.link("/observability/dashboards", envQuery(env))
}

// View is one observability view (errors, logs, traces, metrics, sessions,
// alerts, rpc, profiles, graph), scoped to env.
func (c Console) View(view, env string) string {
	return c.link("/observability/"+url.PathEscape(view), envQuery(env))
}

// Release is one release's page, scoped to env.
func (c Console) Release(version, env string) string {
	return c.link("/releases/"+url.PathEscape(version), envQuery(env))
}

// Environment is one environment's page.
func (c Console) Environment(env string) string {
	return c.link("/environments/"+url.PathEscape(env), nil)
}
