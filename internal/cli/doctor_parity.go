// Package cli — `forge doctor parity <svc>` subcommand.
//
// Detects when a service's effective env+config diverges between
// host-mode (`forge run <svc>`) and cluster-mode (`forge env deploy <env>`)
// projection — the "local wasn't representative of prod" class of bug
// that surfaces as a deploy failure on Friday afternoon.
//
// The check is static: we compute what each mode WOULD see by
// composing the same inputs the real loaders compose
// (forge.yaml environments[<env>].config + KCL host env_vars on the
// host side; KCL cluster env_vars on the cluster side), then diff.
// No process is spawned, no manifest applied, no Secret read — just a
// structured report of where the two modes disagree.
//
// Three categories of divergence:
//
//  1. value_mismatch — same key set on both sides with different
//     inline values. Always a bug (exit 1).
//  2. missing_in_<side> — key set in one mode, absent in the other.
//     Bug (exit 1) UNLESS the cluster side projects from a Secret
//     (host's secrets_file is the documented counterpart but we don't
//     load it — see secret_channel_divergence below).
//  3. secret_channel_divergence — same key is sourced from
//     secrets_file on host and `secretRef` on cluster (or one side
//     references a ${SECRET_REF} placeholder). EXPECTED — the channels
//     differ by design. Reported so the model can verify the key
//     names line up, but does NOT fail the exit code.
//
// The diff core (diffParity) is pure: it operates on two side-input
// structs the cobra path assembles from real loaders and tests
// assemble in-memory. Filesystem touches stay in the cobra path.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// parityValueSource is the kind of source backing a single env-var
// value. The JSON output uses the snake_case string form (see
// String()) so consumers can branch on it without inspecting the
// human-readable label.
type parityValueSource int

const (
	// parityUnset — key not present in this mode at all.
	parityUnset parityValueSource = iota
	// parityForgeYAMLConfig — value comes from the per-env KCL config
	// (deploy/kcl/<env>/config.k, projected via appConfigEnvMap). Host AND
	// cluster both derive from it — divergences here are usually a config.k
	// typo. (The JSON kind string is kept stable for consumers.)
	parityForgeYAMLConfig
	// parityHostKCLEnvVar — KCL HostDeploy.env_vars entry with an
	// inline `value` set (host-mode only channel).
	parityHostKCLEnvVar
	// parityClusterKCLEnvVar — KCL K8sCluster.env_vars entry with an
	// inline `value` set (cluster-mode equivalent).
	parityClusterKCLEnvVar
	// paritySecretsFile — host-mode secrets_file source. We never
	// load the file's contents — just note its path so the model
	// can verify the key list lines up with the cluster Secret.
	paritySecretsFile
	// parityKCLSecretRef — cluster-mode KCL EnvVar with secret_ref +
	// secret_key set. Projects from a Secret at runtime.
	parityKCLSecretRef
	// parityKCLConfigMapRef — cluster-mode KCL EnvVar with
	// config_map_ref + config_map_key set.
	parityKCLConfigMapRef
	// paritySecretRefPlaceholder — per-env KCL config carrying a
	// secret-backed value (a forge.SecretRef). Host-mode treats this as "user's
	// env supplies it"; cluster-mode resolves via its own channel.
	paritySecretRefPlaceholder
)

// String returns the source's machine-readable label (used in JSON
// output and as the stable identifier for the kind field).
func (s parityValueSource) String() string {
	switch s {
	case parityUnset:
		return "unset"
	case parityForgeYAMLConfig:
		return "forge_yaml_config"
	case parityHostKCLEnvVar:
		return "kcl_host_env_var"
	case parityClusterKCLEnvVar:
		return "kcl_cluster_env_var"
	case paritySecretsFile:
		return "secrets_file"
	case parityKCLSecretRef:
		return "kcl_secret_ref"
	case parityKCLConfigMapRef:
		return "kcl_config_map_ref"
	case paritySecretRefPlaceholder:
		return "secret_ref_placeholder"
	default:
		return "unknown"
	}
}

