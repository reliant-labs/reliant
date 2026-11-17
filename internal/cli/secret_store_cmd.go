package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/envutil"
	"github.com/reliant-labs/forge/internal/secrets"
)

// newSecretCmd is the `forge secret` group: the developer-facing way to
// put values into an env's FileSecrets store.
//
// The store is a single gitignored YAML file (env-var name -> value).
// These commands exist so adding a secret does not require knowing the
// file's shape, and so a value never lands in shell history — but the file
// is plain YAML, so hand-editing it is equally fine.
func newSecretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage an environment's secret store (local file or hosted control plane)",
		Long: `Manage the secret store an environment's secret_provider declares:

  forge.FileSecrets    the gitignored YAML store (a local env) — a flat map of
                       env-var NAME to value.
  forge.HostedSecrets  the env's control plane (control_plane). set / unset /
                       list go through its API. A hosted env's values are
                       never read back by these commands; a LOCAL env's
                       (control_plane with no hosted tier) are pulled into
                       memory by ` + "`forge env up`" + ` and nothing else.

A name that a store-backed ExternalSecret (forge.StoreSource) reads or
generates lives in that ExternalSecret's STORE instead, whatever the env's
secret_provider is, and set / list / import act on it there:

  forge.HostedStore             the hosted control plane's store — written
                                through its write-only API.
  forge.OpenBaoStore            the env's own OpenBao (Bundle.openbao). set
                                signs in as the forge-rendered
                                forge-secret-writer service account with a
                                ten-minute token minted from YOUR kube context,
                                so it needs cluster access and nothing else.
  forge.GCPSecretManagerStore   Google Secret Manager, with your Application
                                Default Credentials.

  forge secret allow / deny / access
                        a HOSTED secret's access list: which Reliant workflows
                        may use it ({{ secrets.NAME }} in an http/request
                        header). Hosted envs only.

  forge secret import   copy values that already exist into a store, create-only
                        (the first step of moving a Secret onto ESO)

Verifying and handing a Secret to ESO are 'forge env secrets verify' and
'forge env secrets adopt'.

Every command names its environment with a REQUIRED --env flag. There is no
default and no positional form: the env is the one thing a secret command
must never guess.

A secret is declared ONCE in KCL as a reference (EnvVar.secret_ref); its
value lives here and never enters git or KCL render output. A value only
reaches a service that DECLARES it, so putting something here that no
service references does nothing — config belongs in deploy/kcl/<env>/config.k.

  forge secret set   --env dev STRIPE_SECRET_KEY   # value on stdin; add, or replace (= rotate)
  forge secret unset --env dev STRIPE_SECRET_KEY
  forge secret list  --env dev                     # names + presence, never values
  forge secret allow --env prod --workflow trader-bot --project <id> TRADER_KEY   # hosted: let a workflow use it
  forge secret deny  --env prod --workflow trader-bot --project <id> TRADER_KEY
  forge secret access --env prod [TRADER_KEY]      # hosted: who may use it
  forge secret ensure --env dev                    # FileSecrets: create the file + report missing
  forge secret migrate --env dev                   # FileSecrets: convert a legacy .env file`,
	}
	cmd.AddCommand(
		newSecretSetCmd(),
		newSecretUnsetCmd(),
		newSecretImportCmd(),
		newSecretListCmd(),
		newSecretAllowCmd(),
		newSecretDenyCmd(),
		newSecretAccessCmd(),
		newSecretEnsureCmd(),
		newSecretMigrateCmd(),
	)
	return cmdutil.StrictGroup(cmd)
}

// addSecretEnvFlag registers the REQUIRED --env flag every secret command
// takes. Required, with no default and no positional spelling: a secret
// command aimed at the wrong environment writes a credential somewhere it
// must not be, and nothing about the command line would say so.
func addSecretEnvFlag(cmd *cobra.Command, env *string) {
	cmd.Flags().StringVar(env, "env", "", "Environment whose secret store to act on (required; deploy/kcl/<env>/)")
}

// requireSecretEnv turns a missing --env into the actionable error, naming
// the exact command to re-run. Checked in RunE (not MarkFlagRequired) so the
// message can carry the fix.
func requireSecretEnv(env, usage string) (string, error) {
	env = strings.TrimSpace(env)
	if env == "" {
		return "", fmt.Errorf("--env is required: name the environment whose secret store to use\n"+
			"fix: forge secret %s\n"+
			"(there is no default environment, on purpose — a secret aimed at the wrong env is a leaked credential)", usage)
	}
	return env, nil
}

