package codegen

import (
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/configproto"
	"github.com/reliant-labs/forge/internal/templates"
	forgeconfig "github.com/reliant-labs/forge/pkg/config"
)

// ConfigTemplateField holds template data for a single config field.
type ConfigTemplateField struct {
	GoName         string
	GoType         string
	EnvVar         string
	Flag           string
	DefaultValue   string
	Description    string
	Required       bool
	HasDefault     bool
	DefaultInt32   int32
	DefaultInt64   int64
	DefaultBool    bool
	DefaultFloat32 float32
	DefaultFloat64 float64

	// GoPath is the assignment path on the generated Config struct.
	// Root fields: identical to GoName ("Port"). Component config-block
	// leaves: qualified through the block field ("Trader.MaxPerTick").
	// The Load/flag plumbing in config.go.tmpl assigns via GoPath so one
	// flat loop covers both shapes.
	GoPath string

	// IsDuration marks duration-shaped string fields (see
	// isDurationField). They are emitted as time.Duration on the Config
	// struct and parsed ONCE in Load — consumers never re-parse strings,
	// and a typo'd duration fails startup instead of silently zeroing.
	IsDuration bool

	// StructGoType is the Go type emitted on the Config struct:
	// "time.Duration" for duration fields, GoType for everything else.
	StructGoType string

	// ParseFn names the parse helper Load feeds to loadField for this
	// field ("parseString", "parseInt32", "parsePort", "parseGoDuration",
	// …). Selecting it at generate time keeps the emitted Load a flat
	// list of identical one-liners.
	ParseFn string

	// Role is the (forge.v1.config).role annotation (bare enum spelling, ""
	// for none). Codegen selects semantic fields (e.g. the MODE field) by
	// THIS, never by the field's name.
	Role string

	// Sensitive mirrors (forge.v1.config).sensitive. Sensitive fields get NO
	// CLI flag (defense against shell-history / `ps` leaks) and are resolved
	// from env / Secret mount only — never a flag or an inline default.
	Sensitive bool

	// EnvVarAliases are the further env vars the runtime reads for this
	// field when EnvVar is unset ((forge.v1.config).env_var_aliases), by
	// name. Read by validateConfigPathEnvs: an alias is read like EnvVar.
	EnvVarAliases []string
}

// ConfigTemplateBlockType is one component config-block struct type the
// template declares alongside Config (e.g. `type TraderConfig struct`).
// Deduped by TypeName when the same block message is referenced by more
// than one root field.
type ConfigTemplateBlockType struct {
	TypeName string
	Fields   []ConfigTemplateField
}

// ConfigTemplateBlockField is one block-typed field on the root Config
// struct (e.g. `Trader TraderConfig`).
type ConfigTemplateBlockField struct {
	GoName   string // field on Config, e.g. "Trader"
	TypeName string // block struct type, e.g. "TraderConfig"
}

// ConfigTemplateData is the top-level data passed to the config.go template.
type ConfigTemplateData struct {
	// Graph is a composition-graph project: the loader gains LoadReach, the
	// load each root's process runs (cmd/<bin>/cmd/roots_gen.go).
	Graph bool
	// Fields is every leaf field — root fields plus component config-block
	// leaves — in declaration order, with GoPath set. Drives RegisterFlags
	// and Load so block leaves get the exact same env/flag/default
	// treatment as root fields.
	Fields []ConfigTemplateField
	// RootFields are the leaves declared directly on Config (struct decl).
	RootFields []ConfigTemplateField
	// BlockTypes / BlockFields carry the component config-block shapes:
	// the struct type declarations and the Config fields holding them.
	BlockTypes  []ConfigTemplateBlockType
	BlockFields []ConfigTemplateBlockField
	// BlockAliases are the block types pkg/config re-exports under their own
	// names (`type TraderConfig = configv1.TraderConfig`), so a component
	// takes its block as `Cfg *config.TraderConfig` and compose.go fills it
	// from the loaded Config by type. A block whose name pkg/config already
	// declares for something else (Mode, a per-binary config) is left out
	// rather than redeclared.
	BlockAliases []string
	// RoleModeField is the Go field name of the field tagged
	// role=CONFIG_FIELD_ROLE_MODE, or "" when no field carries it. The
	// generated Mode() reads THIS field — selected by
	// annotation, never by the name "Environment". Renaming the role field
	// is a behavior no-op; naming an unannotated field "environment" never
	// enables dev mode.
	RoleModeField string
	NeedsStrconv  bool

	// Module is the project's Go module path. Set by GenerateConfigLoader;
	// left empty by callers that only need the field partition (e.g.
	// ConfigBlocksFromMessages).
	Module string
	// ConfigImport is the Go package the config messages generate into
	// (<module>/gen/<project>/config/v1), which config.go.tmpl imports as
	// configv1; ConfigDir is the proto directory declaring them
	// (proto/<project>/config/v1). Both set by GenerateConfigLoader — see
	// internal/configproto for why the project's name is in them.
	ConfigImport string
	ConfigDir    string

	// BinaryConfigs are the per-binary config surfaces, one per config
	// message carrying a (forge.v1.binary_config).binary annotation. EMPTY
	// for the common project that annotates nothing — config.go.tmpl then
	// emits exactly what it always did, so per-binary config costs the
	// single-binary case nothing.
	BinaryConfigs []BinaryConfig

	// RootConfigMessage names the project-global config message that
	// `Config`, `RegisterFlags` and `Load` are emitted for — "AppConfig" in
	// every scaffolded project. It is empty only when a project has moved
	// ENTIRELY to per-binary configs and deleted its AppConfig, in which
	// case the template emits the per-binary surfaces alone rather than
	// aliasing a type that no longer exists.
	RootConfigMessage string

	// PrimaryConfigMessage names the BinaryConfig bound to the project's
	// PRIMARY binary (the one with cmd/<bin>/cmd/root.go — the server
	// binary every scaffolded project has), used ONLY when
	// RootConfigMessage is empty.
	//
	// The primary binary's own compose graph — providers.go, compose.go,
	// mounts_services_gen.go, the cmd-tree-*.go.tmpl scaffold, every
	// service/worker/internal-package Deps — references `config.Config` /
	// `config.RegisterFlags` / `config.Load` / `config.Validate` /
	// `config.ModeOf` unconditionally; those templates were never written
	// to be all-binary-aware because in the common (single root AppConfig)
	// project there is nothing else for them to say. Rather than teach 20+
	// templates a second vocabulary, config.go.tmpl aliases that SAME
	// symbol set onto whichever message the primary binary itself loads —
	// which is also the semantically right binding: the server's
	// component graph should read the server's own config, not a
	// different binary's.
	//
	// Set by GenerateConfigLoader (never by BuildConfigTemplateData, which
	// has no filesystem access to find the primary binary). Empty when
	// RootConfigMessage is set (the common case — irrelevant) or when no
	// primary binary was found (a CLI/library project with no cmd tree
	// yet — config.go.tmpl then falls back to emitting no root alias at
	// all, matching the historical all-binary behavior for such
	// projects).
	PrimaryConfigMessage string

	// ProjectConfigPathEnv is the environment variable that names the
	// project-global config's file when --config is not given:
	// <PROJECT>_CONFIG (forgeconfig.ConfigPathEnvFor of the project name).
	// RootConfigMessage carries no (forge.v1.binary_config) annotation for the
	// runtime to derive a name from, so the generated RegisterFlags names it
	// on the --config flag. Set by GenerateConfigLoader.
	ProjectConfigPathEnv string

	// PrimaryConfigPathEnv is PrimaryConfigMessage's binary's
	// <BINARY>_CONFIG, for the generated doc comments. Set alongside
	// PrimaryConfigMessage.
	PrimaryConfigPathEnv string
}