// parityValue is one side of the diff for a single key — the
// effective value (empty for projected sources we can't resolve
// statically) plus enough source detail to point the user at the
// right file.
type parityValue struct {
	// Source classifies where the value came from. parityUnset means
	// the key isn't present on this side at all.
	Source parityValueSource `json:"source_kind"`
	// SourceLabel is the human-readable file/field attribution
	// (e.g. "deploy/kcl/dev/config.k",
	// "KCL secret_ref name=tasks-secrets key=stripe").
	SourceLabel string `json:"source"`
	// Value is the resolved literal where one exists. For projected
	// sources (secret_ref, config_map_ref, secrets_file, ${SECRET})
	// Value is empty — we don't dereference projection channels.
	Value string `json:"value,omitempty"`
}

// parityKey is one row of the diff: a single env-var name and how
// each mode sources its value.
type parityKey struct {
	Name    string      `json:"name"`
	Host    parityValue `json:"host"`
	Cluster parityValue `json:"cluster"`
}

// parityDivergenceKind classifies why two parityValues didn't agree.
// "secret_channel_divergence" is the only kind that does NOT fail the
// exit code — the rest are bug-class divergences worth blocking.
type parityDivergenceKind string

const (
	parityValueMismatch       parityDivergenceKind = "value_mismatch"
	parityMissingInHost       parityDivergenceKind = "missing_in_host"
	parityMissingInCluster    parityDivergenceKind = "missing_in_cluster"
	paritySecretChannelDiverg parityDivergenceKind = "secret_channel_divergence"
)

// parityDivergence carries one disagreement plus the suggested fix.
type parityDivergence struct {
	Key  parityKey            `json:"key"`
	Kind parityDivergenceKind `json:"kind"`
	Fix  string               `json:"fix"`
}

// parityReport is the full structured output — the agree list (keys
// both modes converge on, included so the model can sanity-check the
// inputs went through) and the divergence list.
type parityReport struct {
	Service     string             `json:"service"`
	Env         string             `json:"env"`
	Agree       []parityKey        `json:"agree"`
	Divergences []parityDivergence `json:"divergences"`
}

// bugDivergences returns the subset of divergences whose presence
// should make `forge doctor parity` exit 1. Secret-channel
// divergences are reported but don't fail the build — the channels
// differ by design.
func (r *parityReport) bugDivergences() []parityDivergence {
	out := make([]parityDivergence, 0, len(r.Divergences))
	for _, d := range r.Divergences {
		if d.Kind == paritySecretChannelDiverg {
			continue
		}
		out = append(out, d)
	}
	return out
}

// parityInputs is the pure-function input to diffParity. Each map
// carries already-resolved key→parityValue pairs for the relevant
// channel. The cobra path builds these from RenderKCL + the per-env KCL
// config (deploy/kcl/<env>/config.k) + the project's secrets_file path;
// tests construct them directly to exercise the diff logic
// hermetically.
type parityInputs struct {
	Service       string
	Env           string
	ForgeYAMLEnv  map[string]parityValue // both modes derive from it — deploy/kcl/<env>/config.k
	HostKCL       map[string]parityValue // KCL HostDeploy.env_vars
	HostSecrets   map[string]parityValue // declared keys in secrets_file (never loaded — names only when supplied)
	ClusterKCL    map[string]parityValue // KCL K8sCluster.env_vars (inline values)
	ClusterSecret map[string]parityValue // KCL K8sCluster.env_vars with secret_ref / config_map_ref
}

// diffParity is the pure decision core. Computes the effective
// host-mode and cluster-mode views, then walks the union of keys to
// classify each into agree / divergence. Layering rule (matches
// host-launch + the cluster Deployment projection):
//
//   - Host: secrets_file is the BASE; KCL HostDeploy.env_vars layered
//     on top; forge.yaml config layered on top of that. So the
//     priority highest→lowest is: ForgeYAMLEnv > HostKCL > HostSecrets.
//   - Cluster: forge.yaml config projects via ConfigMap, KCL cluster
//     env_vars are direct manifest entries; secret_refs project from
//     Secrets. ClusterKCL inline wins over ForgeYAMLEnv when both set
//     the same key, matching how the K8s manifest renderer overlays
//     them.
//
// Both sides converge on forge.yaml so a key set only there agrees
// trivially (both surfaces resolve it via the same path).
func diffParity(in parityInputs) parityReport {
	host := composeHostSide(in)
	cluster := composeClusterSide(in)

	keys := unionKeys(host, cluster)
	sort.Strings(keys)

	report := parityReport{
		Service:     in.Service,
		Env:         in.Env,
		Agree:       []parityKey{},
		Divergences: []parityDivergence{},
	}
	for _, k := range keys {
		row := parityKey{
			Name:    k,
			Host:    pickValue(host, k),
			Cluster: pickValue(cluster, k),
		}
		classify(&report, row)
	}
	return report
}

