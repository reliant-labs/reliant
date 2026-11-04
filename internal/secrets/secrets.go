// Package secrets resolves declared secret REFERENCES (which live in
// git: EnvVar.secret_ref / secret_key) to secret VALUES (which never
// live in git) for one environment.
//
// A secret has two halves:
//
//   - a non-sensitive REFERENCE — the env-var NAME, the k8s Secret name,
//     and the key within it. KCL projects this into the manifest as a
//     `secretKeyRef`. It is reproducible and version-controlled.
//   - a sensitive VALUE — obtained at resolve time from a per-env
//     PROVIDER. Never in git, never in KCL render output.
//
// This package owns the VALUE side. KCL only emits the provider
// DECLARATION (type + path); all value resolution happens here in Go so
// secrets never enter the KCL renderer.
//
// Provider kinds:
//
//   - "file" (dev/local): forge reads a gitignored YAML file — a flat map
//     of env-var name to value. Local clusters only. This is the
//     scaffolded default; see fileProvider for why it replaced the dotenv.
//   - "dotenv": REMOVED. It injected the whole file into every host
//     service, so values never had to be declared. NewProvider returns a
//     hard error naming `forge secret migrate`.
//   - "hosted": values live in the env's hosted control plane (write-only
//     API) and are materialized in-cluster by it. forge never sees them;
//     `forge secret set/list/unset` write/list through the control plane.
//   - "external" (prod/staging): forge never sees values. k8s references
//     pre-existing Secrets (External Secrets Operator / sealed); host &
//     external runtimes obtain secrets via workload identity / ambient
//     env. forge only validates the secretKeyRef wiring (it can't, and
//     so does not, validate the values themselves).
//
// The package is intentionally decoupled from internal/cli to avoid an
// import cycle (cli depends on secrets, not the reverse). It reuses
// only the stdlib for its own file reads — no cycle risk.
//
//forge:lint-disable-next-line forge-exclude-contract-multi-impl: Provider (file/external/hosted/layered/noop, chosen by NewProvider) IS a strategy contract; moving it to contract.go is tracked as CONTRACTS follow-up F2
//forge:exclude-contract: secret-reference → value resolution for the CLI
package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Provider resolves declared secret references to values for one env.
type Provider interface {
	Kind() string // "file" | "external" | "hosted" | "none"
	// Resolve returns the value for an env var by NAME (the key in the
	// store == the EnvVar.name). ok=false
	// when this provider has no value for name.
	Resolve(name string) (value string, ok bool)
	// All returns every value the provider can supply, keyed by name.
	// file: the whole store. external/none: nil.
	All() map[string]string
}

// ProviderConfig is the cli-decoupled view of the KCL secret_provider
// entity. (cli maps KCLEntities.SecretProvider -> this.)
type ProviderConfig struct {
	Type string // "file" | "external"
	Path string // secret-store file (already resolved to an absolute/project path by caller)
	// SharedPath is the same store resolved against the repo anchor (the
	// primary checkout) when Path is inside a LINKED git worktree. It is
	// the BASE layer: Path overrides it key by key. Empty on the primary
	// checkout, outside a repo, or when it would equal Path.
	//
	// See layeredProvider for why this is a merge and not a fallback.
	SharedPath string
}