// BinaryConfig is one binary's complete config surface: the message that
// carries its (forge.v1.binary_config).binary annotation, resolved into the
// shape the templates and the KCL projection consume.
//
// One of these per binary is what makes ownership DISJOINT. Every consumer
// — the Go loader shim, the KCL schema, the env projection — is built from
// exactly one BinaryConfig, so a field deleted from one binary's message
// is absent from that binary's flags, schema and Deployment env and reaches
// no other binary.
type BinaryConfig struct {
	// Binary is the cmd/<name> leaf this config belongs to.
	Binary string
	// Command is the cobra path below Binary whose process loads this
	// config (["server", "api"]); empty for the binary's ROOT config. See
	// ConfigMessage.Command.
	Command []string
	// Subtrees are the command paths at or below which this config is
	// loaded; empty unless it is a subtree config. See ConfigMessage.Subtrees.
	Subtrees [][]string
	// MessageName is the config message's proto name (e.g. "AdminConfig") —
	// the generated Go type this binary's Config aliases.
	MessageName string
	// Fields is every leaf this config resolves, composed blocks included,
	// each with its GoPath set. This is what the process's env projection and
	// its Deployment's env vars are built from: the union of what the
	// process actually reads, and nothing else. Every one of them registers
	// in ONE flag set — persistent on the root for a root config, local on
	// the subcommand for a command config — so composition shares a
	// DEFINITION, never a flag home.
	Fields []ConfigTemplateField
	// Blocks are the config blocks this message composes, in declaration
	// order — the shared definitions, named so the template can reach them.
	Blocks []ConfigBlockRef
	// BinaryHasRootConfig reports, on a command or subtree config, whether its binary
	// also binds a root config. That root config owns the --config file flag
	// (persistent on the binary's root), so the command config registers its
	// leaves alone rather than a second --config shadowing the inherited one.
	BinaryHasRootConfig bool
	// Reach is true when the loader also gets Load<Msg>Reach: a config of a
	// composition-graph project's primary binary, which roots_gen.go loads
	// with each root's reach (markReachLoaders).
	Reach bool
}

// markReachLoaders marks the configs whose loader gains Load<Msg>Reach: in a
// composition-graph project, the primary binary's (the ones roots_gen.go
// loads). Nothing else calls one, so no other binary's — and no project
// without a command tree for the roots — gets it.
func markReachLoaders(data *ConfigTemplateData, primaryBinary string) {
	for i := range data.BinaryConfigs {
		bc := &data.BinaryConfigs[i]
		bc.Reach = data.Graph && primaryBinary != "" && bc.Binary == primaryBinary
	}
}

// Process is the process this config is loaded by, as typed at a shell:
// "reliant" for a root config, "reliant server api" for a command config.
func (bc BinaryConfig) Process() string {
	if len(bc.Subtrees) > 0 {
		// Several processes: name the first subtree's, marked as a subtree.
		return strings.TrimSpace(bc.Binary+" "+strings.Join(bc.Subtrees[0], " ")) + " …"
	}
	return ConfigMessage{Binary: bc.Binary, Command: bc.Command}.ProcessName()
}

// PathEnv is the environment variable that names this config's file when
// --config is not given: its binary's <BINARY>_CONFIG, shared by the binary's
// root config and every one of its command configs, and derived by the
// runtime from the same (forge.v1.binary_config).binary.
func (bc BinaryConfig) PathEnv() string { return forgeconfig.ConfigPathEnvFor(bc.Binary) }

// CommandLine is the subcommand path alone ("server api"); "" for a root
// config.
func (bc BinaryConfig) CommandLine() string { return strings.Join(bc.Command, " ") }

// CommandGoArgs renders Command as Go string-literal arguments
// (`"server", "api"`), for the generated loader's path check.
func (bc BinaryConfig) CommandGoArgs() string {
	q := make([]string, len(bc.Command))
	for i, c := range bc.Command {
		q[i] = strconv.Quote(c)
	}
	return strings.Join(q, ", ")
}

// SubtreeLines renders each subtree path as typed at a shell, below the
// binary ("daemon", "auth login").
func (bc BinaryConfig) SubtreeLines() []string {
	out := make([]string, len(bc.Subtrees))
	for i, p := range bc.Subtrees {
		out[i] = strings.Join(p, " ")
	}
	return out
}

// SubtreesGoArgs renders Subtrees as Go []string literals
// (`[]string{"daemon"}, []string{"open"}`), for the generated loader's
// subtree check.
func (bc BinaryConfig) SubtreesGoArgs() string {
	parts := make([]string, len(bc.Subtrees))
	for i, p := range bc.Subtrees {
		q := make([]string, len(p))
		for j, c := range p {
			q[j] = strconv.Quote(c)
		}
		parts[i] = "[]string{" + strings.Join(q, ", ") + "}"
	}
	return strings.Join(parts, ", ")
}

// BinaryConfigsFromMessages resolves the per-binary config surfaces from a
// project's parsed config messages: one BinaryConfig per message carrying a
// (forge.v1.binary_config) annotation — a binary's root config or one of its
// command configs — in declaration order.
//
// A project that annotates nothing gets NO BinaryConfigs, and every caller
// falls back to the single project-global AppConfig path — which is why
// per-binary config is additive and the common single-binary project is
// unchanged. Selection is by ANNOTATION, never by message name.
func BinaryConfigsFromMessages(messages []ConfigMessage) []BinaryConfig {
	byName := make(map[string]*ConfigMessage, len(messages))
	for i := range messages {
		byName[messages[i].Name] = &messages[i]
	}

	var out []BinaryConfig
	for i := range messages {
		m := &messages[i]
		if m.Binary == "" {
			continue
		}
		bc := BinaryConfig{Binary: m.Binary, Command: m.Command, Subtrees: m.Subtrees, MessageName: m.Name}
		for _, f := range m.Fields {
			// A message-typed field naming another config message is a
			// composed block — a shared DEFINITION. Its leaves keep their
			// own annotations and are qualified through the block field,
			// exactly as component config blocks already are.
			if f.ProtoType == "message" && f.MessageType != "" {
				bm, known := byName[f.MessageType]
				if !known {
					continue
				}
				bc.Blocks = append(bc.Blocks, ConfigBlockRef{FieldName: f.GoName, TypeName: f.MessageType})
				for _, bf := range bm.Fields {
					if bf.ProtoType == "message" && bf.MessageType != "" {
						continue // one nesting level, as elsewhere
					}
					bc.Fields = append(bc.Fields, configTemplateField(bf, f.GoName+"."+bf.GoName))
				}
				continue
			}
			bc.Fields = append(bc.Fields, configTemplateField(f, f.GoName))
		}
		out = append(out, bc)
	}
	hasRoot := map[string]bool{}
	for _, bc := range out {
		if len(bc.Command) == 0 && len(bc.Subtrees) == 0 {
			hasRoot[bc.Binary] = true
		}
	}
	for i := range out {
		scoped := len(out[i].Command) > 0 || len(out[i].Subtrees) > 0
		out[i].BinaryHasRootConfig = scoped && hasRoot[out[i].Binary]
	}
	return out
}

