package cli

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
)

// upPassthroughArgs splits `forge env up`'s positional args at the cobra
// `--` terminator: the env is the one positional BEFORE it, and everything
// AFTER it is dev-server passthrough (forwarded to each frontend as
// `npm run dev -- <flags>`).
//
// dashPos is cmd.ArgsLenAtDash(): the count of args before the `--`, or -1
// when no `--` was given. Extracted from the RunE so the split and its
// validation are unit-testable without a real project.
//
// This is the seam the deleted `forge env up` existed for. That command was a
// thin alias over runUp whose only addition was this split, so it was one
// spelling of `env up` that drifted from it — it carried an `--env` FLAG
// where the env is a positional here, and defaulted it to dev, which made
// "which env am I running?" answerable two different ways. The passthrough
// moved onto `env up` and the alias went away.
func upPassthroughArgs(args []string, dashPos int) ([]string, error) {
	const usage = "forge env up takes exactly one positional argument, the environment; " +
		"pass dev-server flags after `--` (e.g. forge env up dev -- --host 0.0.0.0)"
	if dashPos < 0 {
		// No `--` terminator: every arg is positional, and there must be
		// exactly one (the env). MinimumNArgs(1) already rejected zero.
		if len(args) > 1 {
			return nil, errors.New(usage)
		}
		return nil, nil
	}
	// With a terminator, exactly the env may precede it.
	if dashPos != 1 {
		return nil, errors.New(usage)
	}
	return args[dashPos:], nil
}

// This file holds the env-composition helpers shared by the host-mode
// phase of `forge env up` (up.go) and the dev/prod parity check
// (doctor_parity.go). The standalone `forge env up` command was removed: it was
// an alias over the same runUp, and its one distinct feature — dev-server
// passthrough after `--` — is now `forge env up <env> -- <flags>`. The
// single-service runner is `forge env up <env> --target <service>`. These
// helpers stayed because non-run code still depends on them.

// managedProcess tracks a running child process started by the `forge env up`
// orchestrator (up.go). name/cmd identify the child; pid is the PID
// captured at Start time, so the PID ledger does not depend on the process
// handle after Wait. done synchronizes the observed exit with readiness.
type managedProcess struct {
	name    string
	cmd     *exec.Cmd
	pid     int
	done    chan struct{} // closed after Wait; synchronizes access to waitErr
	waitErr error
}

// loadProjectConfigEnv resolves the per-env app config from
// deploy/kcl/<env>/config.k — via config_gen.appConfigEnvMap, the SAME
// projection cluster mode renders into each workload's env — and returns it as
// env-var strings. Returns an empty map (not nil) on any error so callers can
// pass the result straight to [hostlaunch.LayerHostEnv] without guarding. A
// missing/unrenderable config is non-fatal — host-mode services run against
// whatever defaults the binary's flag/env loader provides.
//
// Only plain values apply on the host; forge.SecretRef entries
// belong to a cluster Secret and have no host equivalent (set them in
// `.env.<env>` or the developer shell). Reading the one KCL projection keeps
// host-mode services from drifting off their cluster-mode counterparts.
func loadProjectConfigEnv(_ *config.ProjectConfig, env string) map[string]string {
	out := map[string]string{}
	if env == "" {
		return out
	}
	projectPath, perr := findProjectConfigFile()
	if perr != nil {
		return out
	}
	srcs, err := loadProjectConfigEnvMap(filepath.Dir(projectPath), env)
	if err != nil {
		return out
	}
	for name, src := range srcs {
		if src.Value != nil {
			out[name] = *src.Value
		}
	}
	return out
}

// declaredServiceNames returns the names of every declared component,
// used by error paths that point users at the right spelling when they
// typo a service name. The inventory is enumerated from the REAL sources
// (proto descriptor + owned worker/operator files + cmd/ binaries):
// callers pass codegen.IntrospectComponents.
func declaredServiceNames(comps []config.ComponentConfig) []string {
	out := make([]string, 0, len(comps))
	for _, s := range comps {
		out = append(out, s.Name)
	}
	return out
}

