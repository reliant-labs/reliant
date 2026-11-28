// Copyright (c) 2025 Reliant Labs

// Package serveraction is Reliant's first-party server actions: workflow
// action nodes named `reliant/<ns>.<verb>@<major>` that run on the worker,
// with no machine and no daemon, against Reliant's own state (the pipeline's
// PR, queue, incident, release and sweep records; dev-docs/pr-records.md,
// decision D4, built generically).
//
// It holds three things, kept apart because they are needed in different
// processes:
//
//   - the Catalog of what exists: each action's name, description and param
//     schema. It is static (builtinSpecs), so validation resolves a `uses:`
//     the same way in the API server, the CLI and the worker, none of which
//     needs the implementations to do it;
//   - the Handler that implements an action, bound only on the worker, where
//     its dependencies (the database, GitHub) are;
//   - the Dispatcher, which checks the platform switch (pipeline.enabled),
//     the name, the handler and the params, establishes who the run is and
//     where its definition came from, calls the handler, and turns its answer
//     or failure into an Outcome that is safe to record in the run's history.
//
//forge:exclude-contract: the server-action catalog, the Handler seam and the dispatcher that routes a call to its bound handler; it does no I/O of its own (the run reads are its Resolver's)
package serveraction

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Prefix is the namespace every server action is named under. No integration
// in the catalog may use it as its id (TestNoIntegrationClaimsTheReliantNamespace),
// so a `uses:` starting with it is a server action and nothing else.
const Prefix = "reliant/"

// IsRef reports whether uses names a server action: it is in the reliant/
// namespace, whether or not the rest of it is well formed or registered.
// Everything that decides how an action node runs branches on it, so a
// malformed or unknown reliant/ name is refused as a server action, never
// looked up in the integration catalog.
func IsRef(uses string) bool {
	return strings.HasPrefix(strings.TrimSpace(uses), Prefix)
}

// Name is a parsed server-action name: reliant/<Namespace>.<Verb>@<Major>.
type Name struct {
	// Namespace is the family the action belongs to ("pr", "queue").
	Namespace string
	// Verb is what it does ("next", "verdict").
	Verb string
	// Major is its contract version. A breaking change to an action's params
	// or output is a new major registered beside the old one.
	Major int
}

// String is the canonical form a workflow's `uses:` names it by.
func (n Name) String() string {
	return Prefix + n.Namespace + "." + n.Verb + "@" + strconv.Itoa(n.Major)
}

// segment is one lowercase identifier: a namespace or a verb. Underscores
// separate words (needs_person, pr_record); dots are reserved for the
// namespace/verb split.
var segment = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ParseName parses a server-action reference. It accepts only the canonical
// form: the reliant/ prefix, a lowercase namespace and verb, and a positive
// major with no leading zero.
func ParseName(uses string) (Name, error) {
	raw := strings.TrimSpace(uses)
	if !strings.HasPrefix(raw, Prefix) {
		return Name{}, fmt.Errorf("server action %q must start with %q", raw, Prefix)
	}
	rest := strings.TrimPrefix(raw, Prefix)
	ident, major, ok := strings.Cut(rest, "@")
	if !ok {
		return Name{}, fmt.Errorf("server action %q has no major version: name it as reliant/<namespace>.<verb>@<major>", raw)
	}
	ns, verb, ok := strings.Cut(ident, ".")
	if !ok || !segment.MatchString(ns) || !segment.MatchString(verb) {
		return Name{}, fmt.Errorf("server action %q is not reliant/<namespace>.<verb>@<major> with a lowercase namespace and verb", raw)
	}
	n, err := strconv.Atoi(major)
	if err != nil || n < 1 || strconv.Itoa(n) != major {
		return Name{}, fmt.Errorf("server action %q has an invalid major version %q: use a positive integer (@1)", raw, major)
	}
	return Name{Namespace: ns, Verb: verb, Major: n}, nil
}