// ConfigFieldsForMessage returns the raw ConfigFields one binary-bound config
// message resolves — its own leaves plus the leaves of every block it
// composes — in the order the KCL emitters expect. It is the ConfigField-
// level twin of BinaryConfig.Fields, kept separate because the KCL
// projection works from the parsed proto fields (env_var/sensitive/default),
// not the Go template shape.
//
// This is what makes a workload's Deployment carry only ITS process's env
// vars: the projection for a config is emitted from exactly this set.
//
// Keyed on the MESSAGE, not the binary: one binary binds several messages
// (its root config and one per command), and a lookup by binary returned the
// first message's fields for all of them.
func ConfigFieldsForMessage(messages []ConfigMessage, name string) []ConfigField {
	for i := range messages {
		m := &messages[i]
		if m.Name != name {
			continue
		}
		// Shared flattener, so a leaf name claimed by two composed blocks is
		// qualified instead of declared twice in this config's schema. See
		// config_block_flatten.go — the bare-name version made a config
		// failure that `forge generate` only warns about, which left the
		// previous config_gen.k on disk missing both blocks entirely.
		return FlattenFieldsWithBlocks(m.Fields, messages)
	}
	return nil
}

// ConfigBlockRef names one component config block as composed on the
// root Config: the Config field holding it and the generated Go type.
// wire_gen consumes this (via ConfigBlocksFromMessages) to resolve Deps
// fields of a block type to `cfg.<FieldName>` by TYPE.
type ConfigBlockRef struct {
	FieldName string // root Config field, e.g. "Trader"
	TypeName  string // generated struct type, e.g. "TraderConfig"
}

// ConfigBlocksFromMessages derives the component config-block references
// from parsed config messages: every message-typed field on a root
// config message whose MessageType names another config message in the
// set. Order follows root-message declaration order, so consumers get a
// deterministic candidate list.
func ConfigBlocksFromMessages(messages []ConfigMessage) []ConfigBlockRef {
	data := BuildConfigTemplateData(messages)
	refs := make([]ConfigBlockRef, 0, len(data.BlockFields))
	for _, bf := range data.BlockFields {
		refs = append(refs, ConfigBlockRef{FieldName: bf.GoName, TypeName: bf.TypeName})
	}
	return refs
}

// BuildConfigTemplateData partitions parsed config messages into the
// template shape:
//
//   - Block messages — those referenced by a MessageType field of another
//     config message — become nested struct types (`type TraderConfig
//     struct`) plus a typed field on Config (`Trader TraderConfig`).
//   - Every other message's scalar fields flatten onto the root Config
//     struct exactly as before (most projects have a single AppConfig).
//
// Block leaves keep their own env_var/flag/default annotations and join
// the flat Fields list with a qualified GoPath, so env binding, flag
// registration, and per-env deploy projection all reuse the existing
// flat plumbing unchanged.
//
// One nesting level is supported: message-typed fields ON a block
// message are ignored. References to messages that aren't in the set
// (or carry no config fields) are skipped.
func BuildConfigTemplateData(messages []ConfigMessage) ConfigTemplateData {
	byName := make(map[string]*ConfigMessage, len(messages))
	for i := range messages {
		byName[messages[i].Name] = &messages[i]
	}

	// A message is a block iff some OTHER message references it via a
	// MessageType field. Everything else is a root message.
	isBlock := map[string]bool{}
	for _, m := range messages {
		for _, f := range m.Fields {
			if f.ProtoType != "message" || f.MessageType == "" || f.MessageType == m.Name {
				continue
			}
			if _, known := byName[f.MessageType]; known {
				isBlock[f.MessageType] = true
			}
		}
	}

	data := ConfigTemplateData{BinaryConfigs: BinaryConfigsFromMessages(messages)}
	seenBlockType := map[string]bool{}
	for _, m := range messages {
		if isBlock[m.Name] {
			continue
		}
		// A message bound to a binary is that binary's config, emitted
		// through data.BinaryConfigs. Flattening its fields onto the root
		// Config too would put every binary's fields (and secrets) back on
		// one shared type — precisely what per-binary config exists to
		// prevent.
		//
		// A message bound to a FRONTEND is skipped for the same reason and
		// one more: it is not Go config at all. Its projections are the
		// TypeScript module, the KCL schema and the browser's config.js —
		// there is no binary that loads it, so aliasing `Config` to it
		// would type the whole backend against a browser's config surface.
		//
		// Skipping BOTH is what the reachability lint already assumes
		// (see rootConfigMessage in lint_config_reach.go). Before the
		// scaffold declared a frontend config, no project reached this
		// branch with one, so the asymmetry was invisible: a project whose
		// frontend message sorted ahead of AppConfig — `admin_config.proto`
		// before `config.proto` — got `type Config = configv1.AdminConfig`
		// and every cfg.Jwt* reference in internal/app stopped compiling.
		if m.Binary != "" || m.Frontend != "" {
			continue
		}
		// The first unbound, non-block message is the project-global config
		// the `Config` alias is emitted for (AppConfig in every scaffold).
		if data.RootConfigMessage == "" {
			data.RootConfigMessage = m.Name
		}
		for _, f := range m.Fields {
			if f.ProtoType == "message" {
				if f.MessageType == "" || !isBlock[f.MessageType] {
					// A message-typed config field that is NOT a component
					// config block is a well-known wrapper carrying its own
					// (forge.v1.config) annotation — in practice a
					// google.protobuf.Duration leaf (pre_stop_delay,
					// shutdown_timeout, db_conn_max_*). It binds to a single
					// env var / flag like any scalar, so it MUST flow into
					// the per-env KCL projection. Emit it as
					// a leaf (NOT a struct field — the config object is the
					// proto type now, so there is no generated struct). Skip
					// only un-annotated message fields (genuinely nothing to
					// bind).
					if f.EnvVar != "" || f.Flag != "" {
						tf := configTemplateField(f, f.GoName)
						data.Fields = append(data.Fields, tf)
					}
					continue
				}
				bm := byName[f.MessageType]
				data.BlockFields = append(data.BlockFields, ConfigTemplateBlockField{
					GoName:   f.GoName,
					TypeName: f.MessageType,
				})
				if !seenBlockType[f.MessageType] {
					seenBlockType[f.MessageType] = true
					bt := ConfigTemplateBlockType{TypeName: f.MessageType}
					for _, bf := range bm.Fields {
						if bf.ProtoType == "message" {
							continue // one nesting level only
						}
						bt.Fields = append(bt.Fields, configTemplateField(bf, bf.GoName))
					}
					data.BlockTypes = append(data.BlockTypes, bt)
				}
				for _, bf := range bm.Fields {
					if bf.ProtoType == "message" {
						continue
					}
					data.Fields = append(data.Fields, configTemplateField(bf, f.GoName+"."+bf.GoName))
				}
				continue
			}

			tf := configTemplateField(f, f.GoName)
			data.RootFields = append(data.RootFields, tf)
			data.Fields = append(data.Fields, tf)

			// Select the MODE field by ANNOTATION, never by name. The first
			// root field tagged role=MODE wins; a project should declare at
			// most one. This is the substitute for the deleted
			// `index .FieldNames "Environment"` name-magic.
			if data.RoleModeField == "" && f.Role == "CONFIG_FIELD_ROLE_MODE" {
				data.RoleModeField = tf.GoName
			}
		}
	}

	for _, f := range data.Fields {
		if f.GoType != "string" {
			data.NeedsStrconv = true
			break
		}
	}
	data.BlockAliases = configBlockAliases(data)
	return data
}

// configPackageIdentifiers are the names config.go.tmpl declares in
// pkg/config regardless of the project's messages.
var configPackageIdentifiers = map[string]bool{
	"Config": true, "Mode": true, "ModeProduction": true, "ModeDevelopment": true,
	"ConfigPathEnv": true, "RegisterFlags": true, "Load": true, "Defaults": true,
	"ModeOf": true, "Validate": true,
}

