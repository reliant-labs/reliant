// Package codegen — config_native_emit.go wires the KCL config emitters
// (config_schema_gen.go, config_projection_gen.go, and the per-env config.k
// scaffolder below) into the generate pipeline.
//
// Two ownership tiers, mirroring the design:
//
//   - config_gen.k is PROJECT-LEVEL and Tier-1 forge-owned
//     (writeForgeOwned): ONE file per project, regenerated from proto on
//     every `forge generate`. It carries the config TYPE and the projection
//     BEHAVIOR together — turning a typed AppConfig into the agnostic-core
//     env map every workload's env is built from. (It was two files,
//     config_schema.k + config_projection.k, until both halves — always
//     generated together, always consistent — were merged into one Tier-1
//     file with one hash guard. See GenerateConfigKCL.)
//   - deploy/kcl/<env>/config.k is PER-ENV and USER-OWNED (write-if-absent):
//     a typed AppConfig instance carrying the per-env values. forge scaffolds
//     it once from the proto's own defaults and never clobbers later edits.
package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/devpg"
	"github.com/reliant-labs/forge/internal/naming"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Dev-environment conventions the config.k scaffolder pins so a freshly-forged
// dev environment is turnkey.
const (
	// configDevEnvName is the conventional development environment. Its
	// config.k is seeded with the MODE marker + a concrete local database DSN.
	configDevEnvName = "dev"
	// configDevModeValue is the runtime MODE value marking an environment as
	// development (config.Mode() keys off the CONFIG_FIELD_ROLE_MODE field).
	configDevModeValue = "development"
	// configModeRole is the annotation role that marks the runtime-MODE field.
	configModeRole = "CONFIG_FIELD_ROLE_MODE"
	// configDBURLEnvVar is the env var of the database connection field the dev
	// scaffold seeds with a concrete local DSN.
	configDBURLEnvVar = "DATABASE_URL"
	// configAutoMigrateEnvVar is the env var of the boolean field the dev
	// scaffold seeds True so a freshly-created dev DB is migrated on the first
	// `forge env up` boot — the app applies its migrations, THEN auto-seed
	// populates the schema. Non-dev envs inherit the proto default (false): a
	// prod deploy owns its migration story (a Job/initContainer), never an
	// implicit on-boot migrate.
	configAutoMigrateEnvVar = "AUTO_MIGRATE"
	// NATIVE SIGN-IN. The server runs the whole OIDC flow, so the OAuth
	// client is a BACKEND value: the browser posts credentials to this
	// app's API and never contacts the issuer. See the login-broker scaffold.
	configAPIURLEnvVar = "API_URL"
)

// devDatabaseDSN is the dev DSN the scaffold seeds for a project's database
// connection field — the SAME coordinates the scaffold's docker-compose
// publishes postgres on, derived from the same POSTGRES_PORT/USER/PASSWORD
// variables compose itself expands (internal/devpg).
//
// It used to be a hardcoded `localhost:5434`. That made the dev postgres port
// two facts that could disagree: compose published `${POSTGRES_PORT:-5432}`
// while `.env.dev` pinned an absolute URL on 5434, and the absolute URL wins
// at runtime. `POSTGRES_PORT=15433 forge run` therefore started THIS project's
// postgres on 15433 and connected the app to whatever was listening on 5434 —
// on a multi-stack dev machine, another project's database — where it created
// the database, ran migrations and seeded rows, then printed a healthy banner
// and exited 0. Deriving both from one input is what makes that
// unrepresentable; `forge env up` re-checks the pair at boot (devpg.Reconcile)
// because a project scaffolded under one port can be run under another.
func devDatabaseDSN(projectName string) string {
	return devpg.DSN(projectName)
}

// GenerateConfigNativeShared emits the project-level, forge-owned KCL
// module that backs the config path — <kclDirAbs>/config_gen.k — from the
// proto-derived config fields, and withdraws the two files it replaced.
//
// projectDir is the project root (for the checksum-relative path); kclDirAbs is
// the absolute deploy/kcl directory; cs is the checksum ledger. When cs is nil
// or the path can't be made relative, the files are still written (untracked).
func GenerateConfigNativeShared(fields []ConfigField, projectName, projectDir, kclDirAbs string, cs *checksums.FileChecksums) error {
	return GenerateConfigNativeSharedRoots(fields, nil, projectName, projectDir, kclDirAbs, cs)
}