// composeHostSide layers the host-mode channels in priority order.
// ForgeYAMLEnv > HostKCL > HostSecrets (forge.yaml wins on conflict
// because it's the version-controlled override; KCL beats secrets
// because the secrets file is "fallback" — secrets-file collisions
// with KCL values are precisely the bug the host-launch layering
// invariant exists to prevent).
func composeHostSide(in parityInputs) map[string]parityValue {
	out := map[string]parityValue{}
	for k, v := range in.HostSecrets {
		out[k] = v
	}
	for k, v := range in.HostKCL {
		out[k] = v
	}
	for k, v := range in.ForgeYAMLEnv {
		out[k] = v
	}
	return out
}

// composeClusterSide layers the cluster-mode channels. ForgeYAMLEnv
// is the ConfigMap-projected baseline; KCL cluster env_vars override;
// secret_ref / config_map_ref entries are added (never overlap with
// inline KCL values for the same key by construction).
func composeClusterSide(in parityInputs) map[string]parityValue {
	out := map[string]parityValue{}
	for k, v := range in.ForgeYAMLEnv {
		out[k] = v
	}
	for k, v := range in.ClusterKCL {
		out[k] = v
	}
	for k, v := range in.ClusterSecret {
		// Secret/ConfigMap refs lose to inline KCL values if a
		// duplicate exists (the schema disallows the duplicate but
		// we're defensive).
		if _, hasInline := out[k]; !hasInline {
			out[k] = v
		}
	}
	return out
}

// pickValue returns the resolved parityValue for a key, or the unset
// sentinel if the side never declared it.
func pickValue(m map[string]parityValue, k string) parityValue {
	if v, ok := m[k]; ok {
		return v
	}
	return parityValue{Source: parityUnset, SourceLabel: "<unset>"}
}