func newSecretSetCmd() *cobra.Command {
	var fromFile, env string
	var store storeOptions
	cmd := &cobra.Command{
		Use:   "set --env <environment> <KEY>",
		Short: "Add or replace one secret value (read from stdin)",
		Args:  cobra.ExactArgs(1),
		Long: `Set a single secret in the environment's secret store. Setting a key that
already has a value REPLACES it — for a hosted store that is a new version,
which is how a secret is rotated (there is no separate rotate command).

The VALUE is read from stdin, never from argv — an argv value would land
in shell history and in the process table. Pipe it, or type it and press
Ctrl-D:

  printf '%s' "$TOKEN" | forge secret set --env dev STRIPE_SECRET_KEY
  forge secret set --env dev TLS_KEY --from-file ./key.pem

A trailing newline is trimmed. Multi-line values (a PEM key, a JSON blob)
round-trip unchanged.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			envName, err := requireSecretEnv(env, "set --env <env> "+args[0])
			if err != nil {
				return err
			}
			return runSecretSetWith(cmd.Context(), envName, args[0], store, fromFile, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	addSecretEnvFlag(cmd, &env)
	cmd.Flags().StringVar(&fromFile, "from-file", "", "Read the value from a file instead of stdin")
	addStoreFlags(cmd, &store)
	return cmd
}

func newSecretUnsetCmd() *cobra.Command {
	var env string
	cmd := &cobra.Command{
		Use:   "unset --env <environment> <KEY>",
		Short: "Remove one secret from the store",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			envName, err := requireSecretEnv(env, "unset --env <env> "+args[0])
			if err != nil {
				return err
			}
			return runSecretUnset(cmd.Context(), envName, args[0], cmd.OutOrStdout())
		},
	}
	addSecretEnvFlag(cmd, &env)
	return cmd
}

func newSecretListCmd() *cobra.Command {
	var (
		jsonOut bool
		env     string
	)
	cmd := &cobra.Command{
		Use:   "list --env <environment>",
		Short: "List declared secrets and whether each has a value",
		Args:  cobra.NoArgs,
		Long: `List every secret the environment's KCL declares, and whether the store
holds a value for it. Values are NEVER printed.

Works for every secret_provider:

  file      presence read from the YAML store; inert (undeclared) keys listed.
  hosted    names and current versions from the control plane's store.
  external  the declarations only — presence is "unknown": forge cannot see
            a Secret provisioned out of band.
  rendered  the declared Secrets' keys, and whether each resolves from its
            declared source.
  none      the declarations only; nothing can supply a value.

--json emits the same facts as a machine-readable document, and holds the
same promise: the report has no field capable of carrying a value.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			envName, err := requireSecretEnv(env, "list --env <env>")
			if err != nil {
				return err
			}
			if jsonOut {
				return runSecretListJSON(cmd.Context(), envName, cmd.OutOrStdout())
			}
			return runSecretList(cmd.Context(), envName, cmd.OutOrStdout())
		},
	}
	addSecretEnvFlag(cmd, &env)
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable JSON (names/presence/versions/declaring workloads/inert keys — never values)")
	return cmd
}

func newSecretEnsureCmd() *cobra.Command {
	var env string
	cmd := &cobra.Command{
		Use:   "ensure --env <environment>",
		Short: "Create the FileSecrets store and report missing values",
		Args:  cobra.NoArgs,
		Long: `Create the environment's FileSecrets store (0600) if absent and list every
declared secret that has no value yet.

Secrets the provider declares in FileSecrets.generate (pure random key
material: an encryption key, a session secret) are minted when absent, never
overwritten, and reported by name only. Rotating one is a manual
'forge secret set'. Hosted and external providers never generate.

Exits non-zero when a declared secret is missing a value, so it works as a
setup gate in a task/Makefile before 'forge env up'.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			envName, err := requireSecretEnv(env, "ensure --env <env>")
			if err != nil {
				return err
			}
			return runSecretEnsure(cmd.Context(), envName, cmd.OutOrStdout())
		},
	}
	addSecretEnvFlag(cmd, &env)
	return cmd
}

func newSecretMigrateCmd() *cobra.Command {
	var (
		dryRun bool
		env    string
	)
	cmd := &cobra.Command{
		Use:   "migrate --env <environment>",
		Short: "Convert a legacy .env secrets file into the YAML store",
		Args:  cobra.NoArgs,
		Long: `Convert a legacy dotenv into the FileSecrets YAML store, then delete the
original. Run with --dry-run first to see exactly which keys move.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			envName, err := requireSecretEnv(env, "migrate --env <env>")
			if err != nil {
				return err
			}
			return runSecretMigrate(cmd.Context(), envName, dryRun, cmd.OutOrStdout())
		},
	}
	addSecretEnvFlag(cmd, &env)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would move without writing anything")
	return cmd
}

// secretStorePath resolves the env's secret file from its KCL provider
// declaration. It requires a FileSecrets provider: set/unset/ensure manage
// that store specifically, and pointing them elsewhere would silently write a
// file nothing reads.
func secretStorePath(ctx context.Context, envName string) (string, *KCLEntities, error) {
	entities, err := renderEntitiesForSecrets(ctx, envName)
	if err != nil {
		return "", nil, fmt.Errorf("render KCL: %w", err)
	}
	path, err := fileSecretStorePath(envName, entities)
	return path, entities, err
}

