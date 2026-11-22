package config

// First-class config-FILE loading layer.
//
// This file adds a config file as a real, always-wired precedence layer to the
// descriptor-driven loader. The full precedence is:
//
//	defaults  →  config file  →  environment  →  flags
//	(earliest)                                   (wins)
//
// Each later layer OVERRIDES the earlier ones. The file layer is NOT optional
// and NOT dormant: RegisterFlags always registers a persistent `--config`
// flag, and Load always consults it (plus its environment twin).
//
// Resolution of the file path (see resolveConfigPath):
//
//   - the `--config <path>` flag, when explicitly set on this invocation, OR
//   - the binary's own path variable, <BINARY>_CONFIG (RELIANT_CONFIG for the
//     reliant binary; see ConfigPathEnvFor and configPathEnv).
//
// The flag wins over the variable. If neither is given, NO file is loaded and
// the loader proceeds with defaults → env → flags. That is normal precedence,
// not a fallback.
//
// The variable is named after the BINARY on purpose. A name every forge-built
// binary shared (it was FORGE_CONFIG) meant a shell that set it for one app
// made every other forge binary started from that shell — a desktop app's
// daemon inheriting the login environment, say — load a file written for a
// different schema and die at startup. A config file is only meaningful to the
// binary whose config messages it was written against, so its variable is that
// binary's alone.
//
// LOUD-ON-MISSING (no silent fallback): when a config path IS explicitly given
// (flag or env) but the file is missing, unreadable, or invalid, Load returns
// an error and aborts, naming where the path came from. A bad explicit path is
// never swallowed.
//
// Formats: protojson for `.json`; for `.yaml`/`.yml` the bytes are converted
// YAML→JSON (sigs.k8s.io/yaml) and then unmarshaled with protojson. Unmarshal
// is proto-native and generic over ANY config message: it honors the proto's
// own JSON field names and descends into nested config blocks automatically.
//
// Sensitive fields: the file MAY set a sensitive field (it is a file on disk,
// not shell history), exactly as env may. Sensitive fields still never get a
// flag. The protojson unmarshal does not know about the (sensitive) annotation
// — it simply sets whatever the file names — which is the intended behavior:
// file/env yes, flag no.
//
// ONE FILE PER PROCESS. A process may load several config messages from one
// file — a binary's root config and the command config of the subcommand it
// runs (`reliant server api` loads ReliantConfig and ApiServerConfig) — and it
// has ONE --config. So the file is FLAT, like the process's flags and env
// vars: its top-level keys are the union of the fields of every config the
// command loads, and each message takes its own keys (see ownDocument). The
// set of configs is the set REGISTERED on the command path through the cobra-
// level Register* functions (recordFileMessage), which is the declaration that
// the command reads them. A key no registered config declares is a loud error,
// so a misspelling still fails; a key two of them declare is refused rather
// than guessed at. Nested keys stay strict: each message decodes its own keys
// with protojson, which rejects an unknown field at any depth.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// ConfigFlag is the name of the always-registered persistent flag that points
// the loader at a config file. RegisterFlags registers it unconditionally.
const ConfigFlag = "config"

// configPathEnvAnnotation is the pflag annotation on the --config flag that
// carries the name of its environment twin. Recording the variable ON the flag
// makes the two one declaration, the way a field's flag and env_var are: every
// command that inherits --config resolves the same variable, whichever config
// message it is loading.
const configPathEnvAnnotation = "forge.config/path-env"

// ConfigPathEnvFor returns the environment variable that names binary's config
// file when --config is not given: the binary name upper-cased with every
// character that cannot appear in a variable name replaced by "_", plus
// "_CONFIG". "reliant" → RELIANT_CONFIG, "idp-provision-oneshot" →
// IDP_PROVISION_ONESHOT_CONFIG. "" for an empty binary.
//
// It is the single derivation both halves use: the runtime, for a message
// carrying (forge.v1.binary_config).binary, and forge's generator, for the
// generated help and docs and for a project-global config (named after the
// project).
func ConfigPathEnvFor(binary string) string {
	if binary == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(binary) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String() + "_CONFIG"
}