// unionKeys returns the unique sorted-input set of keys present in
// either input map. Caller sorts.
func unionKeys(a, b map[string]parityValue) []string {
	seen := map[string]struct{}{}
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// classify appends one row to either the Agree or Divergences slice
// based on the (host, cluster) tuple.
func classify(report *parityReport, row parityKey) {
	hSet := row.Host.Source != parityUnset
	cSet := row.Cluster.Source != parityUnset

	// Both unset can't happen — the row wouldn't exist.
	if !hSet && !cSet {
		return
	}

	// Both set: compare values. Secret-channel sources are special —
	// host-mode secrets_file vs cluster secret_ref is the EXPECTED
	// divergence; flag but don't bug-class it.
	if hSet && cSet {
		hostSC := isSecretChannel(row.Host.Source)
		clusterSC := isSecretChannel(row.Cluster.Source)
		// If either side is a secret channel AND the kinds aren't
		// identical, it's an expected secret-channel divergence —
		// host's secrets_file and cluster's secret_ref are the
		// canonical pair. Reported but doesn't fail the exit code.
		if (hostSC || clusterSC) && row.Host.Source != row.Cluster.Source {
			report.Divergences = append(report.Divergences, parityDivergence{
				Key:  row,
				Kind: paritySecretChannelDiverg,
				Fix:  fixSecretChannel(row),
			})
			return
		}
		// Both sides on the SAME secret channel kind (rare —
		// synthetic case in tests) → no inline values to compare,
		// channels match in shape, agree.
		if hostSC && clusterSC {
			report.Agree = append(report.Agree, row)
			return
		}
		if row.Host.Value == row.Cluster.Value {
			report.Agree = append(report.Agree, row)
			return
		}
		report.Divergences = append(report.Divergences, parityDivergence{
			Key:  row,
			Kind: parityValueMismatch,
			Fix:  fixValueMismatch(row),
		})
		return
	}

	// One side missing.
	if !hSet {
		// Cluster set, host missing. If cluster sources from a
		// secret channel AND host declares the key via
		// secrets_file (which we don't see here because secrets_file
		// content isn't loaded), that'd be the expected case. With
		// the data we have, only flag as secret-channel when the
		// cluster IS a secret channel AND the host has a (non-empty)
		// declared-but-unloaded secrets_file source. The
		// conservative default — flag as missing — keeps the user
		// honest: if the host truly resolves it via secrets_file
		// they'll see the divergence, check the file, and confirm
		// the key is present.
		report.Divergences = append(report.Divergences, parityDivergence{
			Key:  row,
			Kind: parityMissingInHost,
			Fix:  fixMissingInHost(row),
		})
		return
	}
	// Host set, cluster missing.
	report.Divergences = append(report.Divergences, parityDivergence{
		Key:  row,
		Kind: parityMissingInCluster,
		Fix:  fixMissingInCluster(row),
	})
}

// isSecretChannel reports whether the source kind represents a
// projection channel where the literal value isn't visible to a
// static check.
func isSecretChannel(s parityValueSource) bool {
	switch s {
	case paritySecretsFile, parityKCLSecretRef, parityKCLConfigMapRef, paritySecretRefPlaceholder:
		return true
	}
	return false
}

// fixValueMismatch suggests aligning the two sources for an
// inline-value disagreement.
func fixValueMismatch(row parityKey) string {
	return fmt.Sprintf("align %s with the %s value (both sides set the key inline but disagree).",
		row.Host.SourceLabel, row.Cluster.SourceLabel)
}

// fixMissingInHost suggests where to add the missing key on the
// host side.
func fixMissingInHost(row parityKey) string {
	return fmt.Sprintf("host-mode is missing the %s set in %s. Add it to forge.yaml environments[].config OR to the service's HostDeploy.env_vars.",
		row.Name, row.Cluster.SourceLabel)
}

// fixMissingInCluster suggests where to add the missing key on the
// cluster side.
func fixMissingInCluster(row parityKey) string {
	return fmt.Sprintf("cluster-mode is missing the %s set in %s. Add it to the KCL K8sCluster.env_vars block for this service.",
		row.Name, row.Host.SourceLabel)
}

// fixSecretChannel notes the expected nature of the divergence and
// reminds the user to verify the key name lines up across channels.
func fixSecretChannel(row parityKey) string {
	return fmt.Sprintf("secret-channel divergence is expected; verify the %s value matches the key the %s projects.",
		row.Host.SourceLabel, row.Cluster.SourceLabel)
}

// -- cobra wiring -----------------------------------------------------------

// newDoctorParityCmd returns the `forge doctor parity <service>`
// subcommand. The subcommand assembles parityInputs from the same
// real loaders host-launch + deploy use, calls diffParity, and emits
// the result either as a human-readable report (stderr) or a JSON
// document (stdout). Returns a non-nil error iff bug-class
// divergences exist — cobra's outer wrapper turns that into exit 1.
func newDoctorParityCmd() *cobra.Command {
	var (
		env        string
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "parity <service>",
		Short: "Diff a service's host-mode vs cluster-mode env+config",
		Long: `Compare what env vars and config a named service WOULD see in host-mode
(forge run <svc>) vs cluster-mode (forge env deploy <env>) projection.

Surfaces "local wasn't representative of prod" divergences statically
without running anything — no docker, no kubectl, no Secret reads.

Exits 1 when bug-class divergences exist (value mismatch, missing key
without a secret-channel explanation). Exits 0 when the only
divergences are secret-channel (host secrets_file ↔ cluster secret_ref —
expected by design).

Examples:
  forge doctor parity tasks                # default --env=dev
  forge doctor parity tasks --env=staging
  forge doctor parity tasks --json         # machine-readable output`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctorParity(cmd.Context(), args[0], env, jsonOutput, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&env, "env", "dev", "Environment to compare (dev, staging, prod)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit a machine-readable JSON document to stdout")
	return cmd
}

// runDoctorParity is the cobra-side entry point. Assembles
// parityInputs from real loaders, calls diffParity, prints the
// report. Split from the cobra command body so it can be exercised by
// tests without a *cobra.Command.
func runDoctorParity(ctx context.Context, serviceName, env string, jsonOutput bool, stdout, stderr io.Writer) error {
	const ctxLabel = "forge doctor parity"

	// Gate: this must run inside a forge project. We no longer need the
	// parsed config here (the component inventory comes from
	// codegen.IntrospectComponents), so we only assert loadability.
	if _, err := loadProjectStore(); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "load forge.yaml", "", "run from inside a forge project (forge.yaml must be present)", err)
	}

	projectPath, err := findProjectConfigFile()
	if err != nil {
		return cliutil.WrapUserErr(ctxLabel, "locate forge.yaml", "", "", err)
	}
	projectDir := filepath.Dir(projectPath)

	// Inventory is enumerated from the REAL sources (proto descriptor +
	// owned worker/operator files + cmd/ binaries) — see
	// codegen.IntrospectComponents.
	comps := codegen.IntrospectComponents(projectDir)
	if !serviceDeclared(comps, serviceName) {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("service %q not found; available: %s", serviceName, strings.Join(declaredServiceNames(comps), ", ")),
			"",
			fmt.Sprintf("check the spelling, or add the service with `forge scaffold service %s`", serviceName))
	}

	// Per-env KCL config (deploy/kcl/<env>/config.k) — both modes derive
	// from it. Soft-fail: a project that can't render its config is still
	// parity-checkable from the rendered KCL alone, so proceed with an empty
	// projection map on error.
	configSrcs, _ := loadProjectConfigEnvMap(projectDir, env)
	yamlVals := buildForgeConfigValues(configSrcs, env)

	// KCL render — gives us the host + cluster env_vars for the
	// named service.
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return cliutil.WrapUserErr(ctxLabel, "render KCL", "", "ensure deploy/kcl/<env>/ exists and `kcl` is on PATH", err)
	}

	hostKCL, clusterKCL, clusterSecret := extractKCLEnvVars(entities, serviceName)
	// Host secrets come from the env's secret provider, scoped by the same
	// secretRefs the cluster side projects, so there is no separate host
	// secrets file to point at.
	hostSecrets := map[string]parityValue{}

	in := parityInputs{
		Service:       serviceName,
		Env:           env,
		ForgeYAMLEnv:  yamlVals,
		HostKCL:       hostKCL,
		HostSecrets:   hostSecrets,
		ClusterKCL:    clusterKCL,
		ClusterSecret: clusterSecret,
	}
	report := diffParity(in)

	if jsonOutput {
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			return cliutil.WrapUserErr(ctxLabel, "encode JSON", "", "", err)
		}
	} else {
		printParityReport(stderr, report)
	}

	if len(report.bugDivergences()) > 0 {
		return errParityDivergent
	}
	return nil
}

