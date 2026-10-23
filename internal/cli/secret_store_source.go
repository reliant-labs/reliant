package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/secretaccess"
)

// Store sources for `forge secret`.
//
// A forge.ExternalSecret with a forge.StoreSource is a Secret ESO syncs from a
// secret store: the hosted control plane's (HostedStore), the env's own
// OpenBao (OpenBaoStore) or Google Secret Manager (GCPSecretManagerStore).
// `forge secret set|list|import` write and list the STORE side of that, by
// store name, so the value an operator types is the value ESO later delivers.
//
// Three promises hold for every command in this file family and are pinned by
// secret_store_source_invariants_test.go:
//
//   - a value arrives on stdin, in --from-file, or from a cluster Secret or
//     store the operator already holds access to. It is never an argument;
//   - no command prints a value or any digest of one. Comparison uses digests
//     in memory (see sameValue) and reports match / no-match only;
//   - an error that came from a remote carries no copy of a value, even when
//     the remote echoes the request body (redactValue).

// storeRef names one store a store name lives in.
type storeRef struct {
	Kind    string // release.StoreHosted | StoreOpenBao | StoreGCPSecretManager
	Project string // GCPSecretManagerStore only
}

func (r storeRef) String() string {
	if r.Project != "" {
		return r.Kind + ":" + r.Project
	}
	return r.Kind
}

// storeDeclaration is one store name an ExternalSecret reads or generates.
type storeDeclaration struct {
	Name   string
	Store  storeRef
	Secret ExternalSecretEntity
	// Key is the ExternalSecret data key the name fills; empty for a name the
	// source only generates (or a generated companion).
	Key string
}

// storeDeclarations lists every store name the env's store-backed
// ExternalSecrets read or generate, sorted by (name, store).
func storeDeclarations(e *KCLEntities) []storeDeclaration {
	if e == nil {
		return nil
	}
	var out []storeDeclaration
	for _, es := range e.RequiredSecrets {
		src := es.Source
		if src == nil {
			continue
		}
		ref := storeRef{Kind: src.Store.Type, Project: src.Store.Project}
		for key, name := range src.Data {
			out = append(out, storeDeclaration{Name: name, Store: ref, Secret: es, Key: key})
		}
		for name, g := range src.Generate {
			out = append(out, storeDeclaration{Name: name, Store: ref, Secret: es})
			if g.Companion != "" {
				out = append(out, storeDeclaration{Name: g.Companion, Store: ref, Secret: es})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Store != out[j].Store {
			return out[i].Store.String() < out[j].Store.String()
		}
		return out[i].Secret.Namespace+"/"+out[i].Secret.Name < out[j].Secret.Namespace+"/"+out[j].Secret.Name
	})
	return out
}

func hasStoreSources(e *KCLEntities) bool {
	if e == nil {
		return false
	}
	for _, es := range e.RequiredSecrets {
		if es.Source != nil {
			return true
		}
	}
	return false
}

// storeOptions are the flags the store-source commands share.
type storeOptions struct {
	// Store pins which store kind a name is written to, for the rare name two
	// stores both declare.
	Store string
	// KubeContext overrides the context used to reach a self-run store (set,
	// list) or to read live Secrets (import, verify, adopt).
	KubeContext string
}

func (o storeOptions) validate() error {
	if o.Store == "" {
		return nil
	}
	for _, k := range release.StoreKinds {
		if o.Store == k {
			return nil
		}
	}
	return fmt.Errorf("--store %q is not a store kind (want one of %s)", o.Store, strings.Join(release.StoreKinds, ", "))
}

// selectStore finds the one store a name is declared in. found is false when
// no store-backed ExternalSecret mentions the name, so the caller can fall
// through to the env's secret_provider.
func selectStore(decls []storeDeclaration, name string, o storeOptions) (ref storeRef, found bool, err error) {
	seen := map[storeRef]bool{}
	var refs []storeRef
	for _, d := range decls {
		if d.Name != name || (o.Store != "" && d.Store.Kind != o.Store) || seen[d.Store] {
			continue
		}
		seen[d.Store] = true
		refs = append(refs, d.Store)
	}
	switch len(refs) {
	case 0:
		return storeRef{}, false, nil
	case 1:
		return refs[0], true, nil
	}
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.String()
	}
	return storeRef{}, true, fmt.Errorf(
		"%s is declared in more than one store (%s)\nfix: name the one to write with --store <kind>",
		name, strings.Join(names, ", "))
}

