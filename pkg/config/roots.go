package config

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// roots.go — each root of a project's composition graph as data, with the
// validation its process runs at boot.
//
// A project's generated pkg/config/roots_gen.go returns one Root per root
// (Roots, RootNamed, ValidateRootEnv): the command that runs it, every
// environment variable it reaches and the configs its process loads. It is
// the same reach deploy/kcl/config_gen.k's ROOTS projects, as Go another
// module can import: the repo that keeps a deployment's VALUES checks them
// against the SCHEMA of the binary it deploys, per root, in a unit test —
//
//	for _, r := range appconfig.Roots() {
//		for _, problem := range r.Validate(renderedEnvOf(r.Name)) {
//			t.Error(problem)
//		}
//	}
//
// — and fails on a missing required variable, a value the process would
// refuse (a bare-number duration, a value outside allowed_values, a
// misspelt env-family variable) and a variable the root never reads.

// RootVar is one environment variable a root reaches.
type RootVar struct {
	// Name is the variable; Aliases are the other names the process also
	// reads the field from (env_var_aliases), in order.
	Name    string
	Aliases []string
	// Config is the config message that declares the field, Path its path
	// from that message ("database_url", "stripe.secret_key").
	Config, Path string
	// Type is the field's type: a Go type ("string", "int32", "bool",
	// "[]string"), or "duration" for a google.protobuf.Duration.
	Type string
	// Required: the process refuses to start without it. Sensitive: a
	// credential, delivered as a secret.
	Required, Sensitive bool
}

// RootFamily is one env family a root reaches: a map field whose entries are
// every variable named Prefix<key>Suffix.
type RootFamily struct {
	Prefix, Suffix string
	Config, Path   string
}

// RootConfig is one config a root's process loads, with the paths of it the
// root reaches — what its command passes Load<Msg>Reach.
type RootConfig struct {
	// Message is the config message (any value of its type).
	Message proto.Message
	// Reach is the fields of it the root reaches, by path.
	Reach []string
}

// Root is one root of the composition graph as a process.
type Root struct {
	// Name is the root's name (//forge:root <name>); Args the command path
	// that runs it (`<bin> <args>`).
	Name string
	Args []string
	// Vars is every environment variable it reaches, across every config
	// its process loads, in declaration order; Families the env families.
	Vars     []RootVar
	Families []RootFamily
	// Configs is what its process loads, root config first.
	Configs []RootConfig
}

// Env is the non-sensitive variables the root reaches: ROOTS' env.
func (r Root) Env() []string { return r.names(func(v RootVar) bool { return !v.Sensitive }) }

// Secrets is the credentials the root reaches: ROOTS' secrets.
func (r Root) Secrets() []string { return r.names(func(v RootVar) bool { return v.Sensitive }) }

// RequiredSecrets is the credentials the root reaches and requires: ROOTS'
// required_secrets, which every workload running the root is handed.
func (r Root) RequiredSecrets() []string {
	return r.names(func(v RootVar) bool { return v.Sensitive && v.Required })
}

func (r Root) names(keep func(RootVar) bool) []string {
	out := []string{}
	for _, v := range r.Vars {
		if keep(v) {
			out = append(out, v.Name)
		}
	}
	return out
}

// Reaches reports whether the process reads the variable name: a reached
// field's variable or alias, or one under a reached env family's prefix. The
// prefix is owned, so a misspelt entry under it is the load's to report —
// as the family it fails to match — not an unread variable.
func (r Root) Reaches(name string) bool {
	for _, v := range r.Vars {
		if v.Name == name {
			return true
		}
		for _, a := range v.Aliases {
			if a == name {
				return true
			}
		}
	}
	for _, f := range r.Families {
		if f.Prefix != "" && strings.HasPrefix(name, f.Prefix) {
			return true
		}
	}
	return false
}

// Outside is the keys of env the root never reads, sorted: a variable — or a
// secret — handed to a process that does not need it.
func (r Root) Outside(env map[string]string) []string {
	var out []string
	for k := range env {
		if !r.Reaches(k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// UnreachedVarError is a variable in an env the root never reads.
type UnreachedVarError struct {
	Root, Name string
}

func (e *UnreachedVarError) Error() string {
	return fmt.Sprintf("root %q never reads %s: a process should be handed only the settings — and above all the secrets — it reaches", e.Root, e.Name)
}

// Validate runs what the root's process runs at boot over env instead of the
// process environment — each config it loads, with the reach its command
// demands, no flags — and returns every problem, not the first: a required
// variable missing, a value the process would refuse (unparseable, outside
// allowed_values, a misspelt env-family entry, a role invariant), and, as an
// *UnreachedVarError each, every variable of env the root never reads. nil:
// the process would start on env.
//
// env is the process's whole environment as the deployment renders it. A
// variable naming a config file is honoured as at boot, so the file is read
// from this machine.
func (r Root) Validate(env map[string]string) []error {
	var problems []error
	descs := make([]protoreflect.MessageDescriptor, len(r.Configs))
	for i, c := range r.Configs {
		descs[i] = c.Message.ProtoReflect().Descriptor()
	}
	for i, c := range r.Configs {
		var alongside []protoreflect.MessageDescriptor
		for j, d := range descs {
			if j != i {
				alongside = append(alongside, d)
			}
		}
		var collected []error
		msg := c.Message.ProtoReflect().New().Interface()
		err := LoadInto(nil, msg, RequiredWithin(c.Reach...), func(o *loadOptions) {
			o.env = environ{vars: env}
			o.alongside = alongside
			o.collect = &collected
		})
		if err != nil {
			collected = append(collected, err)
		}
		for _, p := range collected {
			problems = append(problems, fmt.Errorf("root %q, %s: %w", r.Name, descs[i].Name(), p))
		}
	}
	for _, k := range r.Outside(env) {
		problems = append(problems, &UnreachedVarError{Root: r.Name, Name: k})
	}
	return problems
}