// errParityDivergent is the sentinel returned when bug-class
// divergences exist. Cobra surfaces it via main.go's "Error: ..." line
// AND the process exits 1.
var errParityDivergent = fmt.Errorf("doctor parity reported divergences; see report above")

// serviceDeclared reports whether a component with the given name exists
// in the enumerated inventory (proto descriptor + owned worker/operator
// files + cmd/ binaries — see codegen.IntrospectComponents).
func serviceDeclared(comps []config.ComponentConfig, name string) bool {
	for _, c := range comps {
		if c.Name == name {
			return true
		}
	}
	return false
}

// buildForgeConfigValues turns the per-env KCL config projection
// (config_gen.appConfigEnvMap, ENV_VAR-keyed) into the
// key→parityValue projection both modes derive from. A plain value
// carries through as a resolved value; a forge.SecretRef entry is a
// secret-backed value — noted as secret_ref_placeholder rather than an
// inline value, since host-mode can't dereference it either.
func buildForgeConfigValues(srcs map[string]kclEnvSource, env string) map[string]parityValue {
	out := make(map[string]parityValue, len(srcs))
	label := fmt.Sprintf("deploy/kcl/%s/config.k", env)
	for name, src := range srcs {
		if src.Value != nil {
			out[name] = parityValue{
				Source:      parityForgeYAMLConfig,
				SourceLabel: label,
				Value:       *src.Value,
			}
			continue
		}
		// A forge.SecretRef: the host has a deferred
		// channel for it, but no literal to compare.
		out[name] = parityValue{
			Source:      paritySecretRefPlaceholder,
			SourceLabel: label + " (secret ref)",
			Value:       "",
		}
	}
	return out
}