// fileSecretStorePath is secretStorePath's render-free half.
func fileSecretStorePath(envName string, entities *KCLEntities) (string, error) {
	projectDir := projectDirForKCL()
	sp := entities.SecretProvider
	if sp == nil {
		return "", fmt.Errorf(
			"env %q declares no secret_provider\n"+
				"fix: add `secret_provider = forge.FileSecrets {path = \"secrets/%s.yaml\"}` to the Bundle in deploy/kcl/%s/main.k",
			envName, envName, envName)
	}
	if sp.Type != "file" {
		return "", fmt.Errorf(
			"env %q declares a %q secret_provider, not FileSecrets — this command manages the file store only\n"+
				"fix: forge secret migrate --env %s   (converts a legacy .env, then switch the KCL to forge.FileSecrets)",
			envName, sp.Type, envName)
	}
	path := sp.Path
	if path == "" {
		path = filepath.Join("secrets", envName+".yaml")
	}
	if !filepath.IsAbs(path) {
		if shared := sharedSecretStorePath(projectDir, path); shared != "" {
			// In a linked worktree, the store that actually holds the
			// values is usually the primary checkout's. Write THERE by
			// default, so `forge secret set` in a worktree updates the
			// store every checkout reads instead of creating a local
			// one-key file that shadows it per key. A worktree that
			// genuinely wants its own value creates the local file
			// deliberately — and once it exists, it wins.
			if _, err := os.Stat(filepath.Join(projectDir, path)); err != nil {
				if _, sharedErr := os.Stat(shared); sharedErr == nil {
					return shared, nil
				}
			}
		}
		path = filepath.Join(projectDir, path)
	}
	return path, nil
}

// loadStore reads the store, treating a missing file as empty so `set` and
// `ensure` work on a fresh clone.
func loadStore(path string) (map[string]string, error) {
	values, err := secrets.ReadSecretFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	return values, nil
}

// hostedEntitiesFor renders the env's KCL and returns it when the env declares
// forge.HostedSecrets, or nil when it does not. A render failure is returned
// so the caller does not silently fall through to the file path.
//
// renderEntitiesForSecrets is a seam: tests substitute entities instead of
// rendering a real project.
var renderEntitiesForSecrets = func(ctx context.Context, envName string) (*KCLEntities, error) {
	return RenderKCL(ctx, projectDirForKCL(), envName)
}

func hostedEntitiesFor(ctx context.Context, envName string) (*KCLEntities, error) {
	entities, err := renderEntitiesForSecrets(ctx, envName)
	if err != nil {
		return nil, fmt.Errorf("render KCL: %w", err)
	}
	if isHostedSecretEnv(entities) {
		return entities, nil
	}
	return nil, nil
}

func readSecretValue(fromFile string, stdin io.Reader) (string, error) {
	var (
		raw []byte
		err error
	)
	if fromFile != "" {
		raw, err = os.ReadFile(fromFile)
		if err != nil {
			return "", fmt.Errorf("read --from-file: %w", err)
		}
	} else {
		raw, err = io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read value from stdin: %w", err)
		}
	}
	return strings.TrimRight(string(raw), "\r\n"), nil
}

func runSecretSet(ctx context.Context, envName, key, fromFile string, stdin io.Reader, out io.Writer) error {
	return runSecretSetWith(ctx, envName, key, storeOptions{}, fromFile, stdin, out)
}

func runSecretSetWith(ctx context.Context, envName, key string, store storeOptions, fromFile string, stdin io.Reader, out io.Writer) error {
	entities, err := renderEntitiesForSecrets(ctx, envName)
	if err != nil {
		if !secrets.ValidSecretKey(key) {
			return fmt.Errorf("%q is not a valid env-var name (want [A-Za-z_][A-Za-z0-9_]*)", key)
		}
		return fmt.Errorf("render KCL: %w", err)
	}
	// A name a store-backed ExternalSecret reads goes to that store, which
	// decides what a legal name is (Secret Manager ids allow '-').
	if handled, err := runStoreSourceSet(ctx, envName, key, entities, store, fromFile, stdin, out); handled || err != nil {
		return err
	}
	if !secrets.ValidSecretKey(key) {
		return fmt.Errorf("%q is not a valid env-var name (want [A-Za-z_][A-Za-z0-9_]*)", key)
	}
	var hosted *KCLEntities
	if isHostedSecretEnv(entities) {
		hosted = entities
	}
	if hosted != nil {
		value, err := readSecretValue(fromFile, stdin)
		if err != nil {
			return err
		}
		if value == "" {
			return fmt.Errorf(
				"refusing to write an empty value for %s\n"+
					"fix: pipe the value in, e.g.  printf '%%s' \"$TOKEN\" | forge secret set --env %s %s",
				key, envName, key)
		}
		return runHostedSecretSet(ctx, envName, key, hosted, value, out)
	}
	path, _, err := secretStorePath(ctx, envName)
	if err != nil {
		return err
	}

	value, err := readSecretValue(fromFile, stdin)
	if err != nil {
		return err
	}
	if value == "" {
		return fmt.Errorf(
			"refusing to write an empty value for %s\n"+
				"fix: pipe the value in, e.g.  printf '%%s' \"$TOKEN\" | forge secret set --env %s %s",
			key, envName, key)
	}

	values, err := loadStore(path)
	if err != nil {
		return err
	}
	values[key] = value
	if err := secrets.WriteSecretFile(path, values); err != nil {
		return err
	}
	// Never echo the value — only that it landed, and how big it was.
	fmt.Fprintf(out, "set %s (%d bytes) in %s\n", key, len(value), path)
	return nil
}