// NewProvider builds a Provider. cfg==nil -> a noop provider (Kind
// "none", All nil, Resolve always !ok) so callers need no nil checks.
// file: loads the store now; a MISSING file is a non-fatal
// file: loads the store now; a MISSING file is a non-fatal empty
// provider (so validation, not load, reports the missing keys with the
// `forge secret set` fix) — BUT an unreadable/invalid store is an error.
func NewProvider(cfg *ProviderConfig) (Provider, error) {
	if cfg == nil {
		return noopProvider{}, nil
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "external":
		return externalProvider{}, nil
	case "hosted":
		return hostedProvider{}, nil
	case "file":
		local, err := readStoreAllowingMissing(cfg.Path)
		if err != nil {
			return nil, err
		}
		if cfg.SharedPath == "" || cfg.SharedPath == cfg.Path {
			return fileProvider{values: local, path: cfg.Path}, nil
		}
		shared, err := readStoreAllowingMissing(cfg.SharedPath)
		if err != nil {
			return nil, err
		}
		return newLayeredProvider(shared, local, cfg.SharedPath, cfg.Path), nil
	case "", "none":
		return noopProvider{}, nil
	case "dotenv":
		// REMOVED. The dotenv provider handed every host service the whole
		// file, so a value went live without ever being declared in KCL —
		// which is how non-secret config drifted out of version control.
		// This is a hard error rather than a silent fallback: a project
		// still declaring it would otherwise boot with NO secrets and fail
		// later, somewhere less obvious.
		return nil, fmt.Errorf(
			"secret_provider 'dotenv' has been removed\n"+
				"fix: forge secret migrate --env <env>   (converts %s to secrets/<env>.yaml)\n"+
				"     then declare `secret_provider = forge.FileSecrets {path = \"secrets/<env>.yaml\"}`",
			cfg.Path)
	default:
		return nil, fmt.Errorf("unknown secret provider type %q (expected \"file\", \"external\" or \"hosted\")", cfg.Type)
	}
}

// noopProvider is the absent-provider case: nothing to resolve.
type noopProvider struct{}

func (noopProvider) Kind() string                  { return "none" }
func (noopProvider) Resolve(string) (string, bool) { return "", false }
func (noopProvider) All() map[string]string        { return nil }

// externalProvider declares values come from outside forge's view. forge
// validates wiring only; it never resolves a value.
type externalProvider struct{}

func (externalProvider) Kind() string                  { return "external" }
func (externalProvider) Resolve(string) (string, bool) { return "", false }
func (externalProvider) All() map[string]string        { return nil }

// hostedProvider declares that values live in the hosted control plane's
// secret store (the env's forge.ControlPlane) and are MATERIALIZED IN-CLUSTER
// by that control plane, into the Secret every managedSecret env var reads.
//
// It resolves NOTHING, exactly like externalProvider, and that is the design
// rather than an unfinished feature: the store's API is write-only (no RPC
// returns a value), so no forge process — a developer laptop, a CI runner —
// ever holds a hosted value. Writes (`forge secret set --env <hosted-env>`) go through
// a separate writer the CLI declares at its call site; this type is only the
// resolve half, and it must stay value-free.
type hostedProvider struct{}

func (hostedProvider) Kind() string                  { return "hosted" }
func (hostedProvider) Resolve(string) (string, bool) { return "", false }
func (hostedProvider) All() map[string]string        { return nil }

// pulledProvider is the hosted provider of a LOCAL environment: one whose
// workloads run on a developer machine (`forge env up`) and whose control
// plane is only its secret store. Such an env's values are PULLABLE — the
// control plane serves them to the env's own org over a separately-scoped
// read path that is structurally confined to local environments — so here,
// unlike hostedProvider, forge holds values.
//
// IN MEMORY ONLY. The values arrive over the wire, are injected into the
// processes `forge env up` launches, and are never written to disk or logged
// by this package: there is no Write, no String, and no path.
type pulledProvider struct {
	values map[string]string
}

// NewPulledProvider wraps the values a LOCAL environment's control plane
// served. Kind is "hosted" (that is where the values live); it resolves them.
func NewPulledProvider(values map[string]string) Provider {
	cp := make(map[string]string, len(values))
	for k, v := range values {
		cp[k] = v
	}
	return pulledProvider{values: cp}
}

func (pulledProvider) Kind() string { return "hosted" }

func (p pulledProvider) Resolve(name string) (string, bool) {
	v, ok := p.values[name]
	return v, ok
}

func (p pulledProvider) All() map[string]string { return p.values }

func (pulledProvider) resolvesValues() bool { return true }
func (fileProvider) resolvesValues() bool   { return true }
func (layeredProvider) resolvesValues() bool {
	return true
}