// extractKCLEnvVars walks the rendered KCL entities for the named workload
// and projects its spec.env:
//   - inline values → BOTH hostKCL and clusterKCL (a workload declares one
//     env for every runtime, so the two sides agree by construction);
//   - secret_ref / config_map_ref → clusterSecret (host-side, a secret ref is
//     resolved from the provider, and a config map has no host projection).
//
// Returns empty maps (not nil) when the workload has no entries on a
// channel, so callers can pass results straight to diffParity.
func extractKCLEnvVars(entities *KCLEntities, serviceName string) (hostKCL, clusterKCL, clusterSecret map[string]parityValue) {
	hostKCL = map[string]parityValue{}
	clusterKCL = map[string]parityValue{}
	clusterSecret = map[string]parityValue{}
	w := entities.FindWorkload(serviceName)
	if w == nil {
		return
	}
	for _, ev := range w.EnvVars() {
		if ev.Name == "" {
			continue
		}
		switch {
		case ev.SecretRef != "":
			clusterSecret[ev.Name] = parityValue{
				Source:      parityKCLSecretRef,
				SourceLabel: fmt.Sprintf("KCL spec.env secretRef name=%s key=%s", ev.SecretRef, ev.SecretKey),
			}
		case ev.ConfigMapRef != "":
			clusterSecret[ev.Name] = parityValue{
				Source:      parityKCLConfigMapRef,
				SourceLabel: fmt.Sprintf("KCL spec.env configMapRef name=%s key=%s", ev.ConfigMapRef, ev.ConfigMapKey),
			}
		default:
			hostKCL[ev.Name] = parityValue{Source: parityHostKCLEnvVar, SourceLabel: "KCL spec.env", Value: ev.Value}
			clusterKCL[ev.Name] = parityValue{Source: parityClusterKCLEnvVar, SourceLabel: "KCL spec.env", Value: ev.Value}
		}
	}
	return
}

// printParityReport writes the human-readable report to w. Layout
// mirrors the spec:
//
//	forge doctor parity — service "tasks", env "dev"
//
//	Both modes agree on N keys.
//	  ...divergence rows...
//	Fix:
//	  - ...
//
// The check/warning marks are ASCII (no emoji) to keep terminal
// width predictable and copy-paste friendly.
func printParityReport(w io.Writer, report parityReport) {
	_, _ = fmt.Fprintf(w, "\nforge doctor parity - service %q, env %q\n\n", report.Service, report.Env)

	if len(report.Divergences) == 0 {
		_, _ = fmt.Fprintf(w, "  OK: both modes agree on %d keys.\n\n", len(report.Agree))
		return
	}
	_, _ = fmt.Fprintf(w, "  Both modes agree on %d keys.\n\n", len(report.Agree))

	bug := report.bugDivergences()
	expected := len(report.Divergences) - len(bug)
	if len(bug) > 0 {
		_, _ = fmt.Fprintf(w, "  %d bug-class divergence(s):\n\n", len(bug))
	}
	for _, d := range bug {
		printDivergenceRow(w, d)
	}
	if expected > 0 {
		_, _ = fmt.Fprintf(w, "  %d expected (secret-channel) divergence(s):\n\n", expected)
		for _, d := range report.Divergences {
			if d.Kind != paritySecretChannelDiverg {
				continue
			}
			printDivergenceRow(w, d)
		}
	}

	_, _ = fmt.Fprintln(w, "Fix:")
	for _, d := range report.Divergences {
		_, _ = fmt.Fprintf(w, "  - %s: %s\n", d.Key.Name, d.Fix)
	}
	_, _ = fmt.Fprintln(w)
}

// printDivergenceRow renders a single divergence under the report.
func printDivergenceRow(w io.Writer, d parityDivergence) {
	_, _ = fmt.Fprintf(w, "  %s\n", d.Key.Name)
	_, _ = fmt.Fprintf(w, "    host:    %s    (source: %s)\n", displayValue(d.Key.Host), d.Key.Host.SourceLabel)
	_, _ = fmt.Fprintf(w, "    cluster: %s    (source: %s)\n\n", displayValue(d.Key.Cluster), d.Key.Cluster.SourceLabel)
}

// displayValue picks the right surface-form for a parityValue in the
// human-readable report. Empty values become "<unset>" / a projection
// note rather than blank columns.
func displayValue(v parityValue) string {
	switch v.Source {
	case parityUnset:
		return "<unset>"
	case parityKCLSecretRef:
		return "<projected from Secret>"
	case parityKCLConfigMapRef:
		return "<projected from ConfigMap>"
	case paritySecretsFile:
		return "<from secrets_file>"
	case paritySecretRefPlaceholder:
		if v.Value != "" {
			return v.Value
		}
		return "<deferred secret ref>"
	default:
		return v.Value
	}
}