func runSecretUnset(ctx context.Context, envName, key string, out io.Writer) error {
	hosted, err := hostedEntitiesFor(ctx, envName)
	if err != nil {
		return err
	}
	if hosted != nil {
		return runHostedSecretUnset(ctx, envName, key, hosted, out)
	}
	path, _, err := secretStorePath(ctx, envName)
	if err != nil {
		return err
	}
	values, err := loadStore(path)
	if err != nil {
		return err
	}
	if _, ok := values[key]; !ok {
		return fmt.Errorf("%s is not set in %s", key, path)
	}
	delete(values, key)
	if err := secrets.WriteSecretFile(path, values); err != nil {
		return err
	}
	fmt.Fprintf(out, "unset %s in %s\n", key, path)
	return nil
}

// secretDeclaration attributes one declared secret to the workload that
// references it, and to the Secret/key the reference resolves through.
//
// There is deliberately no value field here, nor anywhere below it — see
// [secretListReport].
type secretDeclaration struct {
	Workload   string `json:"workload"`
	Kind       string `json:"kind"`
	SecretName string `json:"secret_name,omitempty"`
	SecretKey  string `json:"secret_key,omitempty"`
}

// secretListEntry is one declared secret: its name, whether the store holds
// a value, and who declares it. Present is a BOOLEAN, not a redacted or
// truncated value — the distinction is the whole point of the type.
//
// Presence is the three-valued form: "set" | "missing" | "unknown". Unknown
// is an ANSWER, not a failure — an external provider's Secrets are
// provisioned out of band where forge cannot look, and reporting them as
// missing (or as set) would be a guess. Present is true iff Presence is
// "set".
type secretListEntry struct {
	Name       string              `json:"name"`
	Present    bool                `json:"present"`
	Presence   string              `json:"presence"`
	Version    uint32              `json:"version,omitempty"` // hosted: the current version number
	DeclaredBy []secretDeclaration `json:"declared_by,omitempty"`
	// Access is the hosted secret's access list: who may use it. Hosted only.
	Access []secretAccessEntry `json:"access,omitempty"`
}

// The Presence vocabulary.
const (
	secretPresenceSet     = "set"
	secretPresenceMissing = "missing"
	secretPresenceUnknown = "unknown"
)

// secretListReport is the `forge secret list --json` document.
//
// NO FIELD IN THIS TYPE, OR IN ANY TYPE IT CONTAINS, CAN CARRY A SECRET
// VALUE. That is a property of the struct definitions, not of the code that
// fills them in: [collectSecretListFacts] is the only constructor, it is the
// only place that reads the store, and it returns this report rather than
// the value map — so no renderer downstream of it holds a value to leak,
// even by accident. Adding a `Value string` here would defeat that, and
// TestSecretListReportHasNoValueCarryingField fails on any new field name
// that has not been deliberately vetted.
//
// The same reasoning is why `forge secret set` reads values from stdin
// rather than argv: a value that never enters a place it can be observed
// cannot be observed.
//
// Output contract (stable; extensions are additive, per the same policy
// documented in internal/cli/lint/lint_json.go):
//
//	{
//	  "env": "dev",
//	  "provider": "file",              // file | hosted | external | rendered | none
//	  "store_path": "/abs/secrets/dev.yaml", // hosted: the control-plane URL;
//	                                   // external/none: ""
//	  "store_exists": true,            // distinguishes "no secrets set yet"
//	                                   // from "no store file at all"
//	  "verifiable": true,              // false iff presence cannot be read
//	                                   // (external, none)
//	  "secrets": [
//	    {"name": "STRIPE_SECRET_KEY", "present": true, "presence": "set",
//	     "version": 3,                 // hosted only
//	     "declared_by": [{"workload": "api", "kind": "service",
//	                      "secret_name": "app-secrets",
//	                      "secret_key": "stripe_secret_key"}]}
//	  ],
//	  "inert": ["OLD_KEY"],            // store keys nothing declares
//	  "missing": ["JWT_SECRET"],       // what `forge secret ensure` gates on
//	  "missing_count": 1,
//	  "ok": false                      // true iff verifiable and nothing missing
//	}
//
// `ok` mirrors `forge secret ensure`'s gate, NOT this command's exit code:
// `secret list` reports rather than gates, so it exits 0 with missing (or
// unknown) secrets in both modes, for EVERY provider. Exit codes are
// identical between text and --json.
type secretListReport struct {
	Env         string            `json:"env"`
	Provider    string            `json:"provider"`
	StorePath   string            `json:"store_path"`
	StoreExists bool              `json:"store_exists"`
	Verifiable  bool              `json:"verifiable"`
	Secrets     []secretListEntry `json:"secrets"`
	Inert       []string          `json:"inert"`
	// AccessOnly holds stored names no KCL declaration reads but whose access
	// list is non-empty: secrets used by workflows only. They are not inert,
	// so they are listed here instead of in Inert. Hosted only.
	AccessOnly   map[string][]secretAccessEntry `json:"access_only,omitempty"`
	Missing      []string                       `json:"missing"`
	MissingCount int                            `json:"missing_count"`
	OK           bool                           `json:"ok"`
}

