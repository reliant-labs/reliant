package config

// Descriptor-driven config loader.
//
// This is THE config loader: forge no longer emits a per-field loader into a
// project. Instead of generating Go code from the config proto, it reads the
// (forge.v1.config) field options off a proto.Message's descriptor at RUNTIME
// and resolves each field with the canonical forge precedence (later layers
// override earlier ones): proto default < config FILE < environment variable <
// explicit CLI flag. A project holds a *configv1.AppConfig (the config object
// IS the proto type) and calls LoadInto/RegisterFlags — the generated
// pkg/config/config.go is a thin shim that aliases Config to the proto type and
// wraps these entrypoints.
//
// The resolution semantics:
//
//   - Precedence: defaults → build defaults (stamped with -ldflags, see
//     builddefaults.go) → config file → env (LookupEnv) → flag (only if
//     cmd.Flags().Changed), then a required-field check. The file layer
//     (filelayer.go) loads only when a path is EXPLICITLY given and fails
//     loudly on a missing/invalid explicit path. See LoadInto.
//   - Empty-env handling: an env var set to "" is UNSET, for every field
//     kind — the file or default value stands. An explicitly empty value is
//     a flag's to set (`--pprof-addr=`). See lookupEnv.
//   - Env names: a field reads its env_var, then each of its
//     env_var_aliases in order; the first one set wins, and a deprecated
//     alias that is set prints a notice to stderr. See envnames.go.
//   - Cardinality: a repeated field is carried by env/flag as ONE
//     comma-separated string, each element parsed with the field's element
//     kind, with surrounding whitespace trimmed and blank elements dropped.
//     Each layer REPLACES the whole list; there is no append. An element
//     containing a comma cannot be expressed and belongs in the config file.
//     Map fields are rejected with a diagnostic naming the field. See
//     parseValue/parseList.
//   - A malformed value is an error that aborts loading — never a silent
//     fallback to the default. So is a value the field's annotations reject
//     (allowed_values, the TLS/CORS role invariants): LoadInto ends with
//     Validate, so no caller has to remember a second call.
//   - Durations: a Go duration is recognized ONLY when the proto field is a
//     google.protobuf.Duration message (no name heuristic — declare the
//     field as Duration to get typed-duration behavior). String fields stay
//     strings.

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

const durationFullName protoreflect.FullName = "google.protobuf.Duration"

// fieldOptions reads the (forge.v1.config) extension off a field
// descriptor, returning nil when the field carries no such option (those
// fields are skipped entirely — they are not config-bound).
func fieldOptions(fd protoreflect.FieldDescriptor) *forgepb.ConfigFieldOptions {
	opts := fd.Options()
	if opts == nil {
		return nil
	}
	ext := proto.GetExtension(opts, forgepb.E_Config)
	cfg, ok := ext.(*forgepb.ConfigFieldOptions)
	if !ok || cfg == nil {
		return nil
	}
	// proto.GetExtension returns a non-nil zero message when the extension
	// is registered on the message but unset on this field. Treat a fully
	// blank option as "not config-bound" so such a field is skipped, the
	// same way the generator skips fields with no annotation. A field is
	// considered config-bound if it names any binding source.
	if cfg.GetEnvVar() == "" && len(cfg.GetEnvVarAliases()) == 0 && cfg.GetFlag() == "" &&
		cfg.GetDefaultValue() == "" && !cfg.GetRequired() && cfg.GetEnvPrefix() == "" {
		return nil
	}
	return cfg
}

// isDurationField reports whether a field is a google.protobuf.Duration
// message — the only duration shape the library recognizes (see the
// file-level note on the generated loader's name heuristic).
func isDurationField(fd protoreflect.FieldDescriptor) bool {
	return fd.Kind() == protoreflect.MessageKind &&
		fd.Message() != nil &&
		fd.Message().FullName() == durationFullName
}