// GenerateConfigNativeSharedRoots is GenerateConfigNativeShared for a
// composition-graph project: the module also carries ROOTS and the lambdas
// that bind a workload to a root (RootsKCL). nil roots is the plain module.
func GenerateConfigNativeSharedRoots(fields []ConfigField, roots []GraphRoot, projectName, projectDir, kclDirAbs string, cs *checksums.FileChecksums) error {
	body, err := GenerateConfigKCL(fields, projectName)
	if err != nil {
		return fmt.Errorf("generate config module: %w", err)
	}
	if roots == nil && GraphComposition(projectDir) {
		roots = []GraphRoot{DefaultServerRoot(fields)}
	}
	return writeConfigNativeShared(body+RootsKCL(roots), projectDir, kclDirAbs, cs)
}

// GenerateConfigNativeSharedPerBinary is the per-binary form of
// GenerateConfigNativeShared: it emits the project-level, forge-owned
// config_gen.k carrying one typed schema + one env-projection lambda PER
// BINARY, so each workload's env is built from only its own binary's fields.
//
// rootFields is the project-global AppConfig's field set, still emitted when
// non-empty (a project may keep a shared AppConfig alongside per-binary
// ones, and mid-migration every existing env file still names it).
func GenerateConfigNativeSharedPerBinary(rootFields []ConfigField, perBinary []BinaryConfigFields, projectName, projectDir, kclDirAbs string, cs *checksums.FileChecksums) error {
	return GenerateConfigNativeSharedPerBinaryRoots(rootFields, perBinary, nil, projectName, projectDir, kclDirAbs, cs)
}

// GenerateConfigNativeSharedPerBinaryRoots is GenerateConfigNativeSharedPerBinary
// for a composition-graph project: the module also carries ROOTS, each root's
// env the leaves it reaches across every config its process loads, projected
// through the primary binary's env map (perBinaryRootsEnv). nil roots is the
// default `server` root, as GenerateConfigNativeSharedRoots has it.
func GenerateConfigNativeSharedPerBinaryRoots(rootFields []ConfigField, perBinary []BinaryConfigFields, roots []GraphRoot, projectName, projectDir, kclDirAbs string, cs *checksums.FileChecksums) error {
	body, err := GenerateConfigKCLPerBinary(rootFields, perBinary, projectName)
	if err != nil {
		return fmt.Errorf("generate per-binary config module: %w", err)
	}
	if GraphComposition(projectDir) {
		if env, defaultRoot, ok := perBinaryRootsEnv(projectDir, rootFields, perBinary); ok {
			if roots == nil {
				roots = []GraphRoot{defaultRoot}
			}
			body += RootsKCLFor(roots, env)
		}
	}
	return writeConfigNativeShared(body, projectDir, kclDirAbs, cs)
}

// perBinaryRootsEnv is how a per-binary project projects a root's process
// env, and its default `server` root: through appConfigEnvMap when the
// project keeps an AppConfig and binds no config to the primary binary;
// through the primary binary's process selector when it binds config per
// command; through its root config's own lambda when that is all it binds.
// ok is false when no one lambda is a root's env: no primary binary, or a
// project-global AppConfig beside configs bound to the primary binary.
func perBinaryRootsEnv(projectDir string, rootFields []ConfigField, perBinary []BinaryConfigFields) (env RootsEnv, defaultRoot GraphRoot, ok bool) {
	treeDir := primaryCmdTreeDir(projectDir)
	if treeDir == "" {
		return RootsEnv{}, GraphRoot{}, false
	}
	binary := filepath.Base(filepath.Dir(treeDir))
	var root *BinaryConfigFields
	bound := false
	for i := range perBinary {
		bc := &perBinary[i]
		if bc.Binary != binary {
			continue
		}
		bound = true
		if root == nil && len(bc.Command) == 0 && len(bc.Subtrees) == 0 {
			root = bc
		}
	}
	switch {
	case len(rootFields) > 0 && !bound:
		return appConfigRootsEnv, DefaultServerRoot(rootFields), true
	case len(rootFields) > 0 || root == nil:
		return RootsEnv{}, GraphRoot{}, false
	}
	defaultRoot = DefaultServerRootLoads(root.Fields, perBinary, binary, root.MessageName)
	for _, ps := range ProcessSelectorsFrom(perBinary) {
		if ps.Binary == binary {
			return RootsEnv{ConfigType: ps.SchemaName(), EnvMap: ps.LambdaName(), Argv: true}, defaultRoot, true
		}
	}
	schema, lambda := KCLConfigName(root.MessageName)
	return RootsEnv{ConfigType: schema, EnvMap: lambda}, defaultRoot, true
}