// mergeConfigFrontends reconciles entities.Frontends (what the env's KCL
// declares) with the project's forge.yaml `frontends:`.
//
// Both sources are real. The scaffolded env templates — and
// `forge scaffold frontend` — declare each frontend in deploy/kcl/<env>/main.k
// with its dev port; forge.yaml also lists every frontend, and is the only
// place a frontend adopted without KCL (or declared before the KCL block
// existed) appears. The up/run frontend phase iterates entities.Frontends, so
// a frontend in neither place is never dev-served.
//
// PER FRONTEND, and the env's KCL is authoritative for every frontend it
// declares. The render is where the dev port is RESOLVED
// (`plugin.resolve_port` steps off a busy 3000 and remembers the answer), so
// a KCL-declared frontend keeps its KCL port, path and dev_runner; forge.yaml
// only fills fields the declaration left empty (today: type). A forge.yaml
// frontend the KCL does not mention is appended with forge.yaml's own fields,
// including dev_runner — the path an adopted pnpm app takes before it has a
// KCL declaration of its own.
//
// This used to be all-or-nothing: bridge every forge.yaml frontend when KCL
// carried none, and do nothing otherwise. The first half let forge.yaml's
// literal `port: 3000` stand in for a KCL-resolved port the env had never
// been told about, and the second silently dropped a frontend added after
// the first one was declared in KCL.
func mergeConfigFrontends(e *KCLEntities, cfg *config.ProjectConfig) {
	if e == nil || cfg == nil {
		return
	}
	declared := make(map[string]int, len(e.Frontends))
	for i, fe := range e.Frontends {
		declared[fe.Name] = i
	}
	for _, fe := range cfg.Frontends {
		if i, ok := declared[fe.Name]; ok {
			if e.Frontends[i].Type == "" {
				e.Frontends[i].Type = fe.Type
			}
			continue
		}
		entity := FrontendEntity{
			Name: fe.Name,
			Type: fe.Type,
			Path: fe.DeclaredDir(),
			Port: fe.Port,
		}
		if fe.DevRunner != "" {
			entity.DevRunner = fe.EffectiveDevRunner()
		}
		e.Frontends = append(e.Frontends, entity)
	}
}

// freeTCPPort asks the OS for an unused TCP port by binding :0 on loopback,
// reading the assigned port, then releasing it. There is an inherent
// (tiny) TOCTOU window between release and the dev server re-binding it, but
// for a per-project dev loop it is the standard, race-tolerant way to pick a
// port nothing else holds — and, crucially, two projects allocating
// concurrently are handed DIFFERENT ports by the kernel, which is exactly the
// non-collision property we need.
func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	// The listener exists only to have the kernel hand us a free port;
	// it is closed immediately and never written to.
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// resolveEphemeralFrontendPorts assigns a free OS port to every frontend that
// declares none (Port <= 0 — the ephemeral scaffold default), mutating BOTH
// the entity set the run launches AND the matching cfg.Frontends entry (by
// name). Stamping both keeps every downstream reader consistent off a single
// resolved value: the pre-flight port-conflict guard and post-launch
// readiness/summary read entities.Frontends[].Port; buildFrontendCmd
// force-injects it as PORT into the dev server. The backend does not need
// the resolved value: ENVIRONMENT=development makes it reflect whatever
// Origin arrives, which is the only policy that can hold when the port is
// assigned by the kernel at launch.
//
// This is what makes two freshly-scaffolded dev stacks coexist on one host:
// each gets a distinct, kernel-assigned free frontend port instead of both
// fighting for 3000/3001. A frontend that DID declare a concrete port keeps
// it verbatim (no-op), so existing projects are unaffected. Allocation
// failure leaves the port at 0 — buildFrontendCmd then falls through to the
// dev server's own default, exactly as before.
func resolveEphemeralFrontendPorts(cfg *config.ProjectConfig, e *KCLEntities) {
	if e == nil {
		return
	}
	for i := range e.Frontends {
		if e.Frontends[i].Port > 0 {
			continue
		}
		port, err := freeTCPPort()
		if err != nil {
			fmt.Printf("[up] frontend %s: could not allocate an ephemeral port (%v); falling back to the dev server default\n", e.Frontends[i].Name, err)
			continue
		}
		e.Frontends[i].Port = port
		fmt.Printf("[up] frontend %s: ephemeral dev port %d\n", e.Frontends[i].Name, port)
		if cfg != nil {
			for j := range cfg.Frontends {
				if cfg.Frontends[j].Name == e.Frontends[i].Name {
					cfg.Frontends[j].Port = port
				}
			}
		}
	}
}

