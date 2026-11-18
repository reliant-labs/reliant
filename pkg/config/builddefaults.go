package config

// Build-time defaults: the layer between a field's proto default and the
// config file.
//
//	proto default  →  BUILD DEFAULT  →  config file  →  env  →  flag
//
// A proto default is what the SOURCE says. A build default is what one BUILD
// says: a release binary stamped with its environment's endpoints, while
// `go install` of the same source keeps the proto default. It is the only
// layer a binary carries rather than reads, so it is set the only way a Go
// binary can carry a value it was not compiled from: -ldflags -X, into one
// string variable the generated pkg/config declares (BuildDefaults).
//
// The stamp is ONE string in URL query form, keyed by each field's env_var:
//
//	-X 'example.com/app/pkg/config.BuildDefaults=APP_SERVER_URL=https://api.example.com&APP_REGION=eu'
//
// Query form because it is a standard, unambiguous encoding a shell can carry
// in one -X: `&` separates entries, and a value holding `&`, `=`, `+`, `%` or
// a space escapes it (url.QueryEscape). Keyed by env_var because that is the
// name a field already answers to everywhere outside the proto.
//
// It fails loud, at Load: a malformed stamp, a key that names no env_var of
// any config this binary declares, a key naming a SENSITIVE field (a secret
// compiled into a binary is published with it), or a value that does not
// parse for its field is an error naming the key. A stamp is a build defect,
// so the error says so rather than letting a release ship a silently ignored
// endpoint.

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var (
	buildDefaultsMu  sync.RWMutex
	buildDefaults    map[string]string
	buildDefaultsErr error
)

// SetBuildDefaults installs the build-time defaults layer from stamp (the
// URL-query-form string -ldflags -X wrote into the generated
// pkg/config.BuildDefaults) for every config message in msgs — the configs
// this binary declares, which bound the keys a stamp may name. The generated
// pkg/config calls it from init; a hand-assembled binary calls it before its
// first Load.
//
// It never fails itself: a stamp it refuses makes every later Load fail with
// the reason, so a command that loads no config (--help, version) still runs,
// and one that does cannot run on a stamp nobody checked. An empty stamp
// clears the layer.
func SetBuildDefaults(stamp string, msgs ...proto.Message) {
	values, err := parseBuildDefaults(stamp, msgs)
	buildDefaultsMu.Lock()
	defer buildDefaultsMu.Unlock()
	buildDefaults, buildDefaultsErr = values, err
}

// BuildDefaultsStampError is the reason the installed stamp was refused, or
// nil. Load returns it; it is exported for a binary that wants to report a
// broken stamp at startup, before any command runs.
func BuildDefaultsStampError() error {
	buildDefaultsMu.RLock()
	defer buildDefaultsMu.RUnlock()
	return buildDefaultsErr
}

func parseBuildDefaults(stamp string, msgs []proto.Message) (map[string]string, error) {
	stamp = strings.TrimSpace(stamp)
	if stamp == "" {
		return nil, nil
	}
	const where = "config: the build-time defaults stamped into this binary (-ldflags -X …/pkg/config.BuildDefaults)"
	q, err := url.ParseQuery(stamp)
	if err != nil {
		return nil, fmt.Errorf("%s are not in URL query form (KEY=value&KEY=value, values url.QueryEscape'd): %w", where, err)
	}
	known, sensitive := map[string]bool{}, map[string]bool{}
	for _, m := range msgs {
		collectEnvVars(m.ProtoReflect().Descriptor(), known, sensitive, map[protoreflect.FullName]bool{})
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(q))
	for _, k := range keys {
		switch {
		case sensitive[k]:
			return nil, fmt.Errorf("%s set %s, a `sensitive` field: a secret compiled into a binary is published with it. Deliver it as a Secret at runtime", where, k)
		case !known[k]:
			return nil, fmt.Errorf("%s set %s, which is the env_var of no config field this binary declares%s", where, k, nearEnvVar(k, known))
		case len(q[k]) > 1:
			return nil, fmt.Errorf("%s set %s %d times; set it once", where, k, len(q[k]))
		}
		out[k] = q[k][0]
	}
	return out, nil
}

// collectEnvVars records every primary env_var reachable from desc,
// descending into config blocks, and which of them are sensitive.
func collectEnvVars(desc protoreflect.MessageDescriptor, known, sensitive map[string]bool, seen map[protoreflect.FullName]bool) {
	if seen[desc.FullName()] {
		return
	}
	seen[desc.FullName()] = true
	fields := desc.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if isConfigBlock(fd) {
			collectEnvVars(fd.Message(), known, sensitive, seen)
			continue
		}
		opt := fieldOptions(fd)
		if opt == nil || opt.GetEnvVar() == "" {
			continue
		}
		known[opt.GetEnvVar()] = true
		if opt.GetSensitive() {
			sensitive[opt.GetEnvVar()] = true
		}
	}
}

// nearEnvVar suggests the declared env var a mistyped key was probably meant
// to be: the one equal to it ignoring case.
func nearEnvVar(k string, known map[string]bool) string {
	for n := range known {
		if strings.EqualFold(n, k) {
			return fmt.Sprintf(" (did you mean %s?)", n)
		}
	}
	return ""
}

// applyBuildDefaults overlays the installed build defaults onto m, recursing
// into config blocks: the layer above the proto defaults and below the
// config file.
func applyBuildDefaults(m protoreflect.Message) error {
	buildDefaultsMu.RLock()
	values, stampErr := buildDefaults, buildDefaultsErr
	buildDefaultsMu.RUnlock()
	if stampErr != nil {
		return stampErr
	}
	if len(values) == 0 {
		return nil
	}
	return overlayBuildDefaults(m, values)
}

func overlayBuildDefaults(m protoreflect.Message, values map[string]string) error {
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if isConfigBlock(fd) {
			if err := overlayBuildDefaults(m.Mutable(fd).Message(), values); err != nil {
				return err
			}
			continue
		}
		opt := fieldOptions(fd)
		if opt == nil || opt.GetSensitive() {
			continue
		}
		raw, ok := values[opt.GetEnvVar()]
		if !ok || opt.GetEnvVar() == "" {
			continue
		}
		val, err := parseValue(m, fd, raw)
		if err != nil {
			return fmt.Errorf("invalid build default %q for config field %s (%s, stamped with -ldflags -X …/pkg/config.BuildDefaults): %w",
				raw, fd.Name(), opt.GetEnvVar(), err)
		}
		m.Set(fd, val)
	}
	return nil
}