// writeConfigNativeShared writes the generated config module to
// <kclDirAbs>/config_gen.k and withdraws the two files it replaced. Shared
// by the single-config and per-binary emitters so both land in the same
// place under the same ownership tier.
func writeConfigNativeShared(body, projectDir, kclDirAbs string, cs *checksums.FileChecksums) error {
	outPath := filepath.Join(kclDirAbs, ConfigSchemaModule+".k")
	wrote := false
	if cs != nil && projectDir != "" {
		if rel, rerr := filepath.Rel(projectDir, outPath); rerr == nil {
			if werr := writeForgeOwned(projectDir, rel, []byte(body), cs); werr != nil {
				return fmt.Errorf("write %s: %w", outPath, werr)
			}
			wrote = true
		}
	}
	if !wrote {
		if werr := writeUserScaffold(outPath, []byte(body)); werr != nil {
			return fmt.Errorf("write %s: %w", outPath, werr)
		}
	}

	// Only withdraw the superseded pair once the merged file is safely on
	// disk — a failed write above returns early, leaving the project with
	// its working two-file setup rather than neither.
	return RemoveLegacyConfigModules(kclDirAbs)
}

// ReconcileConfigModuleImports rewrites references to the superseded
// config_schema / config_projection KCL modules to the merged config_gen
// across the USER-OWNED files that name them: each env's config.k (which
// does `import config_schema` and types its instance
// `config_schema.AppConfig`) and each env's main.k (`import
// config_projection`, `config_projection.appConfigEnvMap(...)`).
//
// Rewriting user-owned files needs justifying, since forge's rule is that
// it does not touch them. The alternative is worse: the module those files
// import no longer exists, so a project that regenerates without this fix
// gets a KCL tree that does not load at all — `forge env up`, `forge env
// deploy` and every render fail on an unresolved import until the user
// hand-edits every env. This is the same reconciliation
// ReconcileScaffoldTestHelperName performs when a forge-owned identifier
// a scaffold references gets renamed: forge renamed the symbol, so forge
// fixes the references it caused to dangle.
//
// The rewrite is deliberately narrow — the module NAME only, on the
// import statement and on qualified references. Values, comments the user
// wrote, and every other line are preserved byte-for-byte. Returns the
// project-relative paths it rewrote, for the caller to report.
func ReconcileConfigModuleImports(kclDirAbs string) ([]string, error) {
	envs, err := os.ReadDir(kclDirAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var rewrote []string
	for _, e := range envs {
		if !e.IsDir() {
			continue
		}
		for _, name := range []string{"config.k", "main.k"} {
			path := filepath.Join(kclDirAbs, e.Name(), name)
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				continue // env without this file — nothing to reconcile
			}
			out := string(src)
			for _, mod := range LegacyConfigModules() {
				// Word-boundary replace so a user identifier that merely
				// CONTAINS the module name (my_config_schema_notes) is left
				// alone; only the module token itself moves.
				out = replaceKCLModuleToken(out, mod, ConfigSchemaModule)
			}
			if out == string(src) {
				continue
			}
			if werr := atomicfile.WriteFile(path, []byte(out), 0o644); werr != nil {
				return rewrote, fmt.Errorf("reconcile %s: %w", path, werr)
			}
			rewrote = append(rewrote, filepath.Join(e.Name(), name))
		}
	}
	return rewrote, nil
}