// configBlockAliases returns the block type names pkg/config can alias
// without colliding with an identifier it already declares.
func configBlockAliases(data ConfigTemplateData) []string {
	taken := make(map[string]bool, len(configPackageIdentifiers)+len(data.BinaryConfigs))
	for name := range configPackageIdentifiers {
		taken[name] = true
	}
	for _, bc := range data.BinaryConfigs {
		taken[bc.MessageName] = true
	}
	var out []string
	for _, bt := range data.BlockTypes {
		if !taken[bt.TypeName] {
			taken[bt.TypeName] = true
			out = append(out, bt.TypeName)
		}
	}
	return out
}

// isDurationField reports whether a string config field carries a Go
// duration. Two signals:
//
//  1. The four well-known scaffold duration fields by name — the
//     generated cmd/server.go assigns them to serverkit's time.Duration
//     config directly, so their typing is part of the contract.
//  2. Any other string field whose default parses as a Go duration AND
//     contains a unit letter (so a plain "0" or numeric default stays a
//     string).
func isDurationField(f ConfigField) bool {
	if f.GoType != "string" {
		return false
	}
	switch f.GoName {
	case "PreStopDelay", "ShutdownTimeout", "DbConnMaxIdleTime", "DbConnMaxLifetime", "DbConnectTimeout":
		return true
	}
	if f.DefaultValue == "" {
		return false
	}
	if _, err := time.ParseDuration(f.DefaultValue); err != nil {
		return false
	}
	return strings.ContainsAny(f.DefaultValue, "nsuµmh")
}

// parseFnFor selects the Load parse helper for a field.
func parseFnFor(f ConfigField, isDuration bool) string {
	if isDuration {
		return "parseGoDuration"
	}
	switch f.GoType {
	case "int32":
		// Ports get strict uint16 validation.
		if f.GoName == "Port" || f.Flag == "port" {
			return "parsePort"
		}
		return "parseInt32"
	case "int64":
		return "parseInt64"
	case "bool":
		return "parseBool"
	case "float32":
		return "parseFloat32"
	case "float64":
		return "parseFloat64"
	default:
		return "parseString"
	}
}

// configTemplateField converts one parsed ConfigField to its template
// shape, pre-parsing typed defaults and recording the assignment path.
func configTemplateField(f ConfigField, goPath string) ConfigTemplateField {
	isDur := isDurationField(f)
	structType := f.GoType
	if isDur {
		structType = "time.Duration"
	}
	tf := ConfigTemplateField{
		GoName:       f.GoName,
		GoType:       f.GoType,
		EnvVar:       f.EnvVar,
		Flag:         f.Flag,
		DefaultValue: f.DefaultValue,
		Description:  f.Description,
		Required:     f.Required,
		HasDefault:   f.DefaultValue != "",
		GoPath:       goPath,
		IsDuration:   isDur,
		StructGoType: structType,
		ParseFn:      parseFnFor(f, isDur),
		Role:         f.Role,
		Sensitive:    f.Sensitive,
	}
	for _, a := range f.EnvVarAliases {
		tf.EnvVarAliases = append(tf.EnvVarAliases, a.Name)
	}

	if f.DefaultValue != "" {
		switch f.GoType {
		case "int32":
			if v, err := strconv.ParseInt(f.DefaultValue, 10, 32); err == nil {
				tf.DefaultInt32 = int32(v)
			}
		case "int64":
			if v, err := strconv.ParseInt(f.DefaultValue, 10, 64); err == nil {
				tf.DefaultInt64 = v
			}
		case "bool":
			if v, err := strconv.ParseBool(f.DefaultValue); err == nil {
				tf.DefaultBool = v
			}
		case "float32":
			if v, err := strconv.ParseFloat(f.DefaultValue, 32); err == nil {
				tf.DefaultFloat32 = float32(v)
			}
		case "float64":
			if v, err := strconv.ParseFloat(f.DefaultValue, 64); err == nil {
				tf.DefaultFloat64 = v
			}
		}
	}
	return tf
}

// CmdServerTemplateData holds the data passed to cmd-server.go.tmpl.
// It combines project-level data (Module) with config field awareness
// so the template can conditionally include code that references
// specific config fields. The generated runServer calls the OWNED
// app.SetupAuth() unconditionally (auth is code now, not config), so no
// auth-provider field is threaded here.
type CmdServerTemplateData struct {
	Module string
	// ConfigFields are the root config's own fields (RootConfigFieldNames):
	// the `cfg.<X>` reads the templates may make.
	ConfigFields map[string]bool

	// ConfigInputs are the primary binary's configs beside the root config,
	// each with the serve fields it declares (ServeConfigInputs). The graph
	// serve.go reads those off root.Configs; empty for a binary that loads
	// one config, which renders exactly as before.
	ConfigInputs []ServeConfigInput

	// Name is the primary binary's name — the cmd/<Name>/ leaf. The
	// scaffold-once command-tree templates reference it when they explain
	// where a file sits or what a command is invoked as (`<Name> server`,
	// the `-X <module>/cmd/<Name>/cmd.version=` ldflags path). Derived from
	// the cmd tree on disk by primaryCmdTreeDir, not re-read from
	// forge.yaml, so it agrees with the directory the file is written into
	// even for a project whose binary was renamed.
	Name string

	// RESTEnabled mirrors forge.yaml `api.rest: true`. When set the cmd
	// composition site serves vanguard's REST routes for the mounted
	// services in front of the bare mux (serverkit.RESTHandler).
	// Filled by generateCmdServerData from projectAPIRESTEnabled.
	RESTEnabled bool
}

// Serves reports whether some config the binary loads declares the serve
// field name: the root config, or a config beside it (ConfigInputs). The
// templates gate an import, or a step that needs the field wherever it is
// declared, on it.
func (d CmdServerTemplateData) Serves(name string) bool {
	if d.ConfigFields[name] {
		return true
	}
	for _, in := range d.ConfigInputs {
		if in.Fields[name] {
			return true
		}
	}
	return false
}

// GenerateCmdServer re-renders cmd/server.go with config field awareness.
// Called during `forge generate` so that cmd/server.go stays in sync with
// the actual config proto fields.
//
// cs is the project's checksum tracker — passing it keeps cmd/server.go
// out of `forge project audit`'s orphan/user-edited lists. A nil cs is tolerated.
func GenerateCmdServer(messages []ConfigMessage, targetDir string, cs *checksums.FileChecksums) error {
	return generateCmdServerData(CmdServerTemplateData{
		ConfigFields: RootConfigFieldNames(messages, targetDir),
		ConfigInputs: ServeConfigInputs(messages, targetDir),
	}, targetDir, cs)
}

// GenerateCmdServerWithFields renders cmd/server.go using a pre-built
// config field map (e.g. with migration fields stripped when the
// migrations feature is disabled).
func GenerateCmdServerWithFields(configFields map[string]bool, targetDir string, cs *checksums.FileChecksums) error {
	return GenerateCmdServerForConfigs(configFields, nil, targetDir, cs)
}

// GenerateCmdServerForConfigs is GenerateCmdServerWithFields for a binary
// that binds configs beside its root config: inputs are those configs
// (ServeConfigInputs), stripped the same way.
func GenerateCmdServerForConfigs(configFields map[string]bool, inputs []ServeConfigInput, targetDir string, cs *checksums.FileChecksums) error {
	return generateCmdServerData(CmdServerTemplateData{
		ConfigFields: configFields,
		ConfigInputs: inputs,
	}, targetDir, cs)
}