// ResolvesValues reports whether p holds real values forge can validate
// declarations against and inject: a file store, or a LOCAL env's pulled
// hosted store. external / none / a persistent hosted env resolve nothing.
//
// Declared as an unexported marker rather than a Kind() comparison because a
// hosted provider may or may not hold values (local vs persistent env), so
// the kind name alone cannot answer it.
func ResolvesValues(p Provider) bool {
	r, ok := p.(interface{ resolvesValues() bool })
	return ok && r.resolvesValues()
}

// fileProvider resolves values from a single gitignored YAML file: a flat
// map of env-var NAME -> value.
//
// One file, not a file-per-secret and not a dotenv. The dotenv was
// replaced because forge handed the WHOLE file to every host service, so a
// value went live without ever being declared in KCL — that is a property
// of the injection, not of the format, and it is fixed by
// declaration-scoped injection (see scopeSecretsToService), not by the
// on-disk shape. YAML then buys what a dotenv cannot: real quoting, and
// multi-line values (a PEM key, a JSON service-account blob) without
// escaping games.
type fileProvider struct {
	values map[string]string
	path   string
}

func (f fileProvider) Kind() string { return "file" }

func (f fileProvider) Resolve(name string) (string, bool) {
	v, ok := f.values[name]
	return v, ok
}

func (f fileProvider) All() map[string]string { return f.values }