// replaceKCLModuleToken replaces whole-token occurrences of `old` with
// `new`, where a token is bounded by anything that cannot appear in a KCL
// identifier. This keeps `import config_schema` and
// `config_schema.AppConfig` in scope while leaving a longer identifier
// that happens to embed the name untouched.
func replaceKCLModuleToken(s, old, replacement string) string {
	isIdentByte := func(b byte) bool {
		return b == '_' ||
			(b >= 'a' && b <= 'z') ||
			(b >= 'A' && b <= 'Z') ||
			(b >= '0' && b <= '9')
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		idx := strings.Index(s[i:], old)
		if idx < 0 {
			b.WriteString(s[i:])
			break
		}
		start := i + idx
		end := start + len(old)
		beforeOK := start == 0 || !isIdentByte(s[start-1])
		afterOK := end == len(s) || !isIdentByte(s[end])
		b.WriteString(s[i:start])
		if beforeOK && afterOK {
			b.WriteString(replacement)
		} else {
			b.WriteString(old)
		}
		i = end
	}
	return b.String()
}

// RemoveLegacyConfigModules deletes the pre-merge config_schema.k /
// config_projection.k once config_gen.k supersedes them.
//
// This is the same withdrawal RemoveK3dPorts performs for a generated file
// forge stops emitting: the files are Tier-1 forge output that nothing
// imports anymore, and leaving them would be worse than deleting them —
// they would sit in the tree as a stale second definition of AppConfig
// that no longer tracks the proto, and (being in the same KCL package as
// the merged file) would collide on the very symbols it declares.
//
// Only forge's OWN renders are removed: a file whose generated-by stamp is
// missing has been taken over by the user, and it is left in place. It is
// deliberately NOT gated on the checksum manifest — a project regenerated
// by an older binary, or one whose ledger was rebuilt, still needs the
// collision cleared.
func RemoveLegacyConfigModules(kclDirAbs string) error {
	for _, mod := range LegacyConfigModules() {
		path := filepath.Join(kclDirAbs, mod+".k")
		body, err := os.ReadFile(path)
		if err != nil {
			continue // absent (the common case: a fresh project) — nothing to do
		}
		if !strings.Contains(string(body), "Code generated by forge") {
			continue // user-owned bytes; not forge's to delete
		}
		// Journaled: an aborted run restores the superseded module.
		if err := checksums.RemoveJournaled(path); err != nil {
			return fmt.Errorf("remove superseded %s: %w", path, err)
		}
	}
	return nil
}

// GenerateConfigKScaffold emits deploy/kcl/<envName>/config.k — the per-env,
// user-owned typed AppConfig VALUES instance — ONLY when it does not already
// exist. Returns true when a fresh file was written, false when an existing
// user-owned file was left untouched.
func GenerateConfigKScaffold(fields []ConfigField, projectName, kclDirAbs, envName string) (bool, error) {
	body := generateConfigKBody(fields, projectName, envName)
	outDir := filepath.Join(kclDirAbs, envName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return false, fmt.Errorf("create %s: %w", outDir, err)
	}
	return writeUserScaffoldIfAbsent(filepath.Join(outDir, "config.k"), []byte(body))
}

// GenerateConfigKScaffoldPerBinary emits the per-env, user-owned config.k for
// a project with PER-BINARY configs: one typed instance per binary
// (`admin_config: config_gen.AdminConfig = {...}`), each carrying only the
// values that binary reads.
//
// rootFields is the project-global AppConfig's field set, still emitted as
// `app_config` when non-empty so a project keeping a shared AppConfig
// alongside per-binary ones has both. Write-if-absent, exactly like the
// single-config scaffolder: forge writes it once and never clobbers edits.
func GenerateConfigKScaffoldPerBinary(rootFields []ConfigField, perBinary []BinaryConfigFields, projectName, kclDirAbs, envName string) (bool, error) {
	body := generateConfigKBodyPerBinary(rootFields, perBinary, projectName, envName)
	outDir := filepath.Join(kclDirAbs, envName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return false, fmt.Errorf("create %s: %w", outDir, err)
	}
	return writeUserScaffoldIfAbsent(filepath.Join(outDir, "config.k"), []byte(body))
}