// generateCmdServerData is the shared render+write tail of the
// GenerateCmdServer* variants. Fills Module from go.mod.
func generateCmdServerData(data CmdServerTemplateData, targetDir string, cs *checksums.FileChecksums) error {
	modulePath, err := GetModulePath(targetDir)
	if err != nil {
		return fmt.Errorf("read module path: %w", err)
	}
	data.Module = modulePath
	data.RESTEnabled = projectAPIRESTEnabled(targetDir)

	// The SCAFFOLD-ONCE half of the command tree. Each of these files is
	// all decisions and no ceremony, so forge writes each exactly once and
	// never rewrites it:
	//
	//   - serve.go   the serve pipeline (auth posture, interceptor order,
	//                payload caps, CORS, readiness, teardown)
	//   - server.go  the all-services ServeSpec (what this process mounts
	//                and supervises, and whether boot fails on a gap)
	//   - version.go the ldflags-stamped variables (renaming one changes
	//                your build's -X targets)
	//   - db.go      migration policy (fail hard on a dirty schema vs
	//                auto-force; is "nothing pending" success)
	//
	// The invariant steps they used to carry live in pkg/serverkit,
	// pkg/cmdkit and pkg/migratekit now, which is what keeps an owned copy
	// on the upgrade path — those improvements arrive by dependency bump,
	// not re-render.
	//
	// These calls therefore only BIRTH a file for a tree that has none yet
	// (an older project, or one whose cmd layer predates it). An existing
	// copy is left untouched, edits and all — which is precisely the
	// migration path for projects whose copies were Tier-1 until now.
	treeDir := primaryCmdTreeDir(targetDir)
	if treeDir == "" {
		return nil
	}
	// treeDir is cmd/<bin>/cmd, so the binary name is its grandparent leaf.
	data.Name = filepath.Base(filepath.Dir(treeDir))

	scaffolds := []struct{ tmpl, file string }{
		{"cmd-tree-serve.go.tmpl", "serve.go"},
		{"cmd-tree-server.go.tmpl", "server.go"},
		{"cmd-tree-version.go.tmpl", "version.go"},
		{"cmd-tree-metrics.go.tmpl", "metrics.go"},
	}
	if GraphComposition(targetDir) {
		// The composition graph: serve.go is runRoot, the process policy
		// every root runs under, and there is no server.go — each root's
		// command, `server` included, is generated into roots_gen.go
		// (GenerateGraph).
		scaffolds[0].tmpl = "cmd-tree-serve-graph.go.tmpl"
		scaffolds = append(scaffolds[:1], scaffolds[2:]...)
	}
	// db.go is birthed only when the project HAS a migration story:
	// cmd/<bin>/cmd/root.go gates its newDBCmd(deps) call on the same
	// AutoMigrate config field, so emitting one without the other yields
	// either an unused function or an undefined reference.
	if data.ConfigFields["AutoMigrate"] {
		scaffolds = append(scaffolds, struct{ tmpl, file string }{"cmd-tree-db.go.tmpl", "db.go"})
	}

	for _, s := range scaffolds {
		content, rerr := templates.ProjectTemplates().Render(s.tmpl, data)
		if rerr != nil {
			return fmt.Errorf("render %s: %w", s.tmpl, rerr)
		}
		dest := filepath.Join(treeDir, s.file)
		if _, werr := writeForgeScaffoldOnce(targetDir, dest, content); werr != nil {
			return fmt.Errorf("write %s: %w", dest, werr)
		}
	}

	// internal/metrics/metrics.go is where the project's own metrics are
	// declared, and `metrics schema` (above) links it. Birthed with the tree
	// that imports it so the two can never be present without each other.
	content, rerr := templates.ProjectTemplates().Render(metricsPackageTemplate, data)
	if rerr != nil {
		return fmt.Errorf("render %s: %w", metricsPackageTemplate, rerr)
	}
	if _, werr := writeForgeScaffoldOnce(targetDir, metricsPackageFile, content); werr != nil {
		return fmt.Errorf("write %s: %w", metricsPackageFile, werr)
	}
	return nil
}

// The project's declared-metrics package: scaffold-once and the user's from
// birth. The CRUD Create shims record its example metric.
const (
	metricsPackageTemplate = "internal-metrics.go.tmpl"
	metricsPackageFile     = "internal/metrics/metrics.go"
)

// primaryCmdTreeDir returns the project-relative directory of the primary
// binary's command tree (cmd/<bin>/cmd), discovered by glob so this path
// doesn't need to re-read forge.yaml for the binary name.
//
// It anchors on root.go, NOT on any of the scaffold-once files that live
// beside it. Those are write-if-absent, so "it isn't there yet" is precisely
// the case that still needs a destination — anchoring on a file we might be
// about to create would make it uncreatable. root.go is the right anchor
// because it is Tier-1 (regenerated every run, hence always present in a
// service tree) and lives in exactly the directory they belong to. Returns
// "" when there is no cmd tree at all — a CLI/library kind, or a project
// whose tree has not been generated yet.
func primaryCmdTreeDir(targetDir string) string {
	matches, _ := filepath.Glob(filepath.Join(targetDir, "cmd", "*", "cmd", "root.go"))
	if len(matches) == 0 {
		return ""
	}
	rel, err := filepath.Rel(targetDir, filepath.Dir(matches[0]))
	if err != nil {
		return ""
	}
	return rel
}

// projectConfigName is the name a project-global config's path variable is
// derived from: forge.yaml's project name, which is also the name every
// scaffold gives the primary binary (cmd/<name>) — so a project that later
// binds its AppConfig to that binary keeps the same variable. A directory with
// no readable forge.yaml (a generator test) falls back to the last element of
// the Go module path, skipping a /vN major-version suffix.
func projectConfigName(targetDir, modulePath string) string {
	return configproto.ProjectName(targetDir, modulePath)
}

// isMajorVersionSuffix reports whether a module path element is a Go
// major-version suffix ("v2", "v10").
func isMajorVersionSuffix(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	_, err := strconv.Atoi(s[1:])
	return err == nil
}

// configGoImport is the one Go package every config message generates
// into, read off the compiled protos (each message's go_package), or
// fallback when the descriptor predates GoImportPath. pkg/config names
// every config message through a single import, so messages spread over
// two Go packages cannot be shimmed: that is refused here, naming both,
// rather than emitted as a pkg/config that does not compile.
func configGoImport(messages []ConfigMessage, fallback string) (string, error) {
	got := ""
	var first ConfigMessage
	for _, m := range messages {
		if m.GoImportPath == "" {
			continue
		}
		if got == "" {
			got, first = m.GoImportPath, m
			continue
		}
		if m.GoImportPath != got {
			return "", fmt.Errorf("config messages generate into two Go packages: %s (%s) is in %s, %s (%s) is in %s; "+
				"pkg/config imports one config package, so declare every config message in %s with that directory's package and go_package",
				first.Name, first.ProtoFile, got, m.Name, m.ProtoFile, m.GoImportPath, path.Dir("proto/"+first.ProtoFile))
		}
	}
	if got == "" {
		return fallback, nil
	}
	return got, nil
}