// storeSource is one store, reduced to what the commands do with it. None of
// its methods can return a value: a store the CLI writes to is write-only from
// here, and presence is names and version numbers.
type storeSource interface {
	// describe says where the store is, for messages.
	describe() string
	// put writes one value. createOnly makes it a create (check-and-set 0):
	// it fails with errStoreValueExists when the name already has a value.
	put(ctx context.Context, name, value string, createOnly bool) (version uint32, err error)
	// presence reports the current usable version of each of names that has
	// one. Names without a usable value are absent from the map.
	presence(ctx context.Context, names []string) (map[string]uint32, error)
}

// closeStoreSource ends whatever a store source holds open (the OpenBao
// tunnel). A source that holds nothing is not an io.Closer.
func closeStoreSource(src storeSource) {
	if c, ok := src.(io.Closer); ok {
		_ = c.Close()
	}
}

// errStoreValueExists is a create-only write that found a value.
var errStoreValueExists = errors.New("the store already holds a value for this name")

// openStoreSource is the seam the commands open a store through; tests drive
// the lower seams (newHostedSecretWriter, newKubeAccess, newGSMAPI) instead.
var openStoreSource = func(ctx context.Context, envName string, e *KCLEntities, ref storeRef, o storeOptions, writing bool) (storeSource, error) {
	switch ref.Kind {
	case release.StoreHosted:
		w, ep, err := newHostedSecretWriter(ctx, envName, e, writing)
		if err != nil {
			return nil, err
		}
		return hostedStoreSource{w: w, env: envName, endpoint: ep.URL}, nil
	case release.StoreOpenBao:
		return openBaoStoreSource(ctx, envName, e, o)
	case release.StoreGCPSecretManager:
		api, err := newGSMAPI(ctx)
		if err != nil {
			return nil, err
		}
		return gsmStoreSource{api: api, project: ref.Project, create: true}, nil
	}
	return nil, fmt.Errorf("unknown store kind %q", ref.Kind)
}

// hostedStoreSource writes through the hosted control plane's write-only API.
type hostedStoreSource struct {
	w        hostedSecretWriter
	env      string
	endpoint string
}

func (h hostedStoreSource) describe() string {
	return fmt.Sprintf("hosted env %q at %s", h.env, h.endpoint)
}

func (h hostedStoreSource) put(ctx context.Context, name, value string, createOnly bool) (uint32, error) {
	var (
		v   uint32
		err error
	)
	if createOnly {
		v, err = h.w.Create(ctx, name, value)
	} else {
		v, err = h.w.Set(ctx, name, value)
	}
	if err != nil {
		// SetSecret answers a lost check-and-set with Aborted.
		var apiErr *cloud.Error
		if createOnly && errors.As(err, &apiErr) && apiErr.HasCode("aborted") {
			return 0, errStoreValueExists
		}
		return 0, redactValue(err, value)
	}
	return v, nil
}

func (h hostedStoreSource) presence(ctx context.Context, names []string) (map[string]uint32, error) {
	stored, err := h.w.List(ctx)
	if err != nil {
		return nil, err
	}
	want := toSet(names)
	versions := map[string]uint32{}
	for _, s := range stored {
		if s.present() && want[s.Name] {
			versions[s.Name] = s.CurrentVersion
		}
	}
	return versions, nil
}

func toSet(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, s := range in {
		out[s] = true
	}
	return out
}

