package cli

// SECRET PRESENCE FOR A PLAN, read from the cluster the env is applied to.
//
// An `external` secret is one forge never writes: the env's KCL says a
// workload reads it, and something outside forge is trusted to have put it in
// the cluster. Its presence is not a question any ledger can answer — the
// control plane's secret store does not hold it, and a declaration only says
// it is needed. The cluster can answer it, and for an env forge applies itself
// forge holds that cluster's kubectl context. So the plan asks the cluster,
// and an `external` secret stops being permanently "presence not verifiable".
//
// KEY NAMES ONLY. The read is declared here, at the consumer, as an interface
// with no way to ask for a value (secretKeyGetter), so "a plan never reads a
// secret value" is a property of the type rather than of a careful caller. The
// live implementation, cluster.KubectlSecretGetter, prints keys through a
// go-template and never brings a value into this process.
//
// WHAT IS CHECKED, AND WHERE:
//
//   - every Secret a cluster workload reads (an env var's secretKeyRef), in
//     that workload's own context and namespace, carrying the keys it reads
//     (an optional ref asserts nothing);
//   - every declared prerequisite (forge.ExternalSecret), in its declared
//     namespace, carrying its declared keys — on the clusters whose workloads
//     read it, or, when none does, on ANY of the env's clusters. "Any", not
//     "every": an unattributed prerequisite legitimately lives in one cluster
//     of several, and requiring it in all of them would report MISSING for a
//     Secret that is exactly where it should be.
//
// A read that fails (no credential, an unreachable API server) makes that name
// UNVERIFIABLE — left out of the map — never missing. The plan must not turn
// "forge could not look" into a finding, and the deploy's own preflight, which
// refuses on a missing Secret before anything is recorded, still runs.

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

// secretKeyGetter is the one cluster read a presence check makes: which KEYS a
// Secret carries, and whether it exists. exists=false is a definite absence; an
// error is "could not look".
type secretKeyGetter interface {
	GetSecretKeys(ctx context.Context, kctx, namespace, name string) (keys map[string]struct{}, exists bool, err error)
}

// externalSecretProvider is the shape's provider discriminator for a secret
// forge never writes (shapeSecretProviderOf).
const externalSecretProvider = "external"

// planSecretReadBudget bounds every presence read of one plan together. A plan
// is computed before a deploy; a cluster that does not answer in this long
// makes its secrets unverifiable rather than holding the deploy.
const planSecretReadBudget = 60 * time.Second

// planSecretReadConcurrency bounds how many kubectl reads run at once.
const planSecretReadConcurrency = 8

// deployPlanSecretPresence is the presence map a forge-computed plan reads.
//
// A var so a plan test states what the cluster answers without rendering KCL
// or reaching kubectl; the presence logic itself is tested through
// clusterSecretPresence with a fake getter.
var deployPlanSecretPresence = func(ctx context.Context, projectDir, env string, candidate release.Shape) map[string]bool {
	if !declaresExternalSecret(candidate) {
		return nil
	}
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return nil
	}
	checks := externalSecretChecks(entities, candidate)
	if len(checks) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, planSecretReadBudget)
	defer cancel()
	return clusterSecretPresence(ctx, checks, cluster.KubectlSecretGetter{})
}

func declaresExternalSecret(shape release.Shape) bool {
	for _, s := range shape.Secrets {
		if s.Provider == externalSecretProvider {
			return true
		}
	}
	return false
}

// secretLocation is one place a Secret is read from.
type secretLocation struct {
	Context   string
	Namespace string
}

// secretCheck is one requirement on a declared secret: it must exist, carrying
// Keys, in at least one of AnyOf.
type secretCheck struct {
	Name  string
	AnyOf []secretLocation
	Keys  []string
}

