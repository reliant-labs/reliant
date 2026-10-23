package config

// Annotation-driven semantics + the cobra-facing public API.
//
// This file layers the spec'd public surface — RegisterFlags(cmd, msg),
// Load(cmd, msg), and the typed Load[T] convenience — over the
// descriptor-driven primitives in loader.go (RegisterFlagsFor / LoadInto),
// and adds the role-annotation helpers (Mode).
//
// The defining property: NONE of these helpers match on a field's NAME.
// Mode keys off the field tagged role=CONFIG_FIELD_ROLE_MODE.
// Renaming that field changes nothing; naming an UNannotated field
// "environment" does NOT make it the mode field. Behavior follows the
// annotation, not the identifier.

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// RegisterFlags registers one cobra flag per config-bound field of msg that
// carries a non-empty flag annotation, using the annotated name, default,
// and description. Sensitive fields are skipped (env/Secret only). It is the
// cobra-facing form of RegisterFlagsFor and is generic over ANY config
// message — no per-field codegen.
//
// It ALSO registers a persistent --config <path> flag (ConfigFlag) — always,
// for every config message. This is the first-class config-FILE layer's entry
// point: it is on, not dormant. Load resolves the file from this flag (or its
// environment twin — <BINARY>_CONFIG for a message bound to a binary, see
// ConfigPathEnvFor) and unmarshals it as the layer between defaults and env.
// Registering it persistent means it is also visible to subcommands. The flag
// is registered idempotently so re-registering on the same command (or a
// parent that already has it) is safe.
//
// Every cobra-level Register* also records msg on cmd as a config the
// command path reads, so a process loading several configs (a root config and
// a command config) reads them all from its one config file: the file's keys
// are the union of their fields (see filelayer.go).
func RegisterFlags(cmd *cobra.Command, msg proto.Message) error {
	registerConfigFileFlag(cmd, msg)
	recordFileMessage(cmd, msg)
	return RegisterFlagsFor(cmd.Flags(), msg)
}

// RegisterSharedFlags registers a per-binary config's SHARED half — the
// leaves of the config blocks it composes (conventionally BaseConfig) — as
// PERSISTENT flags on cmd, so every subcommand beneath it inherits them and
// `app --log-level=debug admin` resolves. It also registers the --config file
// flag, exactly as RegisterFlags does.
//
// Call it once on the ROOT command, then RegisterOwnFlags on each binary
// subcommand. The two calls partition the message: no flag is registered
// twice, and a binary's own flags stay invisible to its siblings.
func RegisterSharedFlags(cmd *cobra.Command, msg proto.Message) error {
	registerConfigFileFlag(cmd, msg)
	recordFileMessage(cmd, msg)
	return RegisterFlagsScoped(cmd.PersistentFlags(), msg, ScopeShared)
}

// RegisterOwnFlags registers a per-binary config's OWN half — the leaves
// declared directly on the message, skipping composed blocks — as LOCAL flags
// on cmd. `app admin --help` then lists exactly what admin reads, and two
// binaries may each declare a --port without colliding, because a local flag
// set belongs to one command.
//
// The composed base is deliberately NOT registered here: the root already
// carries it persistently, and cobra merges inherited persistent flags into a
// subcommand's Flags(), so Load still resolves both halves in one pass.
func RegisterOwnFlags(cmd *cobra.Command, msg proto.Message) error {
	recordFileMessage(cmd, msg)
	return RegisterFlagsScoped(cmd.Flags(), msg, ScopeOwn)
}

// RegisterPersistentFlags registers EVERY config-bound leaf of msg — its own
// and those of the blocks it composes — as PERSISTENT flags on cmd, plus the
// --config file flag.
//
// It is the registration for a binary's ROOT config, the binary-wide surface
// every one of its processes loads: called on the binary's root command,
// every subcommand inherits the flags, so `reliant server api --log-level
// debug` and `reliant --log-level debug server api` both resolve, and Load
// from any subcommand sees them (cobra merges inherited persistent flags into
// a subcommand's Flags()). A command-scoped config registers on its own
// subcommand with RegisterFlags instead, as LOCAL flags.
func RegisterPersistentFlags(cmd *cobra.Command, msg proto.Message) error {
	registerConfigFileFlag(cmd, msg)
	recordFileMessage(cmd, msg)
	return RegisterFlagsScoped(cmd.PersistentFlags(), msg, ScopeAll)
}

