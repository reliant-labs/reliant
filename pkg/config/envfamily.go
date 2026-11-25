package config

// Env families: a map<string, scalar> field bound to every variable named
// <env_prefix><KEY><env_suffix> ((forge.v1.config).env_prefix / env_suffix).
//
// A family is for keys the process cannot name at build time — an
// integration catalog's client ids, operator-invented feature flags. Each
// matching variable is one entry, map[KEY] = value parsed as the map's value
// kind, overlaying the config file's map key by key.
//
// The prefix is owned. A variable under a family's prefix that no family and
// no env_var of the process claims fails the load: in an open-ended family a
// misspelled suffix is otherwise an entry that is silently never there.
// "The process" is every config the command path records (a root config and
// a command config share one environment), as for the config file.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// isEnvFamily reports whether opt binds its field to an env family.
func isEnvFamily(opt *forgepb.ConfigFieldOptions) bool {
	return opt != nil && opt.GetEnvPrefix() != ""
}

// familyKey returns the KEY a variable name carries for a family, and whether
// the name belongs to the family at all (prefix and suffix match, KEY not
// empty).
func familyKey(opt *forgepb.ConfigFieldOptions, name string) (string, bool) {
	prefix, suffix := opt.GetEnvPrefix(), opt.GetEnvSuffix()
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) || len(name) <= len(prefix)+len(suffix) {
		return "", false
	}
	return name[len(prefix) : len(name)-len(suffix)], true
}

// familyPattern renders a family's variable names for messages:
// "APP_OAUTH_<KEY>_CLIENT_ID".
func familyPattern(opt *forgepb.ConfigFieldOptions) string {
	return opt.GetEnvPrefix() + "<KEY>" + opt.GetEnvSuffix()
}

// overlayFamily applies a family's env entries onto m's map field fd.
func overlayFamily(m protoreflect.Message, fd protoreflect.FieldDescriptor, opt *forgepb.ConfigFieldOptions, env environ) error {
	if !fd.IsMap() || fd.MapKey().Kind() != protoreflect.StringKind {
		return fmt.Errorf("config field %s: env_prefix needs a map<string, V> field", fd.Name())
	}
	names := env.environ()
	for _, kv := range names {
		name, value, _ := strings.Cut(kv, "=")
		key, ok := familyKey(opt, name)
		if !ok || value == "" {
			continue // not this family's, or "" (unset, as for every env var)
		}
		v, err := parseElement(fd.MapValue(), value)
		if err != nil {
			return fmt.Errorf("invalid value %q for config field %s[%q] (from env %s): %w", value, fd.Name(), key, name, err)
		}
		m.Mutable(fd).Map().Set(protoreflect.ValueOfString(key).MapKey(), v)
	}
	return nil
}

// checkFamilyPrefixes fails when a variable under one of msg's family
// prefixes is claimed by no family and no env var of the configs the
// process loads (msg plus those recorded on cmd's path).
func checkFamilyPrefixes(cmd *cobra.Command, msg proto.Message, o *loadOptions) error {
	own := familiesOf(msg.ProtoReflect().Descriptor())
	if len(own) == 0 {
		return nil
	}
	var families []*forgepb.ConfigFieldOptions
	plain := map[string]bool{}
	descs := []protoreflect.MessageDescriptor{msg.ProtoReflect().Descriptor()}
	descs = append(descs, o.sharedFileMessages(cmd, msg)...)
	for _, md := range descs {
		families = append(families, familiesOf(md)...)
		collectEnvNames(md, plain, map[protoreflect.FullName]bool{})
	}
	for _, kv := range o.env.environ() {
		name, value, _ := strings.Cut(kv, "=")
		if value == "" || plain[name] {
			continue
		}
		var under *forgepb.ConfigFieldOptions
		for _, f := range own {
			if strings.HasPrefix(name, f.GetEnvPrefix()) {
				under = f
				break
			}
		}
		if under == nil {
			continue
		}
		claimed := false
		for _, f := range families {
			if _, ok := familyKey(f, name); ok {
				claimed = true
				break
			}
		}
		if claimed {
			continue
		}
		patterns := make([]string, 0, len(families))
		for _, f := range families {
			if strings.HasPrefix(f.GetEnvPrefix(), under.GetEnvPrefix()) || strings.HasPrefix(under.GetEnvPrefix(), f.GetEnvPrefix()) {
				patterns = append(patterns, familyPattern(f))
			}
		}
		sort.Strings(patterns)
		if err := o.report(fmt.Errorf("env %s is under the config family prefix %s, but matches none of its families (%s): a misspelled name is an entry that silently never loads. Rename it, or unset it",
			name, under.GetEnvPrefix(), strings.Join(patterns, ", "))); err != nil {
			return err
		}
	}
	return nil
}

// familiesOf returns every env family declared on desc or a block it
// composes.
func familiesOf(desc protoreflect.MessageDescriptor) []*forgepb.ConfigFieldOptions {
	var out []*forgepb.ConfigFieldOptions
	var walk func(protoreflect.MessageDescriptor, map[protoreflect.FullName]bool)
	walk = func(d protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) {
		if seen[d.FullName()] {
			return
		}
		seen[d.FullName()] = true
		fields := d.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			if isConfigBlock(fd) {
				walk(fd.Message(), seen)
				continue
			}
			if opt := fieldOptions(fd); isEnvFamily(opt) {
				out = append(out, opt)
			}
		}
	}
	walk(desc, map[protoreflect.FullName]bool{})
	return out
}

// collectEnvNames records every env_var and alias declared on desc or a
// block it composes.
func collectEnvNames(desc protoreflect.MessageDescriptor, into map[string]bool, seen map[protoreflect.FullName]bool) {
	if seen[desc.FullName()] {
		return
	}
	seen[desc.FullName()] = true
	fields := desc.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if isConfigBlock(fd) {
			collectEnvNames(fd.Message(), into, seen)
			continue
		}
		opt := fieldOptions(fd)
		if opt == nil {
			continue
		}
		for _, n := range envNames(opt) {
			into[n] = true
		}
	}
}

// familyValues reads a string-valued family's values, for allowed_values.
func familyValues(m protoreflect.Message, fd protoreflect.FieldDescriptor) []string {
	var out []string
	m.Get(fd).Map().Range(func(_ protoreflect.MapKey, v protoreflect.Value) bool {
		out = append(out, v.String())
		return true
	})
	sort.Strings(out)
	return out
}