// secretListStore is what one provider knows about its store, reduced to
// value-free facts: which names hold a value (and at what version), or that
// presence cannot be read at all.
type secretListStore struct {
	provider   string
	location   string
	exists     bool
	verifiable bool
	present    map[string]bool
	versions   map[string]uint32
	access     map[string][]secretAccessEntry
}

// collectSecretListFacts is the single declared-vs-present computation both
// output modes render from. It asks the env's provider for value-free
// presence facts and joins them with the KCL declarations. It NEVER refuses
// a provider: listing is a read every env can answer, even if the answer is
// "unknown".
func collectSecretListFacts(ctx context.Context, envName string) (secretListReport, error) {
	entities, err := renderEntitiesForSecrets(ctx, envName)
	if err != nil {
		return secretListReport{}, fmt.Errorf("render KCL: %w", err)
	}
	store, err := secretListStoreFor(ctx, envName, entities)
	if err != nil {
		return secretListReport{}, err
	}
	report := buildSecretListReport(envName, entities, store)
	if hasStoreSources(entities) {
		// Names a store-backed ExternalSecret reads live in a store, beside
		// whatever the secret_provider supplies.
		sources, err := storeSourceListStore(ctx, envName, entities)
		if err != nil {
			return secretListReport{}, err
		}
		report = mergeStoreSourceFacts(report, entities, sources)
	}
	return report, nil
}

// secretListStoreFor reads presence facts from whichever provider the env
// declares. The value map of a file store does not escape this function.
func secretListStoreFor(ctx context.Context, envName string, entities *KCLEntities) (secretListStore, error) {
	sp := entities.SecretProvider
	switch {
	case sp == nil:
		return secretListStore{provider: "none"}, nil
	case isHostedSecretEnv(entities):
		return hostedSecretListStore(ctx, envName, entities)
	case sp.Type == "external":
		return secretListStore{provider: "external"}, nil
	case sp.Type == "rendered":
		return renderedSecretListStore(envName, entities)
	case sp.Type == "file":
		path, err := fileSecretStorePath(envName, entities)
		if err != nil {
			return secretListStore{}, err
		}
		values, err := loadStore(path)
		if err != nil {
			return secretListStore{}, err
		}
		_, statErr := os.Stat(path)
		present := make(map[string]bool, len(values))
		for k := range values {
			present[k] = true
		}
		return secretListStore{provider: "file", location: path, exists: statErr == nil, verifiable: true, present: present}, nil
	default:
		// An unrecognised provider type is still a declaration forge can
		// report on; it just cannot see its values.
		return secretListStore{provider: sp.Type}, nil
	}
}

// renderedSecretListStore answers for forge.RenderedSecrets: each declared
// in-Secret key is "present" when its declared source resolves — a literal,
// or a key in the env's local file store (secrets/<env>.yaml).
func renderedSecretListStore(envName string, entities *KCLEntities) (secretListStore, error) {
	source, err := renderedSecretsValueSource(envName, entities)
	if err != nil {
		return secretListStore{}, err
	}
	path := renderedSecretsStoreConfig(envName, entities).Path
	_, statErr := os.Stat(path)
	store := secretListStore{provider: "rendered", location: path, exists: statErr == nil, verifiable: true, present: map[string]bool{}}
	for _, rs := range declaredSecretEntities(entities) {
		for key, src := range rs.Keys {
			name := rs.Name + "/" + key
			switch strings.ToLower(strings.TrimSpace(src.From)) {
			case "literal":
				store.present[name] = true
			default:
				k := src.Key
				if k == "" {
					k = key
				}
				if _, ok := source.Resolve(k); ok {
					store.present[name] = true
				}
			}
		}
	}
	return store, nil
}