// isConfigBlock reports whether a field is a NESTED CONFIG BLOCK: a
// singular message field that is not a google.protobuf.Duration and whose
// message type carries at least one config-bound (or role/allowed_values
// annotated) leaf. These are the component config "blocks" that compose
// onto the root config (e.g. AppConfig.trader → TraderConfig); the loader
// descends into them and populates the sub-message. A repeated/map message
// field is never a block — config blocks are singular composition.
func isConfigBlock(fd protoreflect.FieldDescriptor) bool {
	if fd.Kind() != protoreflect.MessageKind || fd.IsList() || fd.IsMap() {
		return false
	}
	if isDurationField(fd) {
		return false
	}
	md := fd.Message()
	if md == nil {
		return false
	}
	sub := md.Fields()
	for i := 0; i < sub.Len(); i++ {
		if fieldOptions(sub.Get(i)) != nil || roleOptions(sub.Get(i)) != nil {
			return true
		}
	}
	return false
}

// lookupEnv reads one variable for the env layer. Unset and set-to-"" are
// the same answer here, for every field kind: the environment does not
// configure this field, and the value the file or the default gave stands.
//
// Why "" is unset rather than an empty value:
//
//   - The environment cannot tell the two apart. compose's `X: ${X}`, a
//     deploy template's `value:` rendered from a missing key, systemd's
//     `Environment=X=` and `export X=` in a shell profile all produce a
//     set-but-empty variable whose author meant "not configured". Read as a
//     value, it silently replaced a default (TEMPORAL_HOST="" turning
//     "localhost" into "") or a config file's value with "".
//   - One rule for every kind. It used to be a value for strings and lists
//     and unset for numbers, bools and durations: an artifact of what
//     parses, not something an operator could predict from the field.
//   - An explicitly empty value keeps a channel nothing produces by
//     accident: a flag, which a person types (`--pprof-addr=` turns pprof
//     off, and `--allowed-registries=` clears a defaulted list).
//
// The config file's path variable (<BINARY>_CONFIG, filelayer.go) reads
// through here too, and viper (AllowEmptyEnv is off by default) and
// caarlos0/env (an empty variable falls back to the default) agree.
func (e environ) lookupEnv(name string) (string, bool) {
	v, ok := e.raw(name)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// envSetButEmpty reports whether name is present in the environment with an
// empty value — the case lookupEnv reads as unset, and which a required-field
// error names so the operator is not left hunting a variable that IS set.
func (e environ) envSetButEmpty(name string) bool {
	v, ok := e.raw(name)
	return ok && v == ""
}

// environ is the environment a load reads: the process's, or — for
// Root.Validate, which checks an env a deployment renders — a map of one.
type environ struct {
	vars map[string]string // nil: the process environment
}

// raw is one variable as set, "" included.
func (e environ) raw(name string) (string, bool) {
	if e.vars == nil {
		return os.LookupEnv(name)
	}
	v, ok := e.vars[name]
	return v, ok
}

// environ is every variable as "NAME=value", sorted.
func (e environ) environ() []string {
	var names []string
	if e.vars == nil {
		names = os.Environ()
	} else {
		for k, v := range e.vars {
			names = append(names, k+"="+v)
		}
	}
	sort.Strings(names)
	return names
}

// RegisterFlagsFor walks msg's fields and registers one cobra/pflag flag
// per field that carries a non-empty (forge.v1.config).flag. The flag's
// type matches the proto field kind (string/int32/int64/bool/float;
// duration messages register as a string flag — LoadInto parses "5s" →
// Duration), and its default is the proto option's DefaultValue.
//
// It is the pflag-level primitive; the cobra-facing RegisterFlags(cmd, msg)
// in semantic.go wraps it. It recurses into nested config blocks.
//
// Fields without a flag (typically secrets sourced only from env / Secret
// mounts) are intentionally skipped — defense-in-depth against shell-history
// / `ps` exposure of credentials.
func RegisterFlagsFor(flags *pflag.FlagSet, msg proto.Message) error {
	return registerFlagsForDesc(flags, msg.ProtoReflect().Descriptor(), scopeAll)
}

// FlagScope selects WHICH of a config message's flags a registration call
// covers. It exists for per-binary config, where one message's flags are
// split across two cobra flag sets rather than all landing on one command.
//
// A per-binary config composes a SHARED block (conventionally BaseConfig,
// holding port/log level/mode — every binary wants each of them, as its own
// value) alongside the leaves only that binary reads. Those two halves want
// different cobra homes:
//
//   - the shared block's leaves are PERSISTENT flags on the root command, so
//     `app --log-level=debug admin` works and one definition serves every
//     subcommand;
//   - the binary's own leaves are LOCAL flags on that binary's subcommand, so
//     `app admin --help` lists exactly what admin reads and two binaries can
//     each define a `--port` without colliding.
//
// Splitting by scope rather than registering everything twice is what keeps
// ownership disjoint at the CLI: a flag registered under ScopeOwn belongs to
// one subcommand's flag set and no other command can see it.
//
// Loading is deliberately NOT scoped — LoadInto walks the whole message, and
// cobra's Flags() on a subcommand already merges inherited persistent flags
// (so Changed()/Lookup() resolve a root flag from the subcommand). One load
// call therefore fills both halves with the normal precedence.
//
// The GENERATED per-binary surface does not split a message: since
// (forge.v1.binary_config).command scopes a config to one subcommand, each
// message has one flag home — a binary's root config registers ScopeAll
// persistent on the root (RegisterPersistentFlags), a command config ScopeAll
// local on its subcommand (RegisterLocalFlags / RegisterFlags). ScopeShared
// and ScopeOwn remain for a tree assembled by hand.
type FlagScope int

const (
	// ScopeAll registers every config-bound field, blocks included. It is the
	// single-config default and what RegisterFlags/RegisterFlagsFor use.
	ScopeAll FlagScope = iota
	// ScopeShared registers ONLY the leaves of composed config blocks — the
	// shared base a per-binary config embeds. Intended for the root command's
	// persistent flag set.
	ScopeShared
	// ScopeOwn registers ONLY the leaves declared directly on the message,
	// skipping composed blocks — the fields this binary alone reads.
	// Intended for a binary subcommand's local flag set.
	ScopeOwn
)

// scopeAll is the unexported spelling used by the internal recursion.
const scopeAll = ScopeAll

// RegisterFlagsScoped registers the subset of msg's flags selected by scope
// onto flags. See FlagScope for why the split exists and which cobra flag set
// each half belongs on. ScopeAll is identical to RegisterFlagsFor.
func RegisterFlagsScoped(flags *pflag.FlagSet, msg proto.Message, scope FlagScope) error {
	return registerFlagsForDesc(flags, msg.ProtoReflect().Descriptor(), scope)
}

// registerFlagsForDesc registers flags for a descriptor, recursing into
// nested config-block messages. It walks the DESCRIPTOR (not a live
// message) because flag registration needs no instance — only the field
// shapes and annotations. A nested message field that is itself a config
// block (carries config-bound leaves and is not a google.protobuf.Duration)
// is descended into: its leaves keep their OWN env/flag annotations, so a
// block's flags share the flat namespace exactly as the generated loader
// emitted them. Recursion is unbounded in depth but cycle-free in practice
// (a config proto is a finite tree).
// The scope argument selects which half of a per-binary config the call
// covers (see FlagScope): ScopeShared descends into composed blocks and
// registers nothing at this level, ScopeOwn registers this level's leaves and
// skips blocks entirely, and ScopeAll does both. Nested levels below a block
// are always registered in full — scope partitions the message's OWN fields
// from its COMPOSED ones, and that distinction only exists at the top.
func registerFlagsForDesc(flags *pflag.FlagSet, desc protoreflect.MessageDescriptor, scope FlagScope) error {
	fields := desc.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if isConfigBlock(fd) {
			if scope == ScopeOwn {
				continue // composed block: the root registers it, not this binary
			}
			if err := registerFlagsForDesc(flags, fd.Message(), scopeAll); err != nil {
				return err
			}
			continue
		}
		if scope == ScopeShared {
			continue // own leaf: belongs to the binary's subcommand, not the root
		}
		opt := fieldOptions(fd)
		if opt == nil || opt.GetFlag() == "" {
			continue
		}
		// Sensitive fields are NEVER exposed as flags — their value must come
		// from an env var / Secret mount, never from shell history or `ps`.
		// This holds even if the field carries a flag annotation by mistake.
		if opt.GetSensitive() {
			continue
		}
		name := opt.GetFlag()
		def := opt.GetDefaultValue()
		desc := flagUsage(opt) // the description, then the env vars that also set it

		if fd.IsMap() {
			return fmt.Errorf(
				"config field %s: map fields cannot be bound to a flag — set it in the config file layer instead (--%s)",
				fd.Name(), ConfigFlag)
		}
		// A repeated field registers as a STRING flag carrying the same
		// comma-separated form the env layer uses, whatever its element kind
		// — LoadInto parses it back through parseList. Typing it by element
		// kind would be wrong twice over: an Int32 flag cannot accept
		// "8080,9090" at all, and pflag's StringSlice does not round-trip
		// here, because its Value.String() renders "[a,b]" and the loader
		// would then read the brackets as data.
		if fd.IsList() || isDurationField(fd) {
			flags.String(name, def, desc)
			continue
		}
		switch fd.Kind() {
		case protoreflect.StringKind:
			flags.String(name, def, desc)
		case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
			v, err := parseDefaultInt(def, 32)
			if err != nil {
				return fmt.Errorf("config field %s: invalid int32 default %q: %w", fd.Name(), def, err)
			}
			flags.Int32(name, int32(v), desc)
		case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
			v, err := parseDefaultInt(def, 64)
			if err != nil {
				return fmt.Errorf("config field %s: invalid int64 default %q: %w", fd.Name(), def, err)
			}
			flags.Int64(name, v, desc)
		case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
			v, err := parseDefaultUint(def, 32)
			if err != nil {
				return fmt.Errorf("config field %s: invalid uint32 default %q: %w", fd.Name(), def, err)
			}
			flags.Uint32(name, uint32(v), desc)
		case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
			v, err := parseDefaultUint(def, 64)
			if err != nil {
				return fmt.Errorf("config field %s: invalid uint64 default %q: %w", fd.Name(), def, err)
			}
			flags.Uint64(name, v, desc)
		case protoreflect.BoolKind:
			v := false
			if def != "" {
				b, err := strconv.ParseBool(def)
				if err != nil {
					return fmt.Errorf("config field %s: invalid bool default %q: %w", fd.Name(), def, err)
				}
				v = b
			}
			flags.Bool(name, v, desc)
		case protoreflect.FloatKind:
			v, err := parseDefaultFloat(def, 32)
			if err != nil {
				return fmt.Errorf("config field %s: invalid float default %q: %w", fd.Name(), def, err)
			}
			flags.Float32(name, float32(v), desc)
		case protoreflect.DoubleKind:
			v, err := parseDefaultFloat(def, 64)
			if err != nil {
				return fmt.Errorf("config field %s: invalid double default %q: %w", fd.Name(), def, err)
			}
			flags.Float64(name, v, desc)
		default:
			return fmt.Errorf("config field %s: unsupported kind %s for flag registration", fd.Name(), fd.Kind())
		}
	}
	return nil
}