// readStoreAllowingMissing loads a store, treating a MISSING file as empty
// (the fresh-clone / fresh-worktree case) but an unreadable or malformed
// one as fatal. A missing store is reported later by ValidateDeclaredRefs,
// naming the exact keys and the fix — far more useful than a bare stat
// error at load time.
func readStoreAllowingMissing(path string) (map[string]string, error) {
	values, err := ReadSecretFile(path)
	if err == nil {
		return values, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	return nil, fmt.Errorf("load secret file %q: %w", path, err)
}

// layeredProvider resolves a linked git worktree's secrets from TWO stores:
// the primary checkout's (shared, the base) with the worktree's own store
// (local) overriding it PER KEY.
//
// WHY THIS EXISTS. The store is gitignored, by design — it holds real
// credentials. But `git worktree add` only materializes TRACKED files, so
// every new worktree begins with no store at all and forge fail-fasts on
// every declared secret_ref. The developer has already set those values;
// they are simply in the other checkout. That made a new worktree unusable
// until ~30 `forge secret set` calls had been replayed by hand, which is
// the same class of bug the port-block registry hit and fixed by anchoring
// on the primary checkout (see devstack.RepoAnchor).
//
// WHY PER-KEY MERGE, NOT WHOLE-FILE FALLBACK. A worktree that wants its
// own Stripe test account should be able to override THAT ONE KEY and keep
// inheriting the other twenty-seven. Under whole-file fallback the first
// `forge secret set` in a worktree creates a one-key file that shadows the
// shared store entirely, and the remaining secrets break in a way that
// looks nothing like its cause. Merging makes the override granular and
// makes `set` safe by construction.
//
// The layering applies ONLY to a file provider, which forge already
// restricts to dev/e2e — it can never affect staging or prod.
type layeredProvider struct {
	merged     map[string]string
	sharedPath string
	localPath  string
	inherited  int // keys taken from shared and NOT overridden locally
	overridden int // keys present in both, where local won
}

func newLayeredProvider(shared, local map[string]string, sharedPath, localPath string) layeredProvider {
	merged := make(map[string]string, len(shared)+len(local))
	for k, v := range shared {
		merged[k] = v
	}
	var overridden int
	for k, v := range local {
		if _, inShared := shared[k]; inShared {
			overridden++
		}
		merged[k] = v
	}
	return layeredProvider{
		merged:     merged,
		sharedPath: sharedPath,
		localPath:  localPath,
		inherited:  len(shared) - overridden,
		overridden: overridden,
	}
}

func (l layeredProvider) Kind() string { return "file" }

func (l layeredProvider) Resolve(name string) (string, bool) {
	v, ok := l.merged[name]
	return v, ok
}

func (l layeredProvider) All() map[string]string { return l.merged }

// Layering reports where this provider's values came from, so `forge env up`
// can say so out loud. Inheriting another directory's credentials silently
// would be its own surprise — the declared KCL path says "secrets/dev.yaml",
// and a developer must be able to see when that resolved elsewhere.
func (l layeredProvider) Layering() (sharedPath, localPath string, inherited, overridden int) {
	return l.sharedPath, l.localPath, l.inherited, l.overridden
}

// Layered is implemented by a provider that merged more than one store.
// Declared at the CONSUMER (the CLI prints the notice) rather than exported
// as a wide provider interface — Provider stays three methods.
type Layered interface {
	Layering() (sharedPath, localPath string, inherited, overridden int)
}

// ReadSecretFile loads the YAML secret file: a flat mapping of env-var
// name to value.
//
// Values are decoded as strings, so `PORT: 8080` and `DEBUG: true` yield
// "8080" and "true" rather than a type error — an env var is a string, and
// failing on an unquoted number would be a papercut with no upside.
// Nested maps and sequences ARE an error: they cannot become an env var,
// so accepting them silently would leave a value that looks set but never
// arrives.
func ReadSecretFile(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	values := make(map[string]string, len(doc))
	var bad []string
	for k, v := range doc {
		if !ValidSecretKey(k) {
			bad = append(bad, fmt.Sprintf("%s (not a valid env-var name)", k))
			continue
		}
		switch tv := v.(type) {
		case nil:
			values[k] = ""
		case string:
			values[k] = tv
		case bool, int, int64, float64:
			values[k] = fmt.Sprintf("%v", tv)
		default:
			bad = append(bad, fmt.Sprintf("%s (%T is not a scalar)", k, v))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, fmt.Errorf(
			"%s has %d unusable entr(ies): %s\n"+
				"fix: every key must be an UPPER_SNAKE_CASE env-var name mapped to a scalar",
			path, len(bad), strings.Join(bad, ", "))
	}
	return values, nil
}

// ValidSecretKey reports whether name is usable as an env-var name:
// [A-Za-z_][A-Za-z0-9_]*. Shared with the CLI so `forge secret set`
// rejects the same names the loader would.
func ValidSecretKey(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// WriteSecretFile writes the map back as YAML with sorted keys, 0600.
// Sorted so a re-write is a clean diff rather than map-iteration noise.
func WriteSecretFile(path string, values map[string]string) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("# forge secret store — GITIGNORED, never commit.\n")
	b.WriteString("# Managed by `forge secret set/unset`; hand-edits are fine too.\n")
	b.WriteString("# Keys must match an EnvVar.secret_ref declared in KCL, or the value\n")
	b.WriteString("# reaches nothing (`forge secret list --env <env>` reports unused keys).\n")
	for _, k := range keys {
		out, err := yaml.Marshal(map[string]string{k: values[k]})
		if err != nil {
			return fmt.Errorf("marshal %s: %w", k, err)
		}
		b.Write(out)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.WriteFile(path, []byte(b.String()), 0o600)
}

// SecretRef is a declared reference extracted from the entities: the
// env-var NAME (== the secret's filename in the store), the k8s Secret
// NAME, and the key within that Secret. SecretKey defaults to EnvName
// when empty (matches the KCL _env_source lambda).
type SecretRef struct {
	EnvName    string
	SecretName string
	SecretKey  string

	// Optional exempts this ref from the missing-value check, and it is set
	// ONLY from the proto's `optional: true` on a sensitive config field.
	//
	// The default — a declared secret must have a value — is the whole point
	// of the pre-flight, so this is deliberately not inferrable from anything
	// forge can observe about the environment. An unset credential and a
	// deliberately-absent one look identical in the store; the annotation is
	// the author saying which it is.
	Optional bool
}

// key returns the in-Secret key, defaulting to EnvName when unset.
func (r SecretRef) key() string {
	if r.SecretKey != "" {
		return r.SecretKey
	}
	return r.EnvName
}

// ValidateDeclaredRefs returns a single fail-fast error listing every
// declared ref the provider cannot supply. For Kind "file": each
// EnvName must be present in All(). For "external"/"none": returns nil
// (forge cannot see those values).
func ValidateDeclaredRefs(p Provider, refs []SecretRef, storePath string) error {
	missing := MissingDeclaredRefs(p, refs)
	if len(missing) == 0 {
		return nil
	}
	return MissingRefsError(p, missing, storePath)
}

// MissingDeclaredRefs lists the declared, non-optional refs a
// value-resolving provider cannot supply, one per env-var name, sorted by
// name. Nil when there are none, or when the provider cannot see values
// (external/none), so a read-only caller can REPORT what is missing where
// ValidateDeclaredRefs refuses.
func MissingDeclaredRefs(p Provider, refs []SecretRef) []SecretRef {
	if p == nil {
		return nil
	}
	// Only value-resolving providers can be validated: external/none (and a
	// persistent hosted env) deliberately cannot see values, so there is
	// nothing to check.
	if !ResolvesValues(p) {
		return nil
	}
	values := p.All()
	// De-duplicate by env-var NAME. Refs are collected per SERVICE, so a
	// secret three services declare produced three identical lines — a
	// project with ten missing secrets reported "28 missing value(s)" and
	// listed GITHUB_CLIENT_SECRET three times, which reads as a much
	// bigger problem than it is. The unit a developer acts on is the KEY:
	// one `forge secret set` fixes every service that declares it.
	var missing []SecretRef
	seen := map[string]bool{}
	for _, r := range refs {
		if r.EnvName == "" || seen[r.EnvName] || r.Optional {
			continue
		}
		if _, ok := values[r.EnvName]; !ok {
			seen[r.EnvName] = true
			missing = append(missing, r)
		}
	}
	// Deterministic ordering so every report of it is stable across runs.
	sort.Slice(missing, func(i, j int) bool { return missing[i].EnvName < missing[j].EnvName })
	return missing
}

// Key returns the in-Secret key the ref reads, defaulting to EnvName.
func (r SecretRef) Key() string { return r.key() }

// MissingRefsError is ValidateDeclaredRefs' refusal for refs already found
// missing: the aligned list and the command that fixes it.
func MissingRefsError(p Provider, missing []SecretRef, storePath string) error {
	// Column-align the env names for a readable list.
	width := 0
	for _, r := range missing {
		if len(r.EnvName) > width {
			width = len(r.EnvName)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "secret provider %q (%s) is missing %d declared value(s):\n", p.Kind(), storePath, len(missing))
	for _, r := range missing {
		fmt.Fprintf(&b, "    %-*s   (Secret %s/%s)\n", width, r.EnvName, r.SecretName, r.key())
	}
	// The fix line names the command — the whole point of the store is that
	// adding a secret is a forge command, not a hand-edit of a file that
	// gets injected everywhere.
	fmt.Fprintf(&b, "fix: forge secret set --env <env> <KEY>   (writes into %s)\n", storePath)
	fmt.Fprint(&b, "     …or remove the secret_ref from the EnvVar if it is no longer needed.")
	return errors.New(b.String())
}

// DeclaredSecret is one explicitly-declared k8s Secret a RenderedSecrets
// provider renders: a Secret NAME and a map of in-Secret key -> value
// SOURCE. Mirrors the cli RenderedSecretEntity (the secrets package stays
// decoupled from cli, so cli maps its entity -> this).
type DeclaredSecret struct {
	Name string
	Keys map[string]DeclaredSecretKey
}

// DeclaredSecretKey is the value source for one key in a DeclaredSecret.
// From is "file" (resolve Key from the secret store) or "literal"
// (inline Value, gated to dev/e2e by RenderDeclaredSecrets).
type DeclaredSecretKey struct {
	From  string
	Key   string
	Value string
}

// envAllowsLiteral reports whether `from='literal'` inline values are
// permitted for env. The Go-side guard mirroring the KCL check: only
// dev/e2e may inline a secret value; every other env resolves from the
// store. Defense in depth — a hand-built entity (no KCL check) can't
// smuggle a literal into prod.
func envAllowsLiteral(env string) bool {
	return env == "dev" || env == "e2e"
}

// RenderDeclaredSecrets builds k8s Secret manifests from a
// RenderedSecrets provider's declared Secrets. Each declared key resolves
// to its value:
//
//   - from="file": resolve Key from the env's secret store `dot`. A key
//     with no value is an error (listed per Secret/key) — the value can't
//     be rendered.
//   - from="literal": use the inline Value, but ONLY when env is dev/e2e.
//     A literal in any other env is a hard error (the trust-safe gate).
//
// Returns the Secret manifests (one per declared Secret) in deterministic
// order, or a single aggregated error listing every problem. namespace is
// stamped on each Secret's metadata.
func RenderDeclaredSecrets(declared []DeclaredSecret, dot Provider, env, namespace string) ([]map[string]any, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	// Deterministic Secret order.
	byName := make(map[string]DeclaredSecret, len(declared))
	names := make([]string, 0, len(declared))
	for _, d := range declared {
		byName[d.Name] = d
		names = append(names, d.Name)
	}
	sort.Strings(names)

	var problems []string
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		d := byName[name]
		sd := map[string]any{}
		// Deterministic key order within a Secret.
		keys := make([]string, 0, len(d.Keys))
		for k := range d.Keys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			src := d.Keys[k]
			switch strings.ToLower(strings.TrimSpace(src.From)) {
			case "literal":
				if !envAllowsLiteral(env) {
					problems = append(problems, fmt.Sprintf(
						"Secret %s/%s: from='literal' is only allowed in dev/e2e (env %q must use from='file')",
						name, k, env))
					continue
				}
				sd[k] = src.Value
			// Resolve Key from the env's secret store.
			case "file", "":
				srcKey := src.Key
				if srcKey == "" {
					srcKey = k
				}
				v, ok := dot.Resolve(srcKey)
				if !ok {
					problems = append(problems, fmt.Sprintf(
						"Secret %s/%s: secret store key %q not found", name, k, srcKey))
					continue
				}
				sd[k] = v
			default:
				problems = append(problems, fmt.Sprintf(
					"Secret %s/%s: unknown source %q (expected 'file' or 'literal')", name, k, src.From))
			}
		}
		out = append(out, map[string]any{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
			"type":       "Opaque",
			"stringData": sd,
		})
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("rendered secrets: %d problem(s):\n    %s",
			len(problems), strings.Join(problems, "\n    "))
	}
	return out, nil
}

// RenderK8sSecrets builds k8s Secret manifests (as []map[string]any,
// ready to marshal to YAML/JSON) from the resolved values, grouping refs
// by SecretName; each Secret's stringData[SecretKey] = resolved(EnvName).
// Only Kind "file" produces output; "external"/"none" return nil
// (prod references pre-existing Secrets). Skips refs whose value doesn't
// resolve (ValidateDeclaredRefs is the gate for those). Deterministic
// ordering (sorted Secret names + keys) for stable diffs.
func RenderK8sSecrets(p Provider, refs []SecretRef, namespace string) []map[string]any {
	if p == nil || !ResolvesValues(p) {
		return nil
	}
	// Group resolved (key -> value) pairs by Secret name.
	grouped := map[string]map[string]string{}
	for _, r := range refs {
		if r.SecretName == "" {
			continue
		}
		value, ok := p.Resolve(r.EnvName)
		if !ok {
			continue // ValidateDeclaredRefs is the gate; skip unresolved.
		}
		if grouped[r.SecretName] == nil {
			grouped[r.SecretName] = map[string]string{}
		}
		grouped[r.SecretName][r.key()] = value
	}
	if len(grouped) == 0 {
		return nil
	}
	// Sort Secret names for stable output.
	names := make([]string, 0, len(grouped))
	for name := range grouped {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		// stringData as map[string]any with sorted keys. Go marshals
		// maps with sorted keys for JSON, and the kcl/yaml emitters used
		// downstream sort too, so building a plain map is deterministic.
		sd := map[string]any{}
		for k, v := range grouped[name] {
			sd[k] = v
		}
		out = append(out, map[string]any{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
			"type":       "Opaque",
			"stringData": sd,
		})
	}
	return out
}