// resolveEphemeralHostPorts assigns a free OS port to every long-running host
// workload that declares no port of its own, so two host-only dev stacks never
// both fall back to the architectural backend default (:8080). The allocated
// port is recorded as BOTH the workload's ListenPorts[0] AND a PORT in its
// per-run LaunchEnv, so every downstream reader stays consistent off one
// value: the pre-flight port-conflict guard and readiness gate (HostPorts),
// the summary URL (HostPort), and the launched process env (HostEnv, which
// the app binds). A workload that DOES declare a port keeps it verbatim.
//
// Returns the base URL of the primary (first) resolved host workload so the
// caller can wire the frontends at the ephemeral backend; empty when no host
// workload exposes a port.
func resolveEphemeralHostPorts(e *KCLEntities) string {
	if e == nil {
		return ""
	}
	backendURL := ""
	for i := range e.Workloads {
		w := &e.Workloads[i]
		if !w.OnRuntime(RuntimeHost) || !w.LongRunning() {
			continue
		}
		host := w.Runtime.Host
		// An explicitly EMPTY listen_ports means "this workload binds
		// nothing". Allocating an ephemeral port for it publishes a port the
		// process will never bind, and the readiness gate then fails a run
		// that in fact succeeded — observed with the packaged desktop app.
		if host.ListenPorts != nil && len(*host.ListenPorts) == 0 {
			continue
		}
		if p := w.HostPort(); p > 0 {
			// Already binds a declared port — leave it, but adopt it as the
			// backend URL if we don't have one yet.
			if backendURL == "" {
				backendURL = fmt.Sprintf("http://localhost:%d", p)
			}
			continue
		}
		port, err := freeTCPPort()
		if err != nil {
			fmt.Printf("[up] host %s: could not allocate an ephemeral port (%v); falling back to the default\n", w.Name, err)
			continue
		}
		host.ListenPorts = &[]int{port}
		if host.LaunchEnv == nil {
			host.LaunchEnv = map[string]string{}
		}
		host.LaunchEnv["PORT"] = fmt.Sprintf("%d", port)
		fmt.Printf("[up] host %s: ephemeral dev port %d\n", w.Name, port)
		if backendURL == "" {
			backendURL = fmt.Sprintf("http://localhost:%d", port)
		}
	}
	return backendURL
}

// frontendEnvPrefix returns the public-env-var prefix a frontend of the
// given type exposes to its client bundle: Next.js NEXT_PUBLIC_, Vite
// VITE_, React Native/Expo EXPO_PUBLIC_. Defaults to the Next.js prefix
// for an unset/unknown type. The single dispatch every framework-scoped
// var name (API_URL / MOCK_API / OTEL_ENDPOINT / ENVIRONMENT) is built on.
//
// Accepts BOTH the KCL Frontend.type spellings ("vite" / "rn" — what
// render.k projects) and the longer forge.yaml / scaffold-kind spellings
// ("vite-spa" / "react-native"), so the dispatch is correct whether the
// type comes from a rendered KCL entity or a config kind.
func frontendEnvPrefix(frontendType string) string {
	switch strings.ToLower(strings.TrimSpace(frontendType)) {
	case "vite", "vite-spa":
		return "VITE_"
	case "rn", "react-native", "react_native":
		return "EXPO_PUBLIC_"
	default:
		return "NEXT_PUBLIC_"
	}
}

// isNextFrontend reports whether the frontend type is Next.js (the
// default). Used to gate the Next-only NEXT_TELEMETRY_DISABLED knob.
func isNextFrontend(frontendType string) bool {
	return frontendEnvPrefix(frontendType) == "NEXT_PUBLIC_"
}

// frontendAPIURLEnvVar returns the environment variable a frontend of the
// given type reads to override its API base URL. Each scaffold's dev transport
// honors exactly one (see the generated connect.ts / apiurl_gen.ts): Next.js
// reads NEXT_PUBLIC_API_URL, Vite reads VITE_API_URL, React Native/Expo reads
// EXPO_PUBLIC_API_URL. Defaults to the Next.js name for an unset/unknown type.
func frontendAPIURLEnvVar(frontendType string) string {
	return frontendEnvPrefix(frontendType) + "API_URL"
}