// LoadInto populates msg in place using the full forge config precedence —
// later layers OVERRIDE earlier ones:
//
//		defaults  →  build defaults  →  config file  →  environment  →  flags
//		(earliest)                                                      (wins)
//
//	  - defaults: every annotated field's proto default (sensitive fields get
//	    none — an inline secret default would be a leak).
//	  - build defaults: the values this BUILD was stamped with (-ldflags -X
//	    into the generated pkg/config.BuildDefaults; builddefaults.go), keyed
//	    by env_var. A stamp that names no field, a sensitive field, or a value
//	    that does not parse fails the load.
//	  - config file: when a path is EXPLICITLY given via the --config flag or
//	    the binary's <BINARY>_CONFIG env var, the file is proto-native
//	    unmarshaled INTO msg, overlaying the defaults (see filelayer.go). A
//	    missing/invalid explicit file is a LOUD error — never a silent
//	    fallback. With no path given this layer is simply skipped (normal
//	    precedence, not a fallback).
//	  - environment: each field's env var overrides the file/default value.
//	    A variable set to "" is unset, whatever the field's kind.
//	  - flags: a flag changed on THIS invocation overrides everything.
//
// It is the runtime, descriptor-driven loader: empty-env handling, required-
// field errors, and per-kind parsing all key off the (forge.v1.config) field
// options. cmd may be nil (file via <BINARY>_CONFIG + env + defaults only), so
// LoadInto works from non-cobra entrypoints too.
//
// Durations: declare a duration-shaped field as google.protobuf.Duration in
// the config proto — env/flag layers parse the Go-duration string ("5s")
// into the message, and consumers read it with .AsDuration(). (A plain
// string field stays a string; the descriptor has no name heuristic.)
//
// After the layers it checks required fields and then runs Validate (the
// TLS/CORS role invariants and allowed_values), so a loaded config has
// already passed every declared check: an out-of-set value fails the load,
// naming the field. Mode (role=MODE) and Validate live in this package too
// (semantic.go), as FREE FUNCTIONS over the message; Validate is for a
// message built by hand, which no load has checked. There is no parallel
// generated struct: a project holds the proto config type.
func LoadInto(cmd *cobra.Command, msg proto.Message, opts ...LoadOption) error {
	var o loadOptions
	for _, opt := range opts {
		opt(&o)
	}
	m := msg.ProtoReflect()

	// Layer 1: defaults (the base every later layer overlays).
	if err := applyDefaults(m); err != nil {
		return err
	}

	// Layer 1b: build-time defaults stamped into the binary
	// (builddefaults.go): what one BUILD says, above what the source says.
	if err := applyBuildDefaults(m); err != nil {
		return err
	}

	// Layer 2: config file, ONLY when an explicit path is given (--config
	// flag or the binary's <BINARY>_CONFIG env). A missing/invalid explicit
	// file aborts LOUDLY here; no path given means this layer is skipped (not
	// a fallback).
	if path, source, ok := resolveConfigPath(cmd, msg, o.env); ok {
		if err := o.report(applyConfigFile(msg, path, source, o.sharedFileMessages(cmd, msg))); err != nil {
			return err
		}
	}

	// Layers 3 + 4: env then flags overlay onto whatever defaults/file set.
	if err := applyEnvAndFlags(cmd, m, &o); err != nil {
		return err
	}
	// An env family's prefix is owned: a variable under it that nothing
	// claims is a typo, refused rather than silently dropped (envfamily.go).
	if err := checkFamilyPrefixes(cmd, msg, &o); err != nil {
		return err
	}

	// Required fields must be satisfied by SOME layer (default/file/env/flag).
	if err := checkRequiredWithin(m, "", o.required, &o); err != nil {
		return err
	}

	// The declared invariants (allowed_values, the TLS pair, CORS wildcard +
	// credentials) are part of loading, not a second step a caller may skip:
	// a config whose annotations reject its resolved values never reaches the
	// process. Validate stays exported for a message built by hand.
	if err := validateMessageReport(m, o.report); err != nil {
		return err
	}
	// Then the buf.validate rules the config proto declares (rules.go): a
	// port's range, a positive duration, a cross-field CEL rule.
	return validateRules(msg, o.report)
}