// externalSecretChecks derives the checks for the candidate's `external`
// secrets from the env's render. See the file comment for what is checked and
// where.
func externalSecretChecks(e *KCLEntities, shape release.Shape) []secretCheck {
	if e == nil {
		return nil
	}
	external := map[string]bool{}
	for _, s := range shape.Secrets {
		if s.Provider == externalSecretProvider {
			external[s.Name] = true
		}
	}
	if len(external) == 0 {
		return nil
	}
	defaultCtx, defaultNS := "", ""
	if ct := e.ClusterTarget; ct != nil {
		defaultCtx, defaultNS = ct.Cluster, ct.Namespace
	}

	type readAt struct {
		name string
		loc  secretLocation
	}
	keysAt := map[readAt]map[string]bool{}
	var order []readAt
	readersIn := map[string][]string{} // secret name -> contexts whose workloads read it
	var envContexts []string
	for i := range e.Workloads {
		w := &e.Workloads[i]
		rt := w.Runtime.Cluster
		if w.Runtime.Type != RuntimeCluster || rt == nil {
			continue
		}
		loc := secretLocation{Context: firstNonEmpty(rt.Cluster, defaultCtx), Namespace: firstNonEmpty(rt.Namespace, defaultNS)}
		if loc.Context == "" {
			continue
		}
		envContexts = appendDistinct(envContexts, loc.Context)
		for _, ref := range secretRefsForService(w) {
			if !external[ref.SecretName] {
				continue
			}
			at := readAt{name: ref.SecretName, loc: loc}
			if keysAt[at] == nil {
				keysAt[at] = map[string]bool{}
				order = append(order, at)
			}
			if !ref.Optional && ref.SecretKey != "" {
				keysAt[at][ref.SecretKey] = true
			}
			readersIn[ref.SecretName] = appendDistinct(readersIn[ref.SecretName], loc.Context)
		}
	}
	if defaultCtx != "" {
		envContexts = appendDistinct(envContexts, defaultCtx)
	}

	checks := make([]secretCheck, 0, len(order)+len(e.RequiredSecrets))
	for _, at := range order {
		checks = append(checks, secretCheck{Name: at.name, AnyOf: []secretLocation{at.loc}, Keys: sortedSet(keysAt[at])})
	}
	for _, req := range e.RequiredSecrets {
		if !external[req.Name] {
			continue
		}
		ns := firstNonEmpty(req.Namespace, defaultNS)
		if readers := readersIn[req.Name]; len(readers) > 0 {
			for _, kctx := range readers {
				checks = append(checks, secretCheck{Name: req.Name, AnyOf: []secretLocation{{Context: kctx, Namespace: ns}}, Keys: req.Keys})
			}
			continue
		}
		anyOf := make([]secretLocation, 0, len(envContexts))
		for _, kctx := range envContexts {
			anyOf = append(anyOf, secretLocation{Context: kctx, Namespace: ns})
		}
		if len(anyOf) > 0 {
			checks = append(checks, secretCheck{Name: req.Name, AnyOf: anyOf, Keys: req.Keys})
		}
	}
	return checks
}

// clusterSecretPresence runs the checks and folds them into BuildPlan's
// presence map: false when any check is definitely unmet, true when every
// check is met, and ABSENT — unverifiable — when a read failed and nothing
// proved the secret missing.
//
// Each (context, namespace, name) is read once, however many checks name it.
func clusterSecretPresence(ctx context.Context, checks []secretCheck, getter secretKeyGetter) map[string]bool {
	type readAt struct {
		loc  secretLocation
		name string
	}
	type answer struct {
		keys   map[string]struct{}
		exists bool
		err    error
	}
	var reads []readAt
	seen := map[readAt]bool{}
	for _, c := range checks {
		for _, loc := range c.AnyOf {
			if at := (readAt{loc: loc, name: c.Name}); !seen[at] {
				seen[at] = true
				reads = append(reads, at)
			}
		}
	}
	answers := make(map[readAt]answer, len(reads))
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, planSecretReadConcurrency)
	)
	for _, at := range reads {
		wg.Add(1)
		go func(at readAt) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			keys, exists, err := getter.GetSecretKeys(ctx, at.loc.Context, at.loc.Namespace, at.name)
			mu.Lock()
			answers[at] = answer{keys: keys, exists: exists, err: err}
			mu.Unlock()
		}(at)
	}
	wg.Wait()

	missing, unread, named := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range checks {
		named[c.Name] = true
		met, failedRead := false, false
		for _, loc := range c.AnyOf {
			a := answers[readAt{loc: loc, name: c.Name}]
			switch {
			case a.err != nil:
				failedRead = true
			case a.exists && carriesKeys(a.keys, c.Keys):
				met = true
			}
		}
		switch {
		case met:
		case failedRead:
			unread[c.Name] = true
		default:
			missing[c.Name] = true
		}
	}
	presence := make(map[string]bool, len(named))
	for name := range named {
		switch {
		case missing[name]:
			presence[name] = false
		case unread[name]:
			// Unverifiable: left out, so BuildPlan says so rather than
			// guessing either way.
		default:
			presence[name] = true
		}
	}
	return presence
}

func carriesKeys(have map[string]struct{}, want []string) bool {
	for _, k := range want {
		if _, ok := have[k]; !ok {
			return false
		}
	}
	return true
}

func appendDistinct(list []string, s string) []string {
	for _, have := range list {
		if have == s {
			return list
		}
	}
	return append(list, s)
}

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