// generateConfigKBodyPerBinary renders a per-env config.k holding one typed
// instance per binary. Each instance is SPARSE on the same rules as the
// single-config scaffolder (generateConfigKBody): only the fields this env
// must pin are set, everything else inherits its schema default.
func generateConfigKBodyPerBinary(rootFields []ConfigField, perBinary []BinaryConfigFields, projectName, envName string) string {
	var b strings.Builder
	b.WriteString("# Per-environment app config VALUES (user-owned — edit here).\n")
	b.WriteString("# This project declares PER-BINARY configs: one typed instance below per\n")
	b.WriteString("# binary (or binary subcommand), each carrying only the values THAT process\n")
	b.WriteString("# reads. The schemas and their env projections are generated from\n")
	b.WriteString("# " + configProtoDir(projectName) + "/config.proto;\n")
	b.WriteString("# add or change FIELDS in the proto, values here. Only fields this env pins\n")
	b.WriteString("# are set — every other field inherits its schema default.\n\n")
	b.WriteString(fmt.Sprintf("import %s\n\n", ConfigSchemaModule))

	emit := func(fields []ConfigField, messageName, varName string) {
		schemaName, _ := KCLConfigName(messageName)
		b.WriteString(fmt.Sprintf("%s: %s.%s = {\n", varName, ConfigSchemaModule, schemaName))
		for _, l := range configKValueLines(fields, projectName, envName) {
			b.WriteString(l)
			b.WriteString("\n")
		}
		b.WriteString("}\n")
	}

	if len(rootFields) > 0 {
		emit(rootFields, "AppConfig", "app_config")
		if len(perBinary) > 0 {
			b.WriteString("\n")
		}
	}
	for i, bc := range perBinary {
		// Name the process each instance configures: with command-scoped
		// configs, one binary has several, and the message name alone does
		// not say which subcommand reads which.
		if len(bc.Subtrees) > 0 {
			fmt.Fprintf(&b, "# Every process at or below %s.\n", subtreeList(bc.Binary, bc.Subtrees))
		} else if len(bc.Command) == 0 {
			fmt.Fprintf(&b, "# Every `%s` process (the binary's root config).\n", bc.Binary)
		} else {
			fmt.Fprintf(&b, "# The `%s %s` process.\n", bc.Binary, strings.Join(bc.Command, " "))
		}
		emit(bc.Fields, bc.MessageName, InstanceName(bc.MessageName))
		if i < len(perBinary)-1 {
			b.WriteString("\n")
		}
	}

	// A binary that scopes config to its subcommands gets its instances
	// bundled for the generated process selector, so a workload's env is one
	// call that picks the right command config from the workload's argv:
	//
	//	config_gen.<bin>ProcessEnvMap(appcfg.<bin>_process, argv, secrets)
	for _, ps := range ProcessSelectorsFrom(perBinary) {
		fmt.Fprintf(&b, "\n# Every `%s` process's config, for config_gen.%s: a workload's env is\n", ps.Binary, ps.LambdaName())
		fmt.Fprintf(&b, "#     config_gen.%s(%s, (w.command or []) + (w.args or []), w.config_secrets)\n",
			ps.LambdaName(), "appcfg."+naming.ToSnakeCase(ps.SchemaName()))
		fmt.Fprintf(&b, "%s: %s.%s = {\n", naming.ToSnakeCase(ps.SchemaName()), ConfigSchemaModule, ps.SchemaName())
		if ps.Root != nil {
			inst := InstanceName(ps.Root.MessageName)
			fmt.Fprintf(&b, "    %s = %s\n", inst, inst)
		}
		for _, c := range ps.Subtrees {
			inst := InstanceName(c.MessageName)
			fmt.Fprintf(&b, "    %s = %s\n", inst, inst)
		}
		for _, c := range ps.Commands {
			inst := InstanceName(c.MessageName)
			fmt.Fprintf(&b, "    %s = %s\n", inst, inst)
		}
		b.WriteString("}\n")
	}
	return b.String()
}

// EnvSecretsFileName is the per-env secret store the `file` secret provider
// reads: `secrets/<env>.yaml`, keyed by ENV-VAR NAME (the convention
// internal/secrets resolves on). It is gitignored — it holds secret VALUES,
// the half that never lives in git.
//
// It is deliberately NOT a dotenv. The `dotenv` provider was removed (it
// handed every host service the whole file, so a value went live without ever
// being declared in KCL), and `forge lint`'s no-dotenv rule now REJECTS a
// `.env*` file. A scaffold that still wrote one failed its own linter on the
// first run of a brand-new project.
func EnvSecretsFileName(envName string) string {
	return filepath.Join("secrets", envName+".yaml")
}