// report hands a load's problem on: with collect set (Root.Validate), it is
// recorded and the load goes on, so every problem is found; otherwise it ends
// the load, as at boot.
func (o *loadOptions) report(err error) error {
	if err == nil || o.collect == nil {
		return err
	}
	*o.collect = append(*o.collect, err)
	return nil
}

// sharedFileMessages is the configs a load of msg from cmd shares its process
// with: those recorded on cmd's path, and those o names alongside it.
func (o *loadOptions) sharedFileMessages(cmd *cobra.Command, msg proto.Message) []protoreflect.MessageDescriptor {
	return append(sharedFileMessages(cmd, msg), o.alongside...)
}

// DefaultsInto populates msg with the defaults layer alone — the base LoadInto
// starts from before any file, environment or flag overlays it: every
// annotated field at its proto default, nested config blocks allocated, and
// sensitive fields empty. It reads nothing from the process and skips the
// required-field check (a required field with no default is legitimately
// empty here), so it is the hermetic stand-in for LoadInto that tests need:
// the config production would load with nothing set.
//
// It errors only on a default that does not parse for its field's kind — a
// defect in the config proto, never a runtime condition.
func DefaultsInto(msg proto.Message) error {
	return applyDefaults(msg.ProtoReflect())
}

// applyDefaults sets every annotated field to its proto default, recursing
// into nested config blocks. Sensitive fields are skipped — a literal secret
// default would be a leak (their value must come from file/env). A field with
// no default annotation is left at its proto zero value.
func applyDefaults(m protoreflect.Message) error {
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if isConfigBlock(fd) {
			if err := applyDefaults(m.Mutable(fd).Message()); err != nil {
				return err
			}
			continue
		}
		opt := fieldOptions(fd)
		if opt == nil || opt.GetSensitive() || opt.GetDefaultValue() == "" {
			continue
		}
		val, err := parseValue(m, fd, opt.GetDefaultValue())
		if err != nil {
			return fmt.Errorf("invalid default %q for config field %s: %w", opt.GetDefaultValue(), fd.Name(), err)
		}
		m.Set(fd, val)
	}
	return nil
}