// buildSecretListReport joins declarations with a store's presence facts.
// Pure.
func buildSecretListReport(envName string, entities *KCLEntities, store secretListStore) secretListReport {
	report := secretListReport{
		Env:         envName,
		Provider:    store.provider,
		StorePath:   store.location,
		StoreExists: store.exists,
		Verifiable:  store.verifiable,
		Secrets:     []secretListEntry{},
		Inert:       []string{},
		Missing:     []string{},
	}
	declared := declaredSecretNames(entities)
	attribution := secretDeclarationsByEnvName(entities)
	if store.provider == "rendered" {
		// A RenderedSecrets bundle declares Secret/key pairs rather than
		// env-var names, and those ARE its declarations.
		declared, attribution = renderedSecretDeclarations(entities)
	}
	for _, name := range declared {
		entry := secretListEntry{Name: name, DeclaredBy: attribution[name], Presence: secretPresenceUnknown}
		if store.verifiable {
			entry.Present = store.present[name]
			entry.Presence = secretPresenceMissing
			if entry.Present {
				entry.Presence = secretPresenceSet
				entry.Version = store.versions[name]
			} else {
				report.Missing = append(report.Missing, name)
			}
		}
		entry.Access = store.access[name]
		report.Secrets = append(report.Secrets, entry)
	}
	report.MissingCount = len(report.Missing)
	report.OK = store.verifiable && report.MissingCount == 0

	// Keys nobody declares are inert under declaration-scoped injection.
	// Surfacing them is what keeps the store from silently accumulating
	// config that belongs in KCL.
	for k := range store.present {
		if containsString(declared, k) {
			continue
		}
		if entries := store.access[k]; len(entries) > 0 {
			if report.AccessOnly == nil {
				report.AccessOnly = map[string][]secretAccessEntry{}
			}
			report.AccessOnly[k] = entries
			continue
		}
		report.Inert = append(report.Inert, k)
	}
	sort.Strings(report.Inert)
	return report
}

// renderedSecretDeclarations lists a RenderedSecrets bundle's declared keys as
// "<Secret>/<key>" names, attributed to the Secret that declares them.
func renderedSecretDeclarations(e *KCLEntities) ([]string, map[string][]secretDeclaration) {
	var names []string
	attribution := map[string][]secretDeclaration{}
	for _, rs := range declaredSecretEntities(e) {
		for key := range rs.Keys {
			name := rs.Name + "/" + key
			names = append(names, name)
			attribution[name] = append(attribution[name], secretDeclaration{
				Workload: rs.Name, Kind: "rendered-secret", SecretName: rs.Name, SecretKey: key,
			})
		}
	}
	sort.Strings(names)
	return names, attribution
}

// secretDeclarationsByEnvName maps each declared env-var name to the
// workloads that reference it, walking the same services
// [declaredSecretNames] does so the two can never disagree about what is
// declared.
func secretDeclarationsByEnvName(e *KCLEntities) map[string][]secretDeclaration {
	if e == nil {
		return nil
	}
	byName := map[string][]secretDeclaration{}
	for i := range e.Workloads {
		svc := &e.Workloads[i]
		for _, ref := range secretRefsForService(svc) {
			if ref.EnvName == "" {
				continue
			}
			byName[ref.EnvName] = append(byName[ref.EnvName], secretDeclaration{
				Workload:   svc.Name,
				Kind:       "service",
				SecretName: ref.SecretName,
				SecretKey:  ref.SecretKey,
			})
		}
		for _, name := range managedSecretNamesForService(svc) {
			byName[name] = append(byName[name], secretDeclaration{Workload: svc.Name, Kind: "managed-secret"})
		}
	}
	for _, ref := range renderedSecretStoreKeys(e) {
		byName[ref.storeKey] = append(byName[ref.storeKey], secretDeclaration{
			Workload: ref.secret, Kind: "rendered-secret", SecretName: ref.secret, SecretKey: ref.secretKey,
		})
	}
	return byName
}

// renderedSecretStoreRef is one `from="file"` key of a Bundle-level rendered
// Secret: the store key it reads, and the Secret/key it fills.
type renderedSecretStoreRef struct {
	storeKey, secret, secretKey string
}

// renderedSecretStoreKeys lists the store keys Bundle.rendered_secrets reads
// with `from="file"`. They are declarations exactly like a service's
// secret_ref: `forge secret ensure` must list one with no value, and `forge
// secret list` must not call its value inert. (A RenderedSecrets PROVIDER's
// keys are reported by Secret/key instead — see renderedSecretListStore.)
func renderedSecretStoreKeys(e *KCLEntities) []renderedSecretStoreRef {
	if e == nil {
		return nil
	}
	var out []renderedSecretStoreRef
	for _, rs := range e.RenderedSecrets {
		for key, src := range rs.Keys {
			if strings.EqualFold(strings.TrimSpace(src.From), "literal") {
				continue
			}
			storeKey := src.Key
			if storeKey == "" {
				storeKey = key
			}
			out = append(out, renderedSecretStoreRef{storeKey: storeKey, secret: rs.Name, secretKey: key})
		}
	}
	return out
}

// managedSecretNamesForService lists the store names a service's
// `managedSecret` env vars read — the STORE name, not the env var name,
// because that is the key `forge secret set` writes and the provider resolves.
//
// They are declarations exactly like a secret_ref, and are collected here
// rather than through [WorkloadEntity.EnvVars] because they are not
// store-rendered Secret refs: the provider materializes them into
// forge-managed-secrets. An OPTIONAL one (managedSecret.optional) is not
// listed: the materializer skips it when the store has no value, so it is
// not a value `ensure` should demand.
func managedSecretNamesForService(s *WorkloadEntity) []string {
	var names []string
	for _, e := range s.Spec.Env {
		if e.ManagedSecret != nil && e.ManagedSecret.Name != "" && !e.ManagedSecret.Optional {
			names = append(names, e.ManagedSecret.Name)
		}
	}
	return names
}