// GenerateEnvSecretsScaffold emits the gitignored `secrets/<envName>.yaml`
// that backs the env's `file` secret provider — ONLY for a LOCAL env, and ONLY
// when the file does not already exist. Returns true when a fresh file was
// written.
//
// One key per SENSITIVE config field, keyed by env-var NAME, every one seeded
// EMPTY as a labelled slot to fill. That includes DATABASE_URL: the env's KCL
// declares the dev connection string from the port it resolves per render, so
// a copy here could only go stale — see devSecretValue for the full rationale.
// `forge env up` stays turnkey because the KCL declaration supplies the value.
//
// Only local envs get one. A cloud env declares `forge.ExternalSecrets {}` —
// forge never sees those values, so scaffolding a store there would create a
// second, unread place to put a production credential.
func GenerateEnvSecretsScaffold(fields []ConfigField, projectName, projectDir, envName string) (bool, error) {
	if envName != configDevEnvName {
		return false, nil
	}
	var sensitive []ConfigField
	for _, f := range fields {
		if f.Sensitive && f.EnvVar != "" {
			sensitive = append(sensitive, f)
		}
	}
	if len(sensitive) == 0 {
		return false, nil
	}
	dst := filepath.Join(projectDir, EnvSecretsFileName(envName))
	// The store is gitignored, so the committed birth ledger never records
	// it (checksums.IsLocalOnlyScaffold); this is also the step every
	// generate passes through, so it is where a ledger an older forge
	// committed the entry into is cleaned.
	checksums.PersistLedgerPrune(projectDir)
	// The store lives in its own directory, so unlike the root-level dotenv
	// it replaced, the parent may not exist yet on a fresh scaffold.
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return false, fmt.Errorf("create secret store directory: %w", err)
	}
	return writeUserScaffoldIfAbsent(
		dst,
		[]byte(generateEnvSecretsBody(sensitive, projectName, envName)),
	)
}