// applyEnvAndFlags overlays the env layer then the flag layer onto m,
// recursing into nested config blocks. For each field the env var (if set
// to something other than "" — see lookupEnv) overrides the current value,
// then a flag changed on THIS invocation overrides that. A field that
// neither env nor flag touches keeps whatever defaults/file set.
func applyEnvAndFlags(cmd *cobra.Command, m protoreflect.Message, o *loadOptions) error {
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if isConfigBlock(fd) {
			if err := applyEnvAndFlags(cmd, m.Mutable(fd).Message(), o); err != nil {
				return err
			}
			continue
		}
		opt := fieldOptions(fd)
		if opt == nil {
			continue
		}
		if err := o.report(overlayField(cmd, m, fd, opt, o.env)); err != nil {
			return err
		}
	}
	return nil
}

// overlayField applies the env layer then the flag layer to a single field.
// Each present source parses+sets onto the message; an absent source is a
// no-op that preserves the earlier layer (default/file). Sensitive fields
// take env (Secret mount) but NEVER a flag — no flag is ever registered for
// them, and we defensively skip the flag branch even if one were.
func overlayField(cmd *cobra.Command, m protoreflect.Message, fd protoreflect.FieldDescriptor, opt *forgepb.ConfigFieldOptions, env environ) error {
	if isEnvFamily(opt) {
		return overlayFamily(m, fd, opt, env) // env only: a family has no flag
	}
	// Env layer: env_var, then its aliases in order — the first one set wins
	// (envnames.go). A malformed value in the name that wins is an error; it
	// never falls through to the next name.
	name, v, set := lookupFieldEnv(opt, env)
	if set {
		val, err := parseValue(m, fd, v)
		if err != nil {
			return fmt.Errorf("invalid value %q for config field %s (from env %s): %w", v, fd.Name(), name, err)
		}
		m.Set(fd, val)
	}
	notices := noticeWriter(cmd)
	if env.vars != nil {
		notices = io.Discard // a rendered env is being checked, not a process started
	}
	noticeDeprecatedAliases(notices, fd, opt, name, env)

	// Flag layer (wins). Never for sensitive fields.
	flagName := opt.GetFlag()
	if opt.GetSensitive() {
		flagName = ""
	}
	if cmd != nil && flagName != "" && cmd.Flags().Changed(flagName) {
		if f := cmd.Flags().Lookup(flagName); f != nil {
			val, err := parseValue(m, fd, f.Value.String())
			if err != nil {
				return fmt.Errorf("invalid value %q for config field %s (from flag --%s): %w", f.Value.String(), fd.Name(), flagName, err)
			}
			m.Set(fd, val)
		}
	}
	return nil
}