// validateConfigPathEnvs refuses a config field whose env var — its env_var
// or one of its env_var_aliases — is the config-FILE path variable of the
// process that reads it (<BINARY>_CONFIG, or the project's <PROJECT>_CONFIG
// for the project-global config). The loader would read the one variable
// twice — as a file path, and as the field's value — and whichever reading
// the operator meant, the other fails at startup.
func validateConfigPathEnvs(data ConfigTemplateData) error {
	var problems []string
	check := func(owner, pathEnv string, fields []ConfigTemplateField) {
		for _, f := range fields {
			for i, name := range append([]string{f.EnvVar}, f.EnvVarAliases...) {
				if pathEnv == "" || name != pathEnv {
					continue
				}
				what := "env_var"
				if i > 0 {
					what = "env_var_aliases entry"
				}
				problems = append(problems, fmt.Sprintf(
					"%s field %s declares %s %q, which is the variable that names %s's config file (--%s's environment twin)",
					owner, f.GoPath, what, name, owner, forgeconfig.ConfigFlag))
			}
		}
	}
	if data.RootConfigMessage != "" {
		check(data.RootConfigMessage, data.ProjectConfigPathEnv, data.Fields)
	}
	for _, bc := range data.BinaryConfigs {
		check(bc.MessageName, bc.PathEnv(), bc.Fields)
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("config env var collides with the config-file path variable:\n  - %s\nRename the field's env var: the file path variable is derived from the binary (or project) name and cannot be reassigned to a field",
		strings.Join(problems, "\n  - "))
}

// GenerateConfigLoader generates pkg/config/config.go from parsed config messages.
//
// cs is the project's checksum tracker. Passing it ensures the generated
// pkg/config/config.go is recorded so `forge project audit` doesn't flag it as an
// orphan. A nil cs is tolerated (file is still written).
func GenerateConfigLoader(messages []ConfigMessage, targetDir string, cs *checksums.FileChecksums) error {
	// Partition messages into root fields + component config blocks.
	// Most projects have a single flat AppConfig; projects with block
	// messages additionally get nested struct types on Config.
	data := BuildConfigTemplateData(messages)

	// "No ROOT fields" and "nothing to generate" are different things, and
	// conflating them was the fr-config-all-binary bug.
	//
	// data.Fields collects only the fields of UNANNOTATED root messages —
	// a message bound to a binary is deliberately excluded, because
	// flattening it onto the root Config would put every binary's fields
	// (and secrets) back on one shared type, which is exactly what
	// per-binary config exists to prevent. So a project that annotates
	// EVERY config message — the natural end state of the per-binary
	// migration — has an empty Fields by design while having a complete
	// per-binary surface to emit.
	//
	// Early-returning on len(Fields)==0 therefore emitted the per-binary
	// KCL while leaving pkg/config/config_gen.go STALE: the Go half kept
	// whatever it said before the migration, with no error and no warning.
	// A project had to keep a deliberately unannotated AppConfig to work
	// around it.
	//
	// The all-annotated project is LEGITIMATE, not a user error: the deploy
	// half already emits a per-binary KCL schema for exactly this shape, so
	// refusing it in the Go half would make forge reject a project its own
	// generator otherwise supports. config.go.tmpl already gates the root
	// alias/Load/RegisterFlags on .RootConfigMessage, so an all-annotated
	// project renders the per-binary surfaces alone and no root Config type
	// — which is the honest description of that project.
	//
	// Only a project with NO config surface at all is a genuine no-op.
	if len(data.Fields) == 0 && len(data.BinaryConfigs) == 0 {
		return nil
	}

	// No root AppConfig: resolve PrimaryConfigMessage so config.go.tmpl can
	// alias Config/RegisterFlags/Load/Validate/ModeOf onto the PRIMARY
	// binary's own per-binary config, rather than leaving those symbols
	// undefined for the 20+ other templates that reference them
	// unconditionally (see PrimaryConfigMessage's doc comment).
	if data.RootConfigMessage == "" && len(data.BinaryConfigs) > 0 {
		if treeDir := primaryCmdTreeDir(targetDir); treeDir != "" {
			primaryBinary := filepath.Base(filepath.Dir(treeDir))
			for _, bc := range data.BinaryConfigs {
				// The primary binary's ROOT config: the binary-wide surface
				// every one of its commands — and so its whole component
				// graph — loads. A command config is one process's surface
				// and cannot stand in for it.
				if bc.Binary == primaryBinary && len(bc.Command) == 0 && len(bc.Subtrees) == 0 {
					data.PrimaryConfigMessage = bc.MessageName
					data.PrimaryConfigPathEnv = bc.PathEnv()
					break
				}
			}
			// The primary binary EXISTS but no config message is bound
			// to it. Every service/worker/internal-package Deps.Config
			// field, providers.go's Infra.Cfg, compose.go, and the
			// scaffold-once cmd-tree-*.go.tmpl files all reference
			// config.Config unconditionally — there is nothing to alias
			// it to, and that is a real gap the templates cannot paper
			// over. Reject loudly at generate time rather than emit a
			// pkg/config/config_gen.go with no Config symbol and let 20
			// files fail to compile with no explanation of why.
			if data.PrimaryConfigMessage == "" {
				return fmt.Errorf(
					"no root AppConfig and no ROOT config message is bound to the primary binary %q (cmd/%s): "+
						"the primary binary's own component graph (services, workers, internal packages, "+
						"providers.go, compose.go) needs a Config type to construct against, and a config "+
						"scoped to one subcommand (`command: [...]`) is one process's surface, not the binary's. "+
						"Fix: annotate a config message with `option (forge.v1.binary_config) = {binary: %q};` "+
						"(no `command`), or restore an unannotated root AppConfig",
					primaryBinary, primaryBinary, primaryBinary)
			}
		}
		// treeDir == "": no cmd tree on disk yet (CLI/library project, or
		// `forge generate` running before the primary binary's tree has
		// been scaffolded). Leave PrimaryConfigMessage empty —
		// config.go.tmpl falls back to emitting the per-binary surfaces
		// alone, exactly the historical all-binary behavior for a project
		// with no primary binary to bind against.
	}

	// config.go.tmpl is a thin shim that aliases Config to the generated
	// proto type (<module>/gen/<project>/config/v1.AppConfig) and wraps the
	// descriptor-driven loader — it needs that package's import path.
	modulePath, err := GetModulePath(targetDir)
	if err != nil {
		return fmt.Errorf("read module path: %w", err)
	}
	data.Module = modulePath
	layout := configproto.ForProject(targetDir)
	data.ConfigDir = layout.Dir()
	if data.ConfigImport, err = configGoImport(messages, layout.GoImportPath(modulePath)); err != nil {
		return err
	}
	data.ProjectConfigPathEnv = forgeconfig.ConfigPathEnvFor(projectConfigName(targetDir, modulePath))
	data.Graph = GraphComposition(targetDir)
	primaryBinary := ""
	if treeDir := primaryCmdTreeDir(targetDir); treeDir != "" {
		primaryBinary = filepath.Base(filepath.Dir(treeDir))
	}
	markReachLoaders(&data, primaryBinary)

	if err := validateConfigPathEnvs(data); err != nil {
		return err
	}

	content, err := templates.ProjectTemplates().Render("config.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render config.go.tmpl: %w", err)
	}

	// Renamed to _gen so the name states the tier. This one mattered most:
	// pkg/config/config.go is a path every Go developer expects to own, and
	// its old spelling invited exactly the hand-edit that hash-guarding then
	// punished as permanent drift.
	RetireRenamedGenerated(targetDir, filepath.Join("pkg", "config", "config.go"), cs)
	if err := writeForgeOwned(targetDir, filepath.Join("pkg", "config", "config_gen.go"), content, cs); err != nil {
		return fmt.Errorf("write pkg/config/config_gen.go: %w", err)
	}
	return nil
}

// ConfigFieldNamesFromMessages returns a map of Go field names present in the
// given config messages. Used by templates to conditionally include code
// blocks that reference specific config fields.
func ConfigFieldNamesFromMessages(messages []ConfigMessage) map[string]bool {
	names := make(map[string]bool)
	for _, msg := range messages {
		for _, f := range msg.Fields {
			names[f.GoName] = true
		}
	}
	return names
}