// generateEnvSecretsBody renders the `secrets/<env>.yaml` body: a header
// explaining the file's role, then `<ENV_VAR>: <value>` per sensitive field.
func generateEnvSecretsBody(sensitive []ConfigField, projectName, envName string) string {
	var b strings.Builder
	b.WriteString("# SECRET VALUES for the `" + envName + "` environment — GITIGNORED, never commit.\n")
	b.WriteString("#\n")
	b.WriteString("# This is the `file` secret provider deploy/kcl/" + envName + "/main.k declares\n")
	b.WriteString("# (`forge.FileSecrets {path = \"" + filepath.ToSlash(EnvSecretsFileName(envName)) + "\"}`).\n")
	b.WriteString("# Every config field marked `sensitive: true` in " + configProtoDir(projectName) + "/config.proto\n")
	b.WriteString("# is projected into the manifest as a valueFrom.secretKeyRef, NOT an inline\n")
	b.WriteString("# value — so its VALUE lives here, keyed by env-var NAME. forge reads this\n")
	b.WriteString("# file to (a) layer the values onto host processes (`forge env up`) and (b) render\n")
	b.WriteString("# + apply the backing Secret into LOCAL clusters only.\n")
	b.WriteString("#\n")
	b.WriteString("# A value only reaches a service that DECLARES it via `EnvVar.secret_ref` —\n")
	b.WriteString("# which is why this is YAML and not a dotenv: the removed dotenv provider\n")
	b.WriteString("# injected the whole file into every service. Set values with\n")
	b.WriteString("# `forge secret set --env " + envName + " <KEY>` (value on stdin, never argv).\n")
	b.WriteString("#\n")
	b.WriteString("# Cloud environments do NOT read this: they declare\n")
	b.WriteString("# `forge.ExternalSecrets {}` and the Secret is provisioned out of band.\n")
	b.WriteString("#\n")
	b.WriteString("# Every slot starts EMPTY, including DATABASE_URL. A value that the env's\n")
	b.WriteString("# KCL already declares does not belong here: `deploy/kcl/" + envName + "/main.k`\n")
	b.WriteString("# resolves the dev postgres port on every render and composes the\n")
	b.WriteString("# connection string from it, so a copy in this file would pin the port\n")
	b.WriteString("# that happened to be free the day the project was scaffolded and then\n")
	b.WriteString("# silently disagree with the running stack.\n")
	b.WriteString("#\n")
	b.WriteString("# An EMPTY slot is not a value: forge drops it before layering, so the\n")
	b.WriteString("# name falls through to whatever declares it. That is what makes a\n")
	b.WriteString("# fresh clone turnkey with this file untouched.\n")
	b.WriteString("#\n")
	b.WriteString("# Precedence on host launch, lowest to highest:\n")
	b.WriteString("#   project config  ->  THIS STORE  ->  the env's KCL env_vars  ->  your shell\n")
	b.WriteString("# So a name the KCL DECLARES (DATABASE_URL in dev) wins over a value set\n")
	b.WriteString("# here. Setting one is still the right move for a secret the KCL cannot\n")
	b.WriteString("# state -- a real password, an API token -- which is the case where\n")
	b.WriteString("# nothing above it competes.\n\n")
	// Every slot is scaffolded EMPTY — the store lists each declared
	// sensitive ref by NAME so `forge secret ensure --env <env>` and a human
	// reading the file both see what wants a value; supplying it is the
	// developer's job.
	//
	// DATABASE_URL used to be seeded here with a concrete local DSN, to make
	// a fresh clone turnkey. It no longer is, because the env's KCL already
	// states that fact completely and this file could only hold a stale copy:
	// deploy/kcl/<env>/main.k resolves the dev postgres port on EVERY render
	// (`plugin.resolve_port("<project>-dev-postgres", 5432)`, which steps to
	// 5433 when 5432 is busy) and composes the DSN from that one variable,
	// while this store is written ONCE (write-if-absent) and never revisited.
	// hostlaunch.LayerHostEnv layers KCL env vars ABOVE the store so the live
	// declaration wins, which made the stored copy inert — but inert is not
	// harmless: it is still read by whoever opens the file and by shadowdb's
	// DSN candidate scan, and it disagrees with the running stack.
	//
	// Turnkey is unaffected (the KCL declaration supplies the value), and the
	// seed protected nothing: the dev DSN's credentials are
	// `postgres:postgres` on loopback, already spelled out in the git-tracked
	// main.k. A project wanting a real dev password puts that ONE scalar here
	// and references it from KCL (`RenderedSecretKey {from = "file", ...}`),
	// which keeps the value out of render output and the port single-source.
	for _, f := range sensitive {
		if d := strings.TrimSpace(f.Description); d != "" {
			fmt.Fprintf(&b, "# %s\n", strings.Join(strings.Fields(d), " "))
		}
		fmt.Fprintf(&b, "%s: %s\n", f.EnvVar, yamlScalar(""))
	}
	return b.String()
}

// yamlScalar renders a secret value as a YAML scalar. Values are quoted so a
// DSN's `://`, a PEM's newlines, or a password that looks like a YAML keyword
// (`yes`, `null`, `1.0`) survives the round trip as the string it is; an empty
// value stays a bare empty string, which reads as the labelled slot it is.
func yamlScalar(v string) string {
	if v == "" {
		return `""`
	}
	return strconv.Quote(v)
}