// checkRequired errors if any required field is still unset after all layers
// have been applied, recursing into nested config blocks. A required field
// that no layer populated fails fast — the same loud guarantee the single-pass
// loader gave. `required: true` is the only thing that makes a field
// required here: `sensitive` does not (whether a credential must exist is
// the deploy pre-flight's question, per workload; see forge.proto's
// ConfigFieldOptions.optional).
func checkRequired(m protoreflect.Message) error {
	return checkRequiredWithin(m, "", nil, &loadOptions{})
}

// checkRequiredWithin is checkRequired over the fields a process reaches:
// within names them by path ("database_url", "stripe.secret_key"); nil is
// every field.
func checkRequiredWithin(m protoreflect.Message, prefix string, within map[string]bool, o *loadOptions) error {
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if isConfigBlock(fd) {
			if err := checkRequiredWithin(m.Get(fd).Message(), prefix+string(fd.Name())+".", within, o); err != nil {
				return err
			}
			continue
		}
		opt := fieldOptions(fd)
		if opt == nil || !opt.GetRequired() {
			continue
		}
		if within != nil && !within[prefix+string(fd.Name())] {
			continue
		}
		if fieldIsEmpty(m, fd) {
			env, empty := envSourcesPhrase(opt), emptyEnvNote(opt, o.env)
			var err error
			if flag := opt.GetFlag(); flag != "" && !opt.GetSensitive() {
				err = fmt.Errorf("required config field %s is not set (env: %s, flag: --%s)%s", fd.Name(), env, flag, empty)
			} else {
				err = fmt.Errorf("required config field %s is not set (env: %s)%s", fd.Name(), env, empty)
			}
			if err := o.report(err); err != nil {
				return err
			}
		}
	}
	return nil
}