// RegisterConfigFlag registers the persistent --config <path> flag on cmd,
// with pathEnv as its environment twin: the variable that supplies the path
// when the flag is not given. An empty pathEnv registers a flag with no twin.
//
// It is a no-op when cmd or a parent already carries --config — the first
// registration on a command path names the variable for that whole path. That
// is what makes it an override: call it on a binary's root command before the
// generated Register*Flags, and every load beneath that root reads the
// variable named here instead of the derived <BINARY>_CONFIG.
//
// The generated pkg/config calls it for a project-global config, which carries
// no (forge.v1.binary_config) annotation to derive a name from.
func RegisterConfigFlag(cmd *cobra.Command, pathEnv string) {
	if lookupConfigFlag(cmd) != nil {
		return // own or inherited: a second declaration would shadow it
	}
	usage := "path to a config file (.json/.yaml/.yml). Precedence: defaults < file < env < flags"
	if pathEnv != "" {
		usage = fmt.Sprintf("path to a config file (.json/.yaml/.yml); also settable via %s. "+
			"Precedence: defaults < file < env < flags", pathEnv)
	}
	pf := cmd.PersistentFlags()
	pf.String(ConfigFlag, "", usage)
	if pathEnv != "" {
		// SetAnnotation only fails for a flag that does not exist; it was
		// registered on the line above.
		_ = pf.SetAnnotation(ConfigFlag, configPathEnvAnnotation, []string{pathEnv})
	}
}

// fileMessagesAnnotation is the cobra command annotation naming the config
// messages registered on that command (full names, comma-separated): the
// configs a load from it, or from a command beneath it, shares a config file
// with.
const fileMessagesAnnotation = "forge.config/messages"

// fileMessageIndex resolves a full name recorded in fileMessagesAnnotation to
// the descriptor that was registered under it. A cobra annotation holds only
// strings, and a config message need not be in the global registry (a
// dynamically built one is not), so the descriptor is kept here. It holds one
// entry per config message TYPE, so it is bounded by the program, not by how
// many commands it builds.
var fileMessageIndex sync.Map // protoreflect.FullName → protoreflect.MessageDescriptor

// recordFileMessage notes on cmd that a load from cmd's path reads msg, so the
// file layer knows which configs share the process's one config file. Every
// cobra-level Register* calls it.
func recordFileMessage(cmd *cobra.Command, msg proto.Message) {
	md := msg.ProtoReflect().Descriptor()
	name := string(md.FullName())
	fileMessageIndex.Store(md.FullName(), md)
	recorded := cmd.Annotations[fileMessagesAnnotation]
	for _, n := range strings.Split(recorded, ",") {
		if n == name {
			return
		}
	}
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	if recorded != "" {
		name = recorded + "," + name
	}
	cmd.Annotations[fileMessagesAnnotation] = name
}

// sharedFileMessages returns the configs a load of msg from cmd shares its
// config file with: every message recorded on cmd or an ancestor, other than
// msg itself, in no particular order.
func sharedFileMessages(cmd *cobra.Command, msg proto.Message) []protoreflect.MessageDescriptor {
	self := msg.ProtoReflect().Descriptor().FullName()
	seen := map[protoreflect.FullName]bool{self: true}
	var out []protoreflect.MessageDescriptor
	for c := cmd; c != nil; c = c.Parent() {
		for _, n := range strings.Split(c.Annotations[fileMessagesAnnotation], ",") {
			name := protoreflect.FullName(n)
			if n == "" || seen[name] {
				continue
			}
			seen[name] = true
			if md, ok := fileMessageIndex.Load(name); ok {
				out = append(out, md.(protoreflect.MessageDescriptor))
			}
		}
	}
	return out
}

// binaryOf returns the binary msg is bound to by its
// (forge.v1.binary_config) annotation, or "" for an unbound message.
func binaryOf(msg proto.Message) string {
	opts := msg.ProtoReflect().Descriptor().Options()
	if opts == nil {
		return ""
	}
	bc, ok := proto.GetExtension(opts, forgepb.E_BinaryConfig).(*forgepb.BinaryConfigOptions)
	if !ok || bc == nil {
		return ""
	}
	return bc.GetBinary()
}

// lookupConfigFlag finds the --config flag cmd resolves: its own, or one it
// inherits from a parent's persistent flags. cobra merges inherited
// persistent flags into Flags() only when a command executes, so the parents
// are walked as well — a load from a command that has not run yet (a test, a
// hand-built tree) must find the same flag the executed command would.
func lookupConfigFlag(cmd *cobra.Command) *pflag.Flag {
	if f := cmd.Flags().Lookup(ConfigFlag); f != nil {
		return f
	}
	for c := cmd; c != nil; c = c.Parent() {
		if f := c.PersistentFlags().Lookup(ConfigFlag); f != nil {
			return f
		}
	}
	return nil
}

// configPathEnv returns the variable that names msg's config file for a load
// from cmd: the twin recorded on the --config flag cmd resolves (see
// RegisterConfigFlag), else <BINARY>_CONFIG for a message bound to a binary,
// else "" — an unbound message loaded where no --config flag names a variable
// has no environment path, only --config.
func configPathEnv(cmd *cobra.Command, msg proto.Message) string {
	if cmd != nil {
		if f := lookupConfigFlag(cmd); f != nil {
			if v := f.Annotations[configPathEnvAnnotation]; len(v) > 0 && v[0] != "" {
				return v[0]
			}
		}
	}
	return ConfigPathEnvFor(binaryOf(msg))
}