// RootConfigFieldNames returns the Go field names the ROOT config declares at
// its top level: the message `config.Config` aliases — the project's
// AppConfig, or, when every message is bound to a binary, the primary
// binary's own root config. Those are the only `cfg.<X>` reads a template may
// make off *config.Config. A field of another binary's config, of a command
// or subtree config, of a frontend config, or of a block the root config
// composes is not one: `cfg.Port` on a root config with no port does not
// compile. Empty when the project has no root config.
func RootConfigFieldNames(messages []ConfigMessage, projectDir string) map[string]bool {
	names := make(map[string]bool)
	root := rootConfigMessageName(messages, primaryBinaryName(projectDir))
	for _, msg := range messages {
		if msg.Name != root {
			continue
		}
		for _, f := range msg.Fields {
			names[f.GoName] = true
		}
	}
	return names
}

// jwtConfigFields are the root-config-shaped fields the scaffolded
// internal/app/auth.go's JWT validator reads; brokerConfigFields the ones
// internal/app/login_broker.go reads.
var (
	jwtConfigFields = []string{"JwtSigningMethod", "JwtIssuer", "JwtAudience", "JwtJwksUrl", "JwtSecret",
		"JwtRolesClaim", "JwtJwksSnapshotFile", "JwtJwksSnapshot", "JwtJwksSnapshotMaxAge"}
	brokerConfigFields = []string{"IdpBase", "IdpHost", "IdpBrokerToken", "IdpBrokerTokenFile",
		"OidcClientId", "OidcRedirectUri", "IdpLoginUri", "Environment"}
)

// configDeclaring names the config of the primary binary a scaffold reading
// goNames reads them from: the one that declares every one of them at its top
// level — the root config ("", which the templates spell Config), else the
// first config beside it that does (its message name). found is false when no
// config the binary loads declares them all; the scaffold then reads none of
// them (F1: a scaffold may only read fields some config declares).
func configDeclaring(projectDir string, goNames []string) (typeName string, found bool) {
	messages, err := ParseConfigProtosFromDir(filepath.Join(projectDir, "proto", "config"))
	if err != nil || len(messages) == 0 {
		return "", true // no parsed config yet: the default scaffold's AppConfig
	}
	return configDeclaringIn(messages, projectDir, goNames)
}

// configDeclaringIn is configDeclaring over parsed messages.
func configDeclaringIn(messages []ConfigMessage, projectDir string, goNames []string) (typeName string, found bool) {
	declares := func(fields map[string]bool) bool {
		for _, n := range goNames {
			if !fields[n] {
				return false
			}
		}
		return true
	}
	if declares(RootConfigFieldNames(messages, projectDir)) {
		return "", true
	}
	if pc := processConfigsFrom(messages, primaryBinaryName(projectDir)); pc != nil {
		for _, in := range pc.inputs {
			own := map[string]bool{}
			for _, f := range in.msg.Fields {
				own[f.GoName] = true
			}
			if declares(own) {
				return in.msg.Name, true
			}
		}
	}
	return "", false
}

// primaryBinaryName is the primary binary's name — its cmd/<bin>/cmd tree's
// <bin> — or "" when the project has no command tree.
func primaryBinaryName(projectDir string) string {
	treeDir := primaryCmdTreeDir(projectDir)
	if treeDir == "" {
		return ""
	}
	return filepath.Base(filepath.Dir(treeDir))
}

// rootConfigMessageName is the message config.Config aliases: the first
// unbound root message (AppConfig), else binary's own root config — bound to
// it with neither a command nor subtrees. "" when there is neither.
func rootConfigMessageName(messages []ConfigMessage, binary string) string {
	if root := BuildConfigTemplateData(messages).RootConfigMessage; root != "" {
		return root
	}
	for _, m := range messages {
		if binary != "" && m.Binary == binary && len(m.Command) == 0 && len(m.Subtrees) == 0 {
			return m.Name
		}
	}
	return ""
}

// DefaultConfigFieldNames returns the field names from the default scaffold
// config proto. Used at initial project scaffold time before the config
// proto has been parsed by the generator.
func DefaultConfigFieldNames() map[string]bool {
	return ConfigFieldNamesFromMessages(DefaultConfigMessages())
}