// fieldIsEmpty reports whether a field still holds its zero value after
// loading. For a list or map, empty means zero entries; for a Duration
// message, an unpopulated message; for scalars, the kind's zero. This is the
// post-load required check — a required field must be non-zero from SOME
// layer.
//
// Cardinality is checked before kind for the same reason it is in parseValue:
// a repeated string reports StringKind, and Value.String() on a list formats
// as "[]" — text that is not empty — so a kind-only check would silently
// accept an unset required list.
func fieldIsEmpty(m protoreflect.Message, fd protoreflect.FieldDescriptor) bool {
	if fd.IsMap() {
		return m.Get(fd).Map().Len() == 0
	}
	if fd.IsList() {
		return m.Get(fd).List().Len() == 0
	}
	if isDurationField(fd) {
		return !m.Has(fd)
	}
	switch fd.Kind() {
	case protoreflect.StringKind:
		return m.Get(fd).String() == ""
	case protoreflect.BytesKind:
		return len(m.Get(fd).Bytes()) == 0
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return m.Get(fd).Int() == 0
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return m.Get(fd).Uint() == 0
	case protoreflect.BoolKind:
		return !m.Get(fd).Bool()
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return m.Get(fd).Float() == 0
	default:
		return !m.Has(fd)
	}
}

// listSeparator is the character that separates the elements of a repeated
// field inside the single string an env var or a flag can carry. A comma is
// the convention env vars already use for lists everywhere else (PATH-style
// colons lose to it because a colon is ordinary data in a URL or host:port).
const listSeparator = ","

// parseValue converts a raw string to the protoreflect.Value for fd,
// dispatching on the field's CARDINALITY before its kind.
//
// The cardinality check has to come first, and getting that wrong is what
// this function is shaped around: a `repeated string` field still reports
// protoreflect.StringKind, so a kind-only switch returns a scalar string for
// it and the caller's m.Set then panics with "assigning invalid type string",
// naming neither the field nor its cardinality. Every shape now resolves to
// either a value of the RIGHT cardinality or a named error, so m.Set can no
// longer be handed a mismatch.
//
// The message is needed because a list value must be allocated from it
// (m.NewField gives a new empty mutable list of the field's element type).
func parseValue(m protoreflect.Message, fd protoreflect.FieldDescriptor, raw string) (protoreflect.Value, error) {
	if fd.IsMap() {
		return protoreflect.Value{}, errMapField
	}
	if fd.IsList() {
		return parseList(m, fd, raw)
	}
	return parseElement(fd, raw)
}

// errMapField is the diagnostic for a config-bound map field. A map has no
// unambiguous single-string spelling (the key, the value, and the entry
// separator would all need escaping), so rather than invent one that breaks
// on the first key containing a comma, the loader refuses and points at the
// layer that CAN express it natively.
var errMapField = fmt.Errorf(
	"map fields cannot be set from an env var or flag — set it in the config file layer instead (--%s)",
	ConfigFlag)