// frontendMockEnvVar / frontendOTELEnvVar / frontendEnvironmentEnvVar are
// the mock-mode / browser-OTLP-endpoint / environment-label siblings of
// frontendAPIURLEnvVar — the framework-prefixed variable each scaffold's
// transport / telemetry module reads (see connect.ts / otel.ts). Same
// dispatch (frontendEnvPrefix) so the four move together.
func frontendMockEnvVar(frontendType string) string {
	return frontendEnvPrefix(frontendType) + "MOCK_API"
}

// frontendVersionEnvVar is the variable a frontend's bundle reads its
// service.version from, which forge sets to the build id (see
// deploytarget.StageInput.VersionEnvVar). Empty for a frontend type whose
// scaffold reads none.
func frontendVersionEnvVar(frontendType string) string {
	if frontendEnvPrefix(frontendType) == "EXPO_PUBLIC_" {
		return ""
	}
	return frontendEnvPrefix(frontendType) + "APP_VERSION"
}

func frontendOTELEnvVar(frontendType string) string {
	return frontendEnvPrefix(frontendType) + "OTEL_ENDPOINT"
}

func frontendEnvironmentEnvVar(frontendType string) string {
	return frontendEnvPrefix(frontendType) + "ENVIRONMENT"
}

// frontendConfigEnv maps a typed FrontendConfig onto the framework-scoped
// env vars (NEXT_PUBLIC_* / VITE_* / EXPO_PUBLIC_*, plus Next.js's
// NEXT_TELEMETRY_DISABLED) the frontend's transport + build read. It
// returns them as inline-value KCLEnvVar entries so they flow through the
// SAME dev-launch + build-time plumbing env_vars use. nil cfg (no `config`
// block) yields nil.
//
// mock is normalized: "off" (the default) contributes NOTHING here — the
// scaffold's transport already defaults to the real backend, and the
// build path force-sets an authoritative empty mock var separately (see
// frontendBuildEnv). "true" / "hybrid" pass through so a KCL-declared mock
// applies at dev launch (still overridable by the developer's shell).
func frontendConfigEnv(frontendType string, cfg *FrontendConfigEntity) []KCLEnvVar {
	if cfg == nil {
		return nil
	}
	var out []KCLEnvVar
	if cfg.APIURL != "" {
		out = append(out, KCLEnvVar{Name: frontendAPIURLEnvVar(frontendType), Value: cfg.APIURL})
	}
	if mv := frontendMockValue(cfg.Mock); mv != "" {
		out = append(out, KCLEnvVar{Name: frontendMockEnvVar(frontendType), Value: mv})
	}
	if cfg.OTELEndpoint != "" {
		out = append(out, KCLEnvVar{Name: frontendOTELEnvVar(frontendType), Value: cfg.OTELEndpoint})
	}
	if cfg.Environment != "" {
		out = append(out, KCLEnvVar{Name: frontendEnvironmentEnvVar(frontendType), Value: cfg.Environment})
	}
	if cfg.TelemetryDisabled && isNextFrontend(frontendType) {
		out = append(out, KCLEnvVar{Name: "NEXT_TELEMETRY_DISABLED", Value: "1"})
	}
	return out
}

// frontendMockValue normalizes a FrontendConfig.mock to the value its
// *_MOCK_API variable carries: "off" (or empty/unset) becomes "" — the
// real-backend default — while "true" / "hybrid" pass through verbatim
// (connect.ts treats anything other than those two as the real backend).
func frontendMockValue(mock string) string {
	m := strings.ToLower(strings.TrimSpace(mock))
	if m == "" || m == "off" {
		return ""
	}
	return m
}

// frontendConfigMockValue is frontendMockValue over an optional config
// block: "" (real backend) for a nil config or mock=off, else the mode.
func frontendConfigMockValue(cfg *FrontendConfigEntity) string {
	if cfg == nil {
		return ""
	}
	return frontendMockValue(cfg.Mock)
}