// runStoreSourceSet is `forge secret set` for a name a store-backed
// ExternalSecret declares. handled is false when no store declares the name,
// and the caller carries on with the env's secret_provider.
func runStoreSourceSet(ctx context.Context, envName, key string, e *KCLEntities, o storeOptions, fromFile string, stdin io.Reader, out io.Writer) (handled bool, err error) {
	if err := o.validate(); err != nil {
		return false, err
	}
	ref, found, err := selectStore(storeDeclarations(e), key, o)
	if err != nil || !found {
		return found, err
	}
	if err := validStoreName(ref, key); err != nil {
		return true, err
	}
	value, err := readSecretValue(fromFile, stdin)
	if err != nil {
		return true, err
	}
	if value == "" {
		return true, fmt.Errorf(
			"refusing to write an empty value for %s\n"+
				"fix: pipe the value in, e.g.  printf '%%s' \"$TOKEN\" | forge secret set --env %s %s",
			key, envName, key)
	}
	src, err := openStoreSource(ctx, envName, e, ref, o, true)
	if err != nil {
		return true, err
	}
	defer closeStoreSource(src)
	version, err := src.put(ctx, key, value, false)
	if err != nil {
		return true, redactValue(fmt.Errorf("set %s in the %s store: %w", key, ref.Kind, err), value)
	}
	// Never echo the value: only that it landed, where, and its version.
	fmt.Fprintf(out, "%s: %s (%d bytes) in %s\n", key, hostedSetVerb(version), len(value), src.describe())
	return true, nil
}