// RegisterLocalFlags registers EVERY config-bound leaf of msg — its own and
// those of the blocks it composes — as LOCAL flags on cmd, and NOT the
// --config file flag.
//
// It is the registration for a command-scoped config whose binary also has a
// root config: the root config's RegisterPersistentFlags already put
// --config on the root, every subcommand inherits it, and a second
// declaration here would only shadow it. A command config with no root
// config beside it registers with RegisterFlags, which adds --config itself.
func RegisterLocalFlags(cmd *cobra.Command, msg proto.Message) error {
	recordFileMessage(cmd, msg)
	return RegisterFlagsScoped(cmd.Flags(), msg, ScopeAll)
}

// CheckCommandPath refuses to load a command-scoped config from any command
// other than the one it is declared for. configName names the config message
// and want is its (forge.v1.binary_config).command — the cobra path below the
// binary's root, e.g. "server", "api".
//
// It is the exact form of a check generate cannot make: whether a command
// path exists is decided by the cobra tree the binary assembles at runtime,
// so this compares against that real tree. A config whose declared path is
// wrong can then never load silently — every Load errors, naming the
// declared path and the command that called it.
//
// A DETACHED command (no parent) is not checked: a test, or a binary loading
// a config outside its tree, has no path to compare. A nil cmd is not
// checked either, matching LoadInto's "cmd may be nil".
func CheckCommandPath(cmd *cobra.Command, configName string, want ...string) error {
	if cmd == nil || !cmd.HasParent() {
		return nil
	}
	var got []string
	for c := cmd; c.HasParent(); c = c.Parent() {
		got = append([]string{c.Name()}, got...)
	}
	if strings.Join(got, " ") == strings.Join(want, " ") {
		return nil
	}
	return fmt.Errorf(
		"config: %s is declared for the %q subcommand ((forge.v1.binary_config).command = [%s]), "+
			"but `%s` loaded it. Its flags are registered on that subcommand alone "+
			"(Register%sFlags), so another command would load it without them: call Load%s "+
			"from the %q command, or correct the annotation in proto/<project>/config/v1",
		configName, strings.Join(want, " "), quoteJoin(want), cmd.CommandPath(),
		configName, configName, strings.Join(want, " "))
}

// RegisterInheritedFlags registers EVERY config-bound leaf of msg — its own
// and those of the blocks it composes — as PERSISTENT flags on cmd, and NOT
// the --config file flag.
//
// It is the registration for a SUBTREE config ((forge.v1.binary_config)
// .subtrees) whose binary also has a root config: called on each subtree's
// command, every command beneath inherits the flags, while --config stays the
// root config's (persistent on the binary's root, inherited everywhere). A
// subtree config with no root config beside it registers with
// RegisterPersistentFlags, which adds --config itself.
func RegisterInheritedFlags(cmd *cobra.Command, msg proto.Message) error {
	recordFileMessage(cmd, msg)
	return RegisterFlagsScoped(cmd.PersistentFlags(), msg, ScopeAll)
}

// CheckCommandSubtree refuses to load a subtree config from a command that
// lies under none of its subtrees. configName names the config message and
// subtrees are its (forge.v1.binary_config).subtrees paths, each below the
// binary's root ([]string{"daemon"} covers `daemon` and every command under
// it).
//
// It is CheckCommandPath's twin for a config several commands load: its
// flags are persistent on each subtree's command, so a command outside every
// subtree would load it without them. A detached or nil cmd is not checked,
// as for CheckCommandPath.
func CheckCommandSubtree(cmd *cobra.Command, configName string, subtrees ...[]string) error {
	if cmd == nil || !cmd.HasParent() {
		return nil
	}
	var got []string
	for c := cmd; c.HasParent(); c = c.Parent() {
		got = append([]string{c.Name()}, got...)
	}
	for _, want := range subtrees {
		if len(want) <= len(got) && strings.Join(got[:len(want)], " ") == strings.Join(want, " ") {
			return nil
		}
	}
	paths := make([]string, len(subtrees))
	for i, p := range subtrees {
		paths[i] = fmt.Sprintf("%q", strings.Join(p, " "))
	}
	return fmt.Errorf(
		"config: %s is declared for the commands at or below %s ((forge.v1.binary_config).subtrees), "+
			"but `%s` loaded it. Its flags are registered on those commands (Register%sFlags), so a command "+
			"outside them would load it without them: call Load%s from a command under one of them, "+
			"or add this command's path to the annotation in proto/<project>/config/v1",
		configName, strings.Join(paths, ", "), cmd.CommandPath(), configName, configName)
}