// resolveConfigPath returns the explicit config-file path for this invocation,
// where it came from ("--config" or "env RELIANT_CONFIG", for error messages),
// and whether one was given. The --config flag (when Changed) wins over the
// path variable. A variable set to "" is unset, as every variable the loader
// reads is (lookupEnv). When this returns ok=false, no file is loaded (normal
// defaults→env→flags precedence).
func resolveConfigPath(cmd *cobra.Command, msg proto.Message, e environ) (path, source string, ok bool) {
	if cmd != nil {
		if f := lookupConfigFlag(cmd); f != nil && f.Changed && f.Value.String() != "" {
			return f.Value.String(), "--" + ConfigFlag, true
		}
	}
	if env := configPathEnv(cmd, msg); env != "" {
		if v, set := e.lookupEnv(env); set {
			return v, "env " + env, true
		}
	}
	return "", "", false
}

// applyConfigFile reads the file at path and OVERLAYS it onto msg, preserving
// the defaults layer for fields the file omits. It is the file precedence
// layer (between defaults and env). source names where the path came from
// ("--config", "env RELIANT_CONFIG") so a failure says what to fix.
//
// Implementation note: protojson.Unmarshal RESETS its target before decoding,
// so it cannot decode directly into the defaults-populated msg without wiping
// the defaults. Instead the file is decoded into a FRESH message of the same
// type and then overlaid onto msg by overlayNamed: every field the file NAMES
// takes the file's value outright, and every field it does not name keeps its
// default. See overlayNamed for why that is not proto.Merge.
//
// shared are the other configs the process loads from the same file (see
// sharedFileMessages): their top-level keys are skipped here, because their
// own loads decode them.
//
// Any failure — unreadable file, unknown extension, malformed JSON/YAML, or a
// document that does not match the config schema — is returned as a loud error.
// Callers only invoke this when a path was EXPLICITLY given, so an error here
// always aborts Load (no silent fallback).
func applyConfigFile(msg proto.Message, path, source string, shared []protoreflect.MessageDescriptor) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("config file %q (from %s) does not exist: %s", path, source, missingFileFix(source))
		}
		return fmt.Errorf("config file %q (from %s): %w", path, source, err)
	}

	jsonBytes, err := toJSON(path, raw)
	if err != nil {
		return fmt.Errorf("config file %q (from %s): %w", path, source, err)
	}
	own, err := ownDocument(jsonBytes, msg.ProtoReflect().Descriptor(), shared)
	if err != nil {
		return fmt.Errorf("config file %q (from %s): %w", path, source, err)
	}

	// Decode into a fresh message of the same type (protojson resets it),
	// then overlay the fields the file names onto msg. DiscardUnknown=false
	// makes a stray/typo'd key below the top level a loud error rather than a
	// silent drop (ownDocument already refused an unknown top-level key).
	fileMsg := msg.ProtoReflect().New().Interface()
	opts := protojson.UnmarshalOptions{DiscardUnknown: false}
	if err := opts.Unmarshal(own, fileMsg); err != nil {
		return fmt.Errorf("config file %q (from %s): invalid config document: %w", path, source, err)
	}
	return overlayNamed(msg.ProtoReflect(), fileMsg.ProtoReflect(), own)
}

// overlayNamed sets on dst every field the JSON object doc names, to its
// decoded value in src, and leaves every other field of dst — its default —
// alone. A nested config block the document names as an object is overlaid
// the same way, one level down, so a file that sets one leaf of a block keeps
// the block's other defaults. A key whose value is null names nothing.
//
// It replaces proto.Merge, which this layer used to call, for two reasons
// that are each a silent wrong answer:
//
//   - Merge APPENDS a repeated field and unions a map, so a file's
//     `allowed_registries: [quay.io]` over the default [docker.io, ghcr.io]
//     loaded all three. Every layer REPLACES a whole list (parseList); the
//     file now does too, and `[]` clears a defaulted list.
//   - Merge copies only POPULATED fields, so a value equal to proto3's zero —
//     `port: 0`, `enabled: false`, `""`, `[]` — was dropped and the default
//     kept, and it recursed into every message, so "0.5s" over a 30s
//     Duration default became 30.5s. A field the file names now takes the
//     file's value whatever it is, and a message that is not a config block
//     (a Duration) is replaced whole.
//
// src is a fresh message decoded from doc and discarded after this, so its
// values are handed to dst without copying.
func overlayNamed(dst, src protoreflect.Message, doc []byte) error {
	var named map[string]json.RawMessage
	if err := json.Unmarshal(doc, &named); err != nil {
		return fmt.Errorf("invalid config document: %w", err)
	}
	fields := dst.Descriptor().Fields()
	for key, raw := range named {
		fd := fields.ByJSONName(key)
		if fd == nil {
			fd = fields.ByName(protoreflect.Name(key))
		}
		if fd == nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue // protojson already refused an unknown key; null sets nothing
		}
		if isConfigBlock(fd) && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
			if err := overlayNamed(dst.Mutable(fd).Message(), src.Get(fd).Message(), raw); err != nil {
				return err
			}
			continue
		}
		if src.Has(fd) {
			dst.Set(fd, src.Get(fd))
		} else {
			dst.Clear(fd) // named with its zero value: [] / {} / 0 / false / ""
		}
	}
	return nil
}