// generateConfigKBody renders the config.k body for one environment from the
// proto-derived config fields — no external input. The file is SPARSE: only
// the fields an env must pin are set, and every other field inherits its
// AppConfig schema default (config_schema.k). A field is pinned when:
//
//   - it is the dev env's MODE field (seeded "development" so the dev env is
//     positively development), or
//   - it is the dev env's AUTO_MIGRATE field (seeded True so the app applies
//     its migrations on the first `forge env up` boot — a fresh dev DB comes up
//     with its schema; dev boots alive), or
//   - it is a required field with no schema default (KCL makes such a field
//     mandatory in every AppConfig instance, so config.k must supply it; it
//     gets its type-zero placeholder for the operator to fill — and runtime
//     checkRequired fires loudly if one is left unset).
//
// A SENSITIVE field is never pinned here, in ANY environment. config.k is
// git-tracked, and a sensitive field's AppConfig type is a ConfigSecretRef
// (a Secret name+key REFERENCE), not a value — it already carries the
// default backend as its schema default, so an env that uses that backend
// writes nothing. Its VALUE comes from the env's secret provider — the
// gitignored `secrets/<env>.yaml` store (GenerateEnvSecretsScaffold), whose
// slots are scaffolded empty. An env that points a sensitive field at a
// DIFFERENT Secret writes the ConfigSecretRef override by hand.
func generateConfigKBody(fields []ConfigField, projectName, envName string) string {
	lines := configKValueLines(fields, projectName, envName)

	var b strings.Builder
	b.WriteString("# Per-environment app config VALUES (user-owned — edit here).\n")
	b.WriteString("# The typed AppConfig schema + projection are generated from\n")
	b.WriteString("# " + configProtoDir(projectName) + "/config.proto; this instance supplies the per-env values\n")
	b.WriteString("# they project. Only fields this env pins are set — every other field\n")
	b.WriteString("# inherits its AppConfig schema default.\n\n")
	// The AppConfig schema lives in the sibling config_gen.k module; KCL does
	// not share top-level symbols across separately-imported modules, so import
	// + qualify it (see ConfigSchemaModule).
	b.WriteString(fmt.Sprintf("import %s\n\n", ConfigSchemaModule))
	b.WriteString(fmt.Sprintf("app_config: %s.AppConfig = {\n", ConfigSchemaModule))
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// configKValueLines renders the pinned `    <field> = <value>` lines for one
// config instance in one environment — the SPARSE value set described on
// generateConfigKBody. Shared by the single-config and per-binary
// scaffolders so both pin fields by exactly the same rules.
func configKValueLines(fields []ConfigField, projectName, envName string) []string {
	isDev := envName == configDevEnvName

	var lines []string
	for _, f := range fields {
		// Block-reference / unbound fields carry no env binding of their own.
		if f.EnvVar == "" {
			continue
		}
		// Sensitive: a Secret REFERENCE with a schema default, never a value
		// in a git-tracked file. See the doc comment above.
		if f.Sensitive {
			continue
		}
		switch {
		case isDev && f.Role == configModeRole:
			lines = append(lines, fmt.Sprintf("    %s = %q", f.KCLPath(), configDevModeValue))
		case isDev && f.EnvVar == configAutoMigrateEnvVar:
			// Dev boots alive: the app applies its migrations on the first
			// `forge env up` so a freshly-created dev DB has its schema before the
			// first-boot auto-seed runs. KCL bool literals are capitalized
			// (True/False); the config projection lowercases this to
			// AUTO_MIGRATE=true for the runtime loader. DEV ONLY — a non-dev env
			// inherits the proto default (false), leaving prod's migration story
			// to the deploy path's migration step (an initContainer).
			lines = append(lines, fmt.Sprintf("    %s = True", f.KCLPath()))
		case f.EnvVar == configDBURLEnvVar && isDev:
			// A database URL the project chose NOT to mark sensitive still
			// gets the turnkey local DSN so `forge env up` boots against the dev
			// postgres. (The scaffolded proto DOES mark it sensitive, so this
			// branch is only reached by a project that un-marked it.)
			lines = append(lines, fmt.Sprintf("    %s = %q", f.KCLPath(), devDatabaseDSN(projectName)))
		default:
			if isKCLMandatory(f) {
				lines = append(lines, fmt.Sprintf("    %s = %s", f.KCLPath(), kclConfigZeroLiteral(f)))
			}
		}
	}
	return lines
}

// isKCLMandatory reports whether a config field is one KCL forces an AppConfig
// instance to set — a required field with no schema default. This mirrors
// config_schema_gen.go: such a field is emitted with no `= default`, so KCL
// rejects any instance that omits it.
func isKCLMandatory(f ConfigField) bool {
	return f.Required && f.DefaultValue == ""
}

// kclConfigZeroLiteral returns the type-zero KCL literal for a field in its
// KCL type — the placeholder a mandatory field is scaffolded with when it is
// not otherwise seeded.
func kclConfigZeroLiteral(f ConfigField) string {
	switch kclTypeForProtoConfig(f) {
	case "int":
		return "0"
	case "bool":
		return "False"
	case "float":
		return "0.0"
	default: // str (covers durations, carried as strings)
		return strconv.Quote("")
	}
}