// quoteJoin renders a command path as the annotation's list elements.
func quoteJoin(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, ", ")
}

// registerConfigFileFlag adds the persistent --config <path> flag if neither
// this command nor a parent already carries it, with msg's binary's
// <BINARY>_CONFIG as its environment twin (none for an unbound message).
// Idempotent, so the shared and whole-message registration paths can both
// call it, and a RegisterConfigFlag call made first keeps its variable.
func registerConfigFileFlag(cmd *cobra.Command, msg proto.Message) {
	RegisterConfigFlag(cmd, ConfigPathEnvFor(binaryOf(msg)))
}

// Load populates msg in place from cobra flags > environment variables >
// config file > proto defaults, for every field carrying a (forge.v1.config)
// option.
// It is the cobra-facing alias of LoadInto. cmd may be nil (env + defaults
// only). Sensitive fields resolve from env / Secret mount only.
func Load(cmd *cobra.Command, msg proto.Message, opts ...LoadOption) error {
	return LoadInto(cmd, msg, opts...)
}

// LoadOption narrows what a load demands of the configuration.
type LoadOption func(*loadOptions)

type loadOptions struct {
	required map[string]bool // nil: every field
	// env is the environment read: the process's unless Root.Validate
	// hands the load a rendered one.
	env environ
	// alongside are configs the process loads beside this one that no
	// cobra command records (Root.Validate's, which has no command): their
	// env names and families claim variables under a shared prefix.
	alongside []protoreflect.MessageDescriptor
	// collect, when set, receives every problem the load finds and the load
	// goes on (loadOptions.report); nil ends it at the first, as at boot.
	collect *[]error
}

// RequiredWithin makes `required: true` apply only to the named fields — the
// ones the process reaches, by path from the root message ("database_url",
// "stripe.secret_key"). A process running one root of the composition graph
// loads with the fields that root reaches (cmd/<bin>/cmd/roots_gen.go), so a
// worker that never opens the database does not demand DATABASE_URL, and
// its workload is not handed the credential to satisfy it. Every field still
// loads and validates; only the "must be set" demand narrows.
func RequiredWithin(paths ...string) LoadOption {
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[p] = true
	}
	return func(o *loadOptions) { o.required = set }
}

// LoadTyped is the generic convenience: it allocates a fresh T, loads it,
// and returns it typed. T must be a pointer proto message type (e.g.
// *configv1.AppConfig). Usage:
//
//	cfg, err := config.LoadTyped[*configv1.AppConfig](cmd)
//
// It mirrors the generated Load(cmd) (*Config, error) signature shape so a
// project can drop the generated loader and call this directly.
func LoadTyped[T proto.Message](cmd *cobra.Command, opts ...LoadOption) (T, error) {
	var zero T
	msg := zero.ProtoReflect().New().Interface()
	if err := LoadInto(cmd, msg, opts...); err != nil {
		return zero, err
	}
	return msg.(T), nil
}

// DefaultsTyped is DefaultsInto's generic convenience, as LoadTyped is
// LoadInto's: it allocates a fresh T holding only the proto defaults. The
// generated config.Defaults() wraps it, and the generated test helpers
// construct components with that — the config Load returns when no file,
// environment variable or flag is set, rather than an empty message whose
// nested blocks are nil and whose defaults all read as zero.
func DefaultsTyped[T proto.Message]() (T, error) {
	var zero T
	msg := zero.ProtoReflect().New().Interface()
	if err := DefaultsInto(msg); err != nil {
		return zero, err
	}
	return msg.(T), nil
}

// RuntimeMode is the typed runtime mode. The zero value is ModeProduction:
// a message with no role=MODE field (or one whose mode field is empty)
// always reports production — dev permissiveness is never the default.
type RuntimeMode int

const (
	// ModeProduction is the zero value: full production posture (auth required).
	ModeProduction RuntimeMode = iota
	// ModeDevelopment enables local-dev permissiveness. Never set in
	// deployed environments.
	ModeDevelopment
)

// IsDev reports whether the mode is ModeDevelopment. It gates NON-SECURITY
// dev ergonomics only (permissive CORS, verbose errors, dev DB defaults).
// It MUST NOT gate authentication — auth is enforced in every mode.
func (m RuntimeMode) IsDev() bool { return m == ModeDevelopment }