// DefaultConfigMessages returns the default scaffold config metadata used
// before protoc-gen-forge has produced a descriptor for proto/<project>/config/v1/config.proto.
func DefaultConfigMessages() []ConfigMessage {
	return []ConfigMessage{
		{
			Name: "AppConfig",
			Fields: []ConfigField{
				{
					Name:         "port",
					GoName:       "Port",
					GoType:       "int32",
					ProtoType:    "int32",
					EnvVar:       "PORT",
					Flag:         "port",
					DefaultValue: "8080",
					Description:  "HTTP server port",
				},
				{
					Name:         "log_level",
					GoName:       "LogLevel",
					GoType:       "string",
					ProtoType:    "string",
					EnvVar:       "LOG_LEVEL",
					Flag:         "log-level",
					DefaultValue: "info",
					Description:  "Log level (debug, info, warn, error)",
				},
				{
					Name:      "database_url",
					GoName:    "DatabaseUrl",
					GoType:    "string",
					ProtoType: "string",
					EnvVar:    "DATABASE_URL",
					Flag:      "database-url",
					Required:  true,
					// Mirrors the scaffolded proto (config.proto.tmpl): the DSN
					// embeds the database password, so it is projected as a Secret
					// REFERENCE, never an inline manifest value. This fallback set
					// is what a project with no readable config proto renders from,
					// so it must not be the lenient one.
					Sensitive:   true,
					Description: "PostgreSQL connection string",
				},
				{
					Name:      "idp_broker_token",
					GoName:    "IdpBrokerToken",
					GoType:    "string",
					ProtoType: "string",
					EnvVar:    "IDP_BROKER_TOKEN",
					Flag:      "idp-broker-token",
					// Mirrors the scaffolded proto too, for the same reason as
					// database_url: the dev env with a frontend names it in the
					// API's config_secrets (the login broker's token, which `forge env up`
					// stores), and config_secrets may only name
					// a sensitive field. A fallback set without it could not
					// render that env at all.
					Sensitive:   true,
					Description: "The login broker's service-account token (native sign-in). SENSITIVE.",
				},
				{
					Name:        "cors_origins",
					GoName:      "CorsOrigins",
					GoType:      "string",
					ProtoType:   "string",
					EnvVar:      "CORS_ORIGINS",
					Flag:        "cors-origins",
					Description: "Comma-separated list of allowed CORS origins",
				},
				{
					Name:         "cors_allow_credentials",
					GoName:       "CorsAllowCredentials",
					GoType:       "bool",
					ProtoType:    "bool",
					EnvVar:       "CORS_ALLOW_CREDENTIALS",
					Flag:         "cors-allow-credentials",
					DefaultValue: "false",
					Description:  "Set Access-Control-Allow-Credentials: true on CORS responses. MUST NOT be combined with a wildcard origin ('*') — that combination is spec-invalid and will be rejected at startup.",
				},
				{
					Name:        "tls_cert_path",
					GoName:      "TlsCertPath",
					GoType:      "string",
					ProtoType:   "string",
					EnvVar:      "TLS_CERT_PATH",
					Flag:        "tls-cert-path",
					Description: "Path to the TLS certificate (PEM). When both this and TLS_KEY_PATH are set, the server listens over HTTPS; otherwise it serves plaintext. Setting only one of the two is a configuration error.",
				},
				{
					Name:        "tls_key_path",
					GoName:      "TlsKeyPath",
					GoType:      "string",
					ProtoType:   "string",
					EnvVar:      "TLS_KEY_PATH",
					Flag:        "tls-key-path",
					Description: "Path to the TLS private key (PEM). See TLS_CERT_PATH.",
				},
				{
					Name:         "pre_stop_delay",
					GoName:       "PreStopDelay",
					GoType:       "string",
					ProtoType:    "string",
					EnvVar:       "PRE_STOP_DELAY",
					Flag:         "pre-stop-delay",
					DefaultValue: "5s",
					Description:  "Duration to wait after flipping readiness to false before beginning HTTP shutdown. Gives load balancers time to observe the failing probe and stop routing new traffic (Go duration; 0 is the default, negative is no pause).",
				},
				{
					Name:         "shutdown_timeout",
					GoName:       "ShutdownTimeout",
					GoType:       "string",
					ProtoType:    "string",
					EnvVar:       "SHUTDOWN_TIMEOUT",
					Flag:         "shutdown-timeout",
					DefaultValue: "30s",
					Description:  "Maximum time to wait for in-flight requests and workers to drain during graceful shutdown (Go duration; 0 is the default, negative is no window).",
				},
				{
					Name:         "log_format",
					GoName:       "LogFormat",
					GoType:       "string",
					ProtoType:    "string",
					EnvVar:       "LOG_FORMAT",
					Flag:         "log-format",
					DefaultValue: "json",
					Description:  "Log output format. One of 'json' (structured, machine-readable) or 'text' (human-friendly). Any other value is rejected at startup.",
				},
				{
					Name:         "auto_migrate",
					GoName:       "AutoMigrate",
					GoType:       "bool",
					ProtoType:    "bool",
					EnvVar:       "AUTO_MIGRATE",
					Flag:         "auto-migrate",
					DefaultValue: "false",
					Description:  "Run database migrations on startup",
				},
				{
					Name:         "environment",
					GoName:       "Environment",
					GoType:       "string",
					ProtoType:    "string",
					EnvVar:       "ENVIRONMENT",
					Flag:         "environment",
					DefaultValue: "production",
					Role:         "CONFIG_FIELD_ROLE_MODE",
					Description:  "Runtime environment (production, development). In development, some defaults are permissive (e.g. verbose errors, relaxed CORS) for local ergonomics — never use development in production.",
				},
				{
					Name:         "rate_limit_rps",
					GoName:       "RateLimitRps",
					GoType:       "int32",
					ProtoType:    "int32",
					EnvVar:       "RATE_LIMIT_RPS",
					Flag:         "rate-limit-rps",
					DefaultValue: "100",
					Description:  "Per-key request rate limit (requests per second). 0 or negative disables rate limiting.",
				},
				{
					Name:         "rate_limit_burst",
					GoName:       "RateLimitBurst",
					GoType:       "int32",
					ProtoType:    "int32",
					EnvVar:       "RATE_LIMIT_BURST",
					Flag:         "rate-limit-burst",
					DefaultValue: "200",
					Description:  "Per-key rate limit burst size. Must be >= rate_limit_rps.",
				},
				{
					Name:         "db_max_open_conns",
					GoName:       "DbMaxOpenConns",
					GoType:       "int32",
					ProtoType:    "int32",
					EnvVar:       "DB_MAX_OPEN_CONNS",
					Flag:         "db-max-open-conns",
					DefaultValue: "25",
					Description:  "Maximum number of open database connections.",
				},
				{
					Name:         "db_max_idle_conns",
					GoName:       "DbMaxIdleConns",
					GoType:       "int32",
					ProtoType:    "int32",
					EnvVar:       "DB_MAX_IDLE_CONNS",
					Flag:         "db-max-idle-conns",
					DefaultValue: "5",
					Description:  "Maximum number of idle database connections kept in the pool.",
				},
				{
					Name:         "db_conn_max_idle_time",
					GoName:       "DbConnMaxIdleTime",
					GoType:       "string",
					ProtoType:    "string",
					EnvVar:       "DB_CONN_MAX_IDLE_TIME",
					Flag:         "db-conn-max-idle-time",
					DefaultValue: "5m",
					Description:  "Maximum amount of time a connection may be idle before being closed (Go duration, e.g. 5m).",
				},
				{
					Name:         "db_conn_max_lifetime",
					GoName:       "DbConnMaxLifetime",
					GoType:       "string",
					ProtoType:    "string",
					EnvVar:       "DB_CONN_MAX_LIFETIME",
					Flag:         "db-conn-max-lifetime",
					DefaultValue: "30m",
					Description:  "Maximum amount of time a connection may be reused before being closed (Go duration, e.g. 30m).",
				},
				{
					Name:         "db_connect_timeout",
					GoName:       "DbConnectTimeout",
					GoType:       "string",
					ProtoType:    "string",
					EnvVar:       "DB_CONNECT_TIMEOUT",
					Flag:         "db-connect-timeout",
					DefaultValue: "5m",
					Description:  "Total time to keep retrying the database connection at startup (capped exponential backoff with jitter) before the process exits non-zero. While retrying, the process stays up: /healthz passes and /readyz fails naming the database. The rendered startup probe window covers the 5m default; raise this only together with the probe window.",
				},
				{
					// pprof is ON BY DEFAULT, on a LOOPBACK side-listener. A
					// process being OOMKilled can say how much it holds, never
					// what — and a profiler you must redeploy to enable is one
					// you do not have when you need it. Loopback is what makes
					// always-on safe: present in every environment, routable
					// from none, and never given a k8s Service. Keep this in
					// step with the same field in
					// internal/templates/project/config.proto.tmpl.
					Name:         "pprof_addr",
					GoName:       "PprofAddr",
					GoType:       "string",
					ProtoType:    "string",
					EnvVar:       "PPROF_ADDR",
					Flag:         "pprof-addr",
					DefaultValue: "127.0.0.1:6060",
					Description:  "Address of the always-on net/http/pprof side-listener — never the app's public port. Defaults to loopback (127.0.0.1:6060), which is what makes it safe to leave on: reachable from inside the machine or pod running the process and from nowhere else, and never given a k8s Service. Grab a profile with: go tool pprof http://localhost:6060/debug/pprof/heap (in a cluster, kubectl port-forward <pod> 6060:6060 first). Set 0.0.0.0:6060 when a profile must cross a container boundary; --pprof-addr= (empty) disables pprof.",
				},
				{
					Name:         "security_headers_enabled",
					GoName:       "SecurityHeadersEnabled",
					GoType:       "bool",
					ProtoType:    "bool",
					EnvVar:       "SECURITY_HEADERS_ENABLED",
					Flag:         "security-headers-enabled",
					DefaultValue: "true",
					Description:  "Set OWASP security response headers (CSP, X-Content-Type-Options, Referrer-Policy, Permissions-Policy, HSTS in production). Disable only if a dedicated edge proxy already sets them.",
				},
				{
					// The probe paths: the KCL lowering reads the same env
					// values for a forge-built workload's default probe. Keep
					// in step with internal/templates/project/config.proto.tmpl.
					Name:         "liveness_paths",
					GoName:       "LivenessPaths",
					GoType:       "string",
					ProtoType:    "string",
					EnvVar:       "LIVENESS_PATHS",
					Flag:         "liveness-paths",
					DefaultValue: "/healthz",
					Description:  "Comma-separated paths the liveness probe answers on (200 while the process runs). The first is the one the deploy probes; the rest are aliases, for a platform that probes a fixed path (/health).",
				},
				{
					Name:         "readiness_paths",
					GoName:       "ReadinessPaths",
					GoType:       "string",
					ProtoType:    "string",
					EnvVar:       "READINESS_PATHS",
					Flag:         "readiness-paths",
					DefaultValue: "/readyz",
					Description:  "Comma-separated paths the readiness probe answers on (503 while booting, draining, or a dependency is down). The first is the one the deploy probes; the rest are aliases (/ready).",
				},
			},
		},
	}
}