// ownDocument returns the part of a config document that belongs to md: the
// top-level keys md declares, as a JSON object protojson can decode. A key one
// of the shared configs declares is left for that config's own load. A key
// none of them declares is an error naming every config the command loads, and
// so is a key md AND a shared config both declare: a flat file cannot say
// which of the two it sets. A duplicate key is an error, as protojson makes it.
func ownDocument(doc []byte, md protoreflect.MessageDescriptor, shared []protoreflect.MessageDescriptor) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid config document: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("invalid config document: the top level must be a mapping of config keys, got %s", jsonKind(tok))
	}
	own := map[string]json.RawMessage{}
	seen := map[string]bool{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid config document: %w", err)
		}
		key, _ := kt.(string) // a JSON object key is always a string
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, fmt.Errorf("invalid config document: key %q: %w", key, err)
		}
		if seen[key] {
			return nil, fmt.Errorf("invalid config document: duplicate key %q", key)
		}
		seen[key] = true

		var others []string
		for _, s := range shared {
			if declaresKey(s, key) {
				others = append(others, string(s.Name()))
			}
		}
		switch mine := declaresKey(md, key); {
		case mine && len(others) > 0:
			return nil, fmt.Errorf("key %q is declared by both %s and %s, and this command reads both from one file, "+
				"which cannot say which it sets: rename one of the two fields",
				key, md.Name(), strings.Join(others, " and "))
		case mine:
			own[key] = val
		case len(others) > 0:
			// Another config's key: its own load decodes it.
		default:
			return nil, unknownKeyError(key, md, shared)
		}
	}
	if _, err := dec.Token(); err != nil { // the closing '}'
		return nil, fmt.Errorf("invalid config document: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("invalid config document: unexpected content after the top-level mapping")
	}
	return json.Marshal(own)
}

// declaresKey reports whether md has a field a config file can name with key:
// its JSON name or its proto name, the two spellings protojson accepts.
func declaresKey(md protoreflect.MessageDescriptor, key string) bool {
	fields := md.Fields()
	return fields.ByJSONName(key) != nil || fields.ByName(protoreflect.Name(key)) != nil
}

// unknownKeyError names a top-level key no loaded config declares, and every
// config it was looked up in — with several, which configs the command loads
// is exactly what the reader needs to know.
func unknownKeyError(key string, md protoreflect.MessageDescriptor, shared []protoreflect.MessageDescriptor) error {
	if len(shared) == 0 {
		return fmt.Errorf("unknown key %q: %s has no such field", key, md.Name())
	}
	names := []string{string(md.Name())}
	for _, s := range shared {
		names = append(names, string(s.Name()))
	}
	sort.Strings(names)
	return fmt.Errorf("unknown key %q: no config this command loads has such a field (%s)", key, strings.Join(names, ", "))
}

// jsonKind names what a JSON document's first token is, for the error that
// refuses a document whose top level is not a mapping.
func jsonKind(tok json.Token) string {
	switch v := tok.(type) {
	case json.Delim:
		if v == '[' {
			return "a list"
		}
	case nil:
		return "an empty document"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case float64, json.Number:
		return "a number"
	}
	return fmt.Sprintf("%v", tok)
}

// missingFileFix says how to recover from a missing config file, for the
// source the path came from. A path from the environment is the case worth
// spelling out: the operator may not know the variable is set at all.
func missingFileFix(source string) string {
	if env, ok := strings.CutPrefix(source, "env "); ok {
		return fmt.Sprintf("point %s at a readable file, or unset it", env)
	}
	return fmt.Sprintf("pass --%s a readable file, or omit it", ConfigFlag)
}

// toJSON returns protojson-ready JSON bytes for a config file, dispatching on
// the file extension: .json passes through; .yaml/.yml are converted via
// sigs.k8s.io/yaml (which round-trips through the YAML and JSON codecs). An
// unrecognized extension is a loud error — the format must be explicit.
func toJSON(path string, raw []byte) ([]byte, error) {
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".json":
		return raw, nil
	case ".yaml", ".yml":
		j, err := sigsyaml.YAMLToJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("converting YAML to JSON: %w", err)
		}
		return j, nil
	default:
		return nil, fmt.Errorf("unsupported config file extension %q (want .json, .yaml, or .yml)", ext)
	}
}