// modeFieldValue returns the string value of the field tagged
// role=CONFIG_FIELD_ROLE_MODE on msg, and whether such a field exists. The
// lookup is by ANNOTATION, never by name: a field named "environment"
// without the role is ignored; a role-tagged field named anything is found.
func modeFieldValue(msg proto.Message) (string, bool) {
	m := msg.ProtoReflect()
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		opt := roleOptions(fd)
		if opt == nil || opt.GetRole() != forgepb.ConfigFieldRole_CONFIG_FIELD_ROLE_MODE {
			continue
		}
		if fd.Kind() != protoreflect.StringKind {
			continue // mode is a string switch; non-string role fields are ignored
		}
		return m.Get(fd).String(), true
	}
	return "", false
}

// roleOptions reads the (forge.v1.config) extension purely to inspect the
// role tag. Unlike fieldOptions it does NOT require the field to also name a
// binding source — a field may carry only a role. Returns nil when the
// extension is absent.
func roleOptions(fd protoreflect.FieldDescriptor) *forgepb.ConfigFieldOptions {
	opts := fd.Options()
	if opts == nil {
		return nil
	}
	ext := proto.GetExtension(opts, forgepb.E_Config)
	cfg, ok := ext.(*forgepb.ConfigFieldOptions)
	if !ok || cfg == nil {
		return nil
	}
	return cfg
}

// Mode derives the runtime mode from msg's role=MODE field:
// "development"/"dev" (case-insensitive) → ModeDevelopment, anything else
// (including no role field) → ModeProduction. Deriving — rather than caching
// — means a hand-built message in tests can never disagree with its own
// mode value.
func Mode(msg proto.Message) RuntimeMode {
	v, ok := modeFieldValue(msg)
	if !ok {
		return ModeProduction
	}
	switch strings.ToLower(v) {
	case "development", "dev":
		return ModeDevelopment
	}
	return ModeProduction
}

// Validate runs the cross-field and closed-set config invariants that
// cannot be expressed as per-field defaults, failing fast (before the
// listener binds) on the known misconfiguration classes. It is the
// annotation/TYPE-driven replacement for the deleted name-matched
// validators (CORS-wildcard / TLS-pair / log-format):
//
//   - allowed_values: a string field carrying a closed value set is a
//     string ENUM; a resolved value outside the set is rejected. (True
//     proto enum fields need no check — protoreflect enforces their
//     domain. The empty value is always allowed: an unset, non-required
//     field.)
//   - TLS keypair: the fields tagged role=TLS_CERT and role=TLS_KEY are
//     both-or-neither. Exactly one set is an error (the server would
//     silently serve plaintext).
//   - CORS: a wildcard origin ("*") in the role=CORS_ORIGINS field combined
//     with role=CORS_ALLOW_CREDENTIALS=true is spec-invalid and rejected.
//
// Every check keys off the ANNOTATION, never the field name — renaming a
// field never silently drops its guard. Validate recurses into nested
// config blocks so a block can carry its own allowed_values fields. The
// cross-field TLS/CORS checks operate per message level (the role fields of
// the same message), which is where those pairs naturally live.
//
// Then it runs the message's buf.validate rules (rules.go), the invariants a
// config declares for itself: field ranges and message-level CEL.
func Validate(msg proto.Message) error {
	if err := validateMessage(msg.ProtoReflect()); err != nil {
		return err
	}
	return validateRules(msg, func(err error) error { return err })
}

// validateMessage stops at the first violation.
func validateMessage(m protoreflect.Message) error {
	return validateMessageReport(m, func(err error) error { return err })
}