func runSecretListJSON(ctx context.Context, envName string, out io.Writer) error {
	report, err := collectSecretListFacts(ctx, envName)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func runSecretList(ctx context.Context, envName string, out io.Writer) error {
	report, err := collectSecretListFacts(ctx, envName)
	if err != nil {
		return err
	}

	switch {
	case report.StorePath != "":
		fmt.Fprintf(out, "secret store (%s): %s\n\n", report.Provider, report.StorePath)
	default:
		fmt.Fprintf(out, "secret store: %s\n\n", report.Provider)
	}
	if !report.Verifiable {
		switch report.Provider {
		case "external":
			fmt.Fprintln(out, "values are provisioned out of band (forge.ExternalSecrets): presence is unknown to forge.")
		case "none":
			fmt.Fprintf(out, "env %q declares no secret_provider: nothing supplies these values.\n", envName)
		default:
			fmt.Fprintf(out, "forge cannot read presence from a %q provider.\n", report.Provider)
		}
		if len(report.Secrets) > 0 {
			fmt.Fprintln(out)
		}
	}
	if len(report.Secrets) == 0 {
		fmt.Fprintln(out, "no secrets declared in KCL (nothing to resolve)")
	}
	for _, s := range report.Secrets {
		mark := strings.ToUpper(s.Presence)
		if s.Presence == secretPresenceSet {
			mark = "set"
			if s.Version > 0 {
				mark = fmt.Sprintf("set (v%d)", s.Version)
			}
		}
		fmt.Fprintf(out, "  %-34s %s\n", s.Name, mark)
		writeAccessEntries(out, s.Access, false, "      allowed: ")
	}

	if len(report.Inert) > 0 {
		fmt.Fprintf(out, "\n%d key(s) in the store that no service declares (inert — nothing injects them):\n", len(report.Inert))
		for _, o := range report.Inert {
			fmt.Fprintf(out, "  %s\n", o)
		}
		fmt.Fprintf(out, "fix: declare it with `forge.EnvVar {name = \"%s\", secret_ref = \"...\"}`,\n", report.Inert[0])
		fmt.Fprintln(out, "     move it to deploy/kcl/<env>/config.k if it is not a credential, or remove it.")
	}
	if len(report.AccessOnly) > 0 {
		names := make([]string, 0, len(report.AccessOnly))
		for n := range report.AccessOnly {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Fprintf(out, "\n%d secret(s) no service declares, used by workflows through their access list:\n", len(names))
		for _, n := range names {
			fmt.Fprintf(out, "  %-34s %s\n", n, "set")
			writeAccessEntries(out, report.AccessOnly[n], false, "      allowed: ")
		}
	}
	if report.MissingCount > 0 && (report.Provider == "file" || report.Provider == "hosted") {
		fmt.Fprintf(out, "\nfix: forge secret set --env %s <KEY>\n", envName)
	}
	return nil
}

func runSecretEnsure(ctx context.Context, envName string, out io.Writer) error {
	path, entities, err := secretStorePath(ctx, envName)
	if err != nil {
		return err
	}
	values, err := loadStore(path)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		if werr := secrets.WriteSecretFile(path, values); werr != nil {
			return werr
		}
	}
	fmt.Fprintf(out, "secret store ready: %s\n", path)

	generated, err := generateMissingSecrets(entities, path, values, out)
	if err != nil {
		return err
	}
	for _, g := range generated {
		values[g] = ""
	}

	var missing []string
	for _, name := range declaredSecretNames(entities) {
		if _, ok := values[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		fmt.Fprintln(out, "all declared secrets have values")
		return nil
	}
	fmt.Fprintf(out, "\n%d declared secret(s) have no value yet:\n", len(missing))
	for _, m := range missing {
		fmt.Fprintf(out, "  %s\n", m)
	}
	fmt.Fprintf(out, "\nfix: forge secret set --env %s <KEY>\n", envName)
	return fmt.Errorf("%d secret(s) missing a value", len(missing))
}

// generateMissingSecrets mints every declared secret the FileSecrets provider
// marks `generate` and the store lacks. File-backed only: any other provider
// (hosted, external, rendered) returns nothing, so a missing value there stays
// the error an operator must resolve. `present` is the caller's view of what
// already has a value; storePath is where new values are written. Prints key
// names only.
func generateMissingSecrets(e *KCLEntities, storePath string, present map[string]string, out io.Writer) ([]string, error) {
	if e == nil || e.SecretProvider == nil || e.SecretProvider.Type != "file" || len(e.SecretProvider.Generate) == 0 {
		return nil, nil
	}
	generated, err := secrets.EnsureGenerated(storePath, e.SecretProvider.Generate, generatableSecretNames(e), present)
	if err != nil {
		return nil, fmt.Errorf("generate secrets: %w", err)
	}
	for _, g := range generated {
		fmt.Fprintf(out, "generated %s (random key material; stored in %s — rotation is a manual `forge secret set`)\n", g, storePath)
	}
	return generated, nil
}

func runSecretMigrate(ctx context.Context, envName string, dryRun bool, out io.Writer) error {
	projectDir := projectDirForKCL()

	// Source: the legacy dotenv. Try the conventional spellings rather
	// than requiring the KCL still declare the removed provider — the
	// provider now hard-errors, so the KCL has usually been edited first.
	var src string
	for _, cand := range []string{
		".env." + envName + ".secrets",
		".env." + envName,
		".env",
	} {
		p := filepath.Join(projectDir, cand)
		if _, err := os.Stat(p); err == nil {
			src = p
			break
		}
	}
	if src == "" {
		return fmt.Errorf("no legacy .env file found for env %q (looked for .env.%s.secrets, .env.%s, .env)", envName, envName, envName)
	}

	values, err := envutil.ParseDotEnv(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}

	dstRel := filepath.Join("secrets", envName+".yaml")
	if sp := secretProviderPathForEnv(ctx, envName); sp != "" {
		dstRel = sp
	}
	dst := dstRel
	if !filepath.IsAbs(dst) {
		dst = filepath.Join(projectDir, dst)
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Fprintf(out, "%s  ->  %s\n\n", src, dst)
	var invalid []string
	for _, k := range keys {
		if !secrets.ValidSecretKey(k) {
			invalid = append(invalid, k)
			continue
		}
		fmt.Fprintf(out, "  %s\n", k)
	}
	if len(invalid) > 0 {
		return fmt.Errorf("cannot migrate: %d key(s) are not valid env-var names: %s",
			len(invalid), strings.Join(invalid, ", "))
	}
	if dryRun {
		fmt.Fprintf(out, "\ndry-run: nothing written (%d key(s) would move)\n", len(keys))
		return nil
	}

	// Merge onto whatever the store already holds so a re-run is additive.
	existing, err := loadStore(dst)
	if err != nil {
		return err
	}
	for k, v := range values {
		existing[k] = v
	}
	if err := secrets.WriteSecretFile(dst, existing); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", src, err)
	}

	fmt.Fprintf(out, "\nmoved %d secret(s); removed %s\n", len(keys), src)
	fmt.Fprintf(out, "\nnow declare the provider in deploy/kcl/%s/main.k:\n", envName)
	fmt.Fprintf(out, "    secret_provider = forge.FileSecrets {path = %q}\n", dstRel)
	return nil
}

// secretProviderPathForEnv returns the env's declared store path, or ""
// when the KCL cannot be rendered (e.g. it still declares the removed
// dotenv provider, which is exactly when migrate is run).
func secretProviderPathForEnv(ctx context.Context, envName string) string {
	entities, err := RenderKCL(ctx, projectDirForKCL(), envName)
	if err != nil || entities == nil || entities.SecretProvider == nil {
		return ""
	}
	if entities.SecretProvider.Type != "file" {
		return ""
	}
	return entities.SecretProvider.Path
}

// declaredSecretNames is the sorted, de-duplicated set of env-var names
// every service in the env declares via secret_ref AND must have a value for.
//
// OPTIONAL refs are excluded, so `forge secret ensure` agrees with the env-up
// pre-flight (secrets.ValidateDeclaredRefs). The two are separate code paths
// over the same entities, and a disagreement is worse than either being wrong
// alone: `ensure` exists to tell an operator whether `up` will succeed, so an
// `ensure` that demands a value `up` does not want sends them to invent a
// placeholder credential — which is a real-looking secret in the store, and
// defeats the check for every genuinely-missing one after it.
// generatableSecretNames is what `generate` may mint: every declared secret,
// and also every OPTIONAL secret ref. An optional ref is still a ref — a
// workload reads the value when it is there — and naming it in
// FileSecrets.generate is the env saying it wants it there. Leaving optional
// refs out meant a key the env asked forge to generate was never generated
// whenever the config field behind it is optional (reliant's AUTH_API_KEY:
// required only in its API-key auth mode, so optional in the schema), and the
// process then refused to start for want of it.
func generatableSecretNames(e *KCLEntities) []string {
	seen := map[string]struct{}{}
	for _, n := range declaredSecretNames(e) {
		seen[n] = struct{}{}
	}
	for _, r := range secretRefsFromEntities(e) {
		if r.EnvName != "" && r.Optional {
			seen[r.EnvName] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func declaredSecretNames(e *KCLEntities) []string {
	seen := map[string]struct{}{}
	for _, r := range secretRefsFromEntities(e) {
		if r.EnvName != "" && !r.Optional {
			seen[r.EnvName] = struct{}{}
		}
	}
	if e != nil {
		for i := range e.Workloads {
			for _, name := range managedSecretNamesForService(&e.Workloads[i]) {
				seen[name] = struct{}{}
			}
		}
	}
	for _, ref := range renderedSecretStoreKeys(e) {
		seen[ref.storeKey] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