// parseList builds a repeated field's value from the single string an env var
// or flag carries: elements are comma-separated and each is parsed with the
// field's ELEMENT kind, so a `repeated int32` gets three parsed ints rather
// than one raw string.
//
// Three deliberate decisions about the wire format:
//
//   - Surrounding whitespace is stripped from every element. "a, b" in a YAML
//     env block means two elements; the space is formatting, not data.
//   - Blank elements are dropped, so a trailing comma ("a,b,") is a
//     two-element list and not a list with an empty tail. This also makes an
//     empty flag value (`--allowed-registries=`) an EMPTY list rather than a
//     list holding one empty string: the way to clear a defaulted list. An
//     empty env var never reaches here — it is unset (see lookupEnv).
//   - There is no escape syntax. An element that legitimately contains a
//     comma, or one whose leading/trailing whitespace is significant, cannot
//     be expressed here and belongs in the config file, which carries a real
//     list and needs no encoding. A half-escape ("\," but not "\\,") would
//     read as support while still losing data on the values people actually
//     hit, which is worse than a limitation stated up front.
//
// Every layer REPLACES the whole list; there is no append. An env var that
// could only add to a compiled-in default would leave no way to remove an
// entry, which for a field like allowed_registries is a security-relevant
// difference — a deployment must be able to narrow the list, not just widen it.
func parseList(m protoreflect.Message, fd protoreflect.FieldDescriptor, raw string) (protoreflect.Value, error) {
	val := m.NewField(fd) // a new, empty, mutable list of fd's element type
	list := val.List()
	for i, part := range strings.Split(raw, listSeparator) {
		elem := strings.TrimSpace(part)
		if elem == "" {
			continue
		}
		ev, err := parseElement(fd, elem)
		if err != nil {
			// Name the offending ELEMENT. The caller names the field and the
			// env var, but with a list it quotes the whole raw value, and in
			// a twenty-registry string that leaves the reader to find which
			// one failed. The index counts position in the RAW string, so it
			// lines up with what the reader is looking at even though blanks
			// were dropped.
			return protoreflect.Value{}, fmt.Errorf("element %d (%q): %w", i+1, elem, err)
		}
		list.Append(ev)
	}
	return val, nil
}

// parseElement converts a raw string to a single value of fd's kind — the
// whole value for a scalar field, one element for a repeated one. It mirrors
// the generated parse helpers (parseInt32, parseInt64, parseBool,
// parseFloat32/64, parseGoDuration, parseString). Duration messages parse a
// Go duration string into a google.protobuf.Duration; the check is on the
// field's message TYPE, so it covers a repeated Duration element too.
func parseElement(fd protoreflect.FieldDescriptor, raw string) (protoreflect.Value, error) {
	if isDurationField(fd) {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfMessage(durationpb.New(d).ProtoReflect()), nil
	}

	switch fd.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(raw), nil
	case protoreflect.BytesKind:
		return protoreflect.ValueOfBytes([]byte(raw)), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		v, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfInt32(int32(v)), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfInt64(v), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		v, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfUint32(uint32(v)), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfUint64(v), nil
	case protoreflect.BoolKind:
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfBool(v), nil
	case protoreflect.FloatKind:
		v, err := strconv.ParseFloat(raw, 32)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfFloat32(float32(v)), nil
	case protoreflect.DoubleKind:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfFloat64(v), nil
	default:
		// The caller wraps this with the field name and the source layer, so
		// it states only what the caller cannot know: the shape, and where a
		// field of this shape CAN be set.
		return protoreflect.Value{}, fmt.Errorf(
			"unsupported field kind %s — a field this shape cannot be spelled in an env var or flag; set it in the config file layer instead (--%s)",
			fd.Kind(), ConfigFlag)
	}
}

// parseDefaultInt parses a flag default, treating "" as 0 (the proto zero)
// so a field without a default registers a zero-valued flag.
func parseDefaultInt(s string, bits int) (int64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseInt(s, 10, bits)
}

func parseDefaultUint(s string, bits int) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseUint(s, 10, bits)
}

func parseDefaultFloat(s string, bits int) (float64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, bits)
}