// validateMessageReport hands each violation to report, and stops when report
// returns an error — at the first, for validateMessage; never, for a load that
// collects every problem.
func validateMessageReport(m protoreflect.Message, report func(error) error) error {
	fields := m.Descriptor().Fields()

	var (
		tlsCert, tlsKey               string
		haveTLSCert, haveTLSKey       bool
		corsOrigins                   string
		corsAllowCreds                bool
		haveCORSOrigins, haveCORSCred bool
	)

	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)

		// Recurse into nested config blocks first.
		if isConfigBlock(fd) {
			if err := validateMessageReport(m.Get(fd).Message(), report); err != nil {
				return err
			}
			continue
		}

		opt := roleOptions(fd)
		if opt == nil {
			continue
		}

		// allowed_values closed-set check. A repeated string field is a
		// closed set PER ELEMENT — every element must be in the set.
		if vals := opt.GetAllowedValues(); len(vals) > 0 && isEnvFamily(opt) && fd.IsMap() && fd.MapValue().Kind() == protoreflect.StringKind {
			for _, got := range familyValues(m, fd) {
				if got != "" && !containsStr(vals, got) {
					if err := report(fmt.Errorf("config field %s: value %q is not one of the allowed values %v", fd.Name(), got, vals)); err != nil {
						return err
					}
				}
			}
		}
		if vals := opt.GetAllowedValues(); len(vals) > 0 && isStringField(fd) {
			for _, got := range stringValues(m, fd) {
				if got != "" && !containsStr(vals, got) {
					if err := report(fmt.Errorf("config field %s: value %q is not one of the allowed values %v", fd.Name(), got, vals)); err != nil {
						return err
					}
				}
			}
		}

		switch opt.GetRole() {
		case forgepb.ConfigFieldRole_CONFIG_FIELD_ROLE_TLS_CERT:
			if isStringField(fd) {
				tlsCert, haveTLSCert = firstString(m, fd), true
			}
		case forgepb.ConfigFieldRole_CONFIG_FIELD_ROLE_TLS_KEY:
			if isStringField(fd) {
				tlsKey, haveTLSKey = firstString(m, fd), true
			}
		case forgepb.ConfigFieldRole_CONFIG_FIELD_ROLE_CORS_ORIGINS:
			if isStringField(fd) {
				corsOrigins, haveCORSOrigins = strings.Join(stringValues(m, fd), ","), true
			}
		case forgepb.ConfigFieldRole_CONFIG_FIELD_ROLE_CORS_ALLOW_CREDENTIALS:
			if fd.Kind() == protoreflect.BoolKind {
				corsAllowCreds, haveCORSCred = m.Get(fd).Bool(), true
			}
		}
	}

	// TLS keypair: both-or-neither.
	if haveTLSCert && haveTLSKey {
		certSet := tlsCert != ""
		keySet := tlsKey != ""
		if certSet != keySet {
			if err := report(fmt.Errorf("TLS is half-configured: set BOTH the TLS cert and key (role=TLS_CERT/TLS_KEY) or NEITHER — exactly one was provided")); err != nil {
				return err
			}
		}
	}

	// CORS: wildcard origin + credentials is spec-invalid.
	if haveCORSOrigins && haveCORSCred && corsAllowCreds && hasWildcardOrigin(corsOrigins) {
		if err := report(fmt.Errorf("CORS misconfiguration: a wildcard origin (\"*\") MUST NOT be combined with credentials (role=CORS_ALLOW_CREDENTIALS=true); name explicit origins instead")); err != nil {
			return err
		}
	}

	return nil
}

// isStringField reports whether fd holds string data — a plain string field
// or a repeated string. A map is excluded: its Kind() is message, but even a
// map<string,string> is not a value these role checks can read.
//
// It exists because `fd.Kind() == StringKind` is true for a REPEATED string
// too, and reading one with Value.String() does not fail loudly — it returns
// a formatted dump of the list's internal representation. A wildcard "*" in
// a repeated CORS-origins field therefore did not match, silently disarming
// the wildcard+credentials guard. Every role check goes through here and
// stringValues so the guards apply to both cardinalities.
func isStringField(fd protoreflect.FieldDescriptor) bool {
	return fd.Kind() == protoreflect.StringKind && !fd.IsMap()
}

// stringValues reads a string field as a slice: one element for a scalar,
// every element for a repeated one.
func stringValues(m protoreflect.Message, fd protoreflect.FieldDescriptor) []string {
	if !fd.IsList() {
		return []string{m.Get(fd).String()}
	}
	l := m.Get(fd).List()
	out := make([]string, 0, l.Len())
	for i := 0; i < l.Len(); i++ {
		out = append(out, l.Get(i).String())
	}
	return out
}

// firstString reads the single value a both-or-neither role field carries.
// For a repeated field it takes the first element, so "is it set at all"
// stays the question being asked — which is all the TLS keypair check needs.
func firstString(m protoreflect.Message, fd protoreflect.FieldDescriptor) string {
	vals := stringValues(m, fd)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func containsStr(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// hasWildcardOrigin reports whether the comma-separated origins list
// contains a bare "*" entry.
func hasWildcardOrigin(origins string) bool {
	for _, o := range strings.Split(origins, ",") {
		if strings.TrimSpace(o) == "*" {
			return true
		}
	}
	return false
}