// gsmIDForm is a Secret Manager secret id as the schema's BootstrapSecret and
// GCPSecretManagerStore allow it.
var gsmIDForm = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,254}$`)

// validStoreName reports whether name is a legal name in the store.
func validStoreName(ref storeRef, name string) error {
	if ref.Kind == release.StoreGCPSecretManager {
		if !gsmIDForm.MatchString(name) {
			return fmt.Errorf("%q is not a Secret Manager secret id (letters, digits, '_' and '-', starting with a letter or digit)", name)
		}
		return nil
	}
	if !secretaccess.ValidName(name) {
		return fmt.Errorf("%q is not a valid secret name (want [A-Za-z_][A-Za-z0-9_]*)", name)
	}
	return nil
}

// storeSourceListStore is the presence facts of every store the env's
// ExternalSecrets read from, joined into the one shape `forge secret list`
// reports for every provider.
func storeSourceListStore(ctx context.Context, envName string, e *KCLEntities) (secretListStore, error) {
	byStore := map[storeRef][]string{}
	for _, d := range storeDeclarations(e) {
		byStore[d.Store] = append(byStore[d.Store], d.Name)
	}
	refs := make([]storeRef, 0, len(byStore))
	for r := range byStore {
		refs = append(refs, r)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })

	store := secretListStore{provider: "store", exists: true, verifiable: true,
		present: map[string]bool{}, versions: map[string]uint32{}}
	var where []string
	for _, ref := range refs {
		src, err := openStoreSource(ctx, envName, e, ref, storeOptions{}, false)
		if errors.Is(err, errHostedEnvNotFound) {
			// An env the control plane has not seen holds no secrets.
			where = append(where, ref.String())
			continue
		}
		if err != nil {
			return secretListStore{}, fmt.Errorf("%s store: %w", ref, err)
		}
		versions, err := src.presence(ctx, dedupeSorted(byStore[ref]))
		closeStoreSource(src)
		if err != nil {
			return secretListStore{}, fmt.Errorf("%s store: %w", ref, err)
		}
		where = append(where, src.describe())
		for name, v := range versions {
			store.present[name] = true
			store.versions[name] = v
		}
	}
	store.location = strings.Join(where, "; ")
	return store, nil
}

// mergeStoreSourceFacts adds the store-backed names to a report built from the
// env's secret_provider. A store name the provider's declarations already
// cover keeps its entry; a new one is listed with its store, and a name with
// no usable value is missing.
//
// The two sets of declarations are different vocabularies (env-var names the
// provider supplies; store names ESO fetches), so neither is folded into the
// other's gate: `forge secret ensure` still checks only the provider's.
func mergeStoreSourceFacts(report secretListReport, e *KCLEntities, st secretListStore) secretListReport {
	names, attribution := storeSourceDeclarations(e)
	have := map[string]int{}
	for i, s := range report.Secrets {
		have[s.Name] = i
	}
	for _, n := range names {
		entry := secretListEntry{Name: n, DeclaredBy: attribution[n], Presence: secretPresenceMissing}
		if st.present[n] {
			entry.Present, entry.Presence, entry.Version = true, secretPresenceSet, st.versions[n]
		}
		if i, ok := have[n]; ok {
			// Declared by the provider too: a value in either store counts.
			prev := report.Secrets[i]
			entry.DeclaredBy = append(prev.DeclaredBy, entry.DeclaredBy...)
			if prev.Present {
				entry.Present, entry.Presence, entry.Version = true, prev.Presence, prev.Version
			}
			report.Secrets[i] = entry
			continue
		}
		report.Secrets = append(report.Secrets, entry)
	}
	sort.SliceStable(report.Secrets, func(i, j int) bool { return report.Secrets[i].Name < report.Secrets[j].Name })

	report.Missing = report.Missing[:0]
	for _, s := range report.Secrets {
		if s.Presence == secretPresenceMissing {
			report.Missing = append(report.Missing, s.Name)
		}
	}
	report.MissingCount = len(report.Missing)
	if !report.Verifiable {
		report.Provider = st.provider
	}
	report.Verifiable = true
	if report.StorePath == "" {
		report.StorePath = st.location
	}
	report.StoreExists = report.StoreExists || st.exists
	report.OK = report.MissingCount == 0
	return report
}

// storeSourceDeclarations is the declared-name list and attribution
// buildSecretListReport joins presence against, for the "store" provider: the
// store names (not env-var names), attributed to the ExternalSecret that reads
// each.
func storeSourceDeclarations(e *KCLEntities) ([]string, map[string][]secretDeclaration) {
	attribution := map[string][]secretDeclaration{}
	var names []string
	for _, d := range storeDeclarations(e) {
		if _, ok := attribution[d.Name]; !ok {
			names = append(names, d.Name)
		}
		attribution[d.Name] = append(attribution[d.Name], secretDeclaration{
			Workload:   d.Secret.Namespace,
			Kind:       "store:" + d.Store.Kind,
			SecretName: d.Secret.Name,
			SecretKey:  d.Key,
		})
	}
	sort.Strings(names)
	return names, attribution
}

// redactedError is an error whose text has had every copy of a value removed.
// It unwraps to the original so callers can still branch on its type; only the
// text that gets printed is scrubbed.
type redactedError struct {
	msg string
	err error
}

func (r *redactedError) Error() string { return r.msg }
func (r *redactedError) Unwrap() error { return r.err }

// minRedactLen is the shortest value redactValue scrubs. A shorter one is
// found by chance inside ordinary words, so replacing it would wreck the
// message without hiding anything; a credential is longer than this, and the floor is the shortest value we can scrub without destroying the message.
const minRedactLen = 4

// redactValue returns err with value (raw and base64) replaced by [redacted]
// in its text. A remote that echoes a request back in its error body (a proxy,
// a misbehaving store) must not turn a failed write into a leaked secret.
func redactValue(err error, values ...string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	changed := false
	for _, v := range values {
		if len(v) < minRedactLen {
			continue
		}
		for _, form := range []string{v, base64.StdEncoding.EncodeToString([]byte(v)), base64.RawStdEncoding.EncodeToString([]byte(v))} {
			if strings.Contains(msg, form) {
				msg = strings.ReplaceAll(msg, form, "[redacted]")
				changed = true
			}
		}
	}
	if !changed {
		return err
	}
	return &redactedError{msg: msg, err: err}
}
