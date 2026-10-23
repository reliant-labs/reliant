package doctor

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CheckAuthParity compares the issuer the BACKEND validates against with the
// issuer the FRONTEND signs in against.
//
// The failure it exists for is maximally confusing: sign-in SUCCEEDS (the
// browser talked to a real IdP and got a real token), and then every RPC
// answers 401 with a signature or issuer complaint. It reads like a broken
// token, so the search starts at the validator — which is correct, and is
// simply being shown a token from somewhere else.
//
// It is deliberately a WARN and never a FAIL: split-issuer setups are
// legitimate (a token-exchange gateway, an IdP migration accepting two
// issuers via auth.Config.TokenValidators), and doctor cannot tell those
// from a typo. It reports what it read and lets a human judge.
//
// # WHY THE BACKEND HALF READS THE RENDER
//
// This check used to answer "which issuer does the backend validate?" with a
// line scan of deploy/kcl/<env>/config.k for the literal string `jwt_issuer`
// — the name forge's own scaffold gives that field. On a project that spells
// it anything else the scan found nothing, and the check then reported, with
// full confidence, "the frontend names an issuer but the backend validates
// against none — every authenticated RPC will reject the token the browser
// obtains".
//
// It fired exactly that way on control-plane, whose backend validates
// SUPABASE_JWT_ISSUER (end-user auth) and ZITADEL_ISSUER (operator auth) and
// has no field called jwt_issuer at all. Every authenticated RPC on that
// stack demonstrably succeeds. The false alarm cost real debugging time and
// sent someone down a wrong path — the precise opposite of what a check that
// exists to shorten an auth investigation is for.
//
// So the backend half now reads the RENDERED manifests: the container env
// entries and host-service env vars an `env up` / `env deploy` actually
// hands the process. That is the authoritative artefact — it IS the variable
// the validator reads — and doctor already computes and memoises it for the
// deploy checks (see deployRenders / envRender in deploy.go), so this costs
// nothing extra in a `forge doctor` run. Issuer variables are recognised by
// SHAPE (a name ending in _ISSUER / _ISSUERS carrying an absolute http(s)
// URL), not by one scaffold-specific spelling, so no project can be reported
// issuer-less merely for naming its field something forge did not invent.
//
// The env's KCL source is still read, under one precedence rule applied to
// both halves: it is consulted ONLY for a side the render says nothing about
// — a frontend whose typed config never becomes a k8s object, or a deploy
// package that does not render at all yet. Where an artefact exists the
// artefact wins, and the source scan itself now matches issuer fields by the
// same shape and refuses any right-hand side that is not a string literal
// (see readKCLIssuerLiterals).
//
// WHY "BACKEND ONLY" IS NOT A WARNING
//
// The two one-sided cases are NOT symmetric, and treating them as if they
// were is the other half of the same over-claim:
//
//   - The backend's issuer is fully forge-visible. It is literally the env
//     var the validator reads, so "the render declares none" is a real fact
//     about the validator, and a frontend naming an issuer against it is a
//     finding worth a warning.
//   - The browser's is NOT. A frontend obtains tokens through a client SDK
//     built from a project URL (Supabase, Firebase), through a
//     backend-initiated OAuth redirect, or from a native shell — none of
//     which put an issuer-shaped variable anywhere forge can read. control-
//     plane's reliant-web is exactly this: it signs in through the Supabase
//     SDK from VITE_SUPABASE_URL, and declares no issuer variable at all.
//
// So "no frontend names an issuer" is an absence of evidence, not evidence
// of absence, and warning on it would re-create the same cry-wolf failure on
// every API-only and SDK-authenticated project. That case reports SKIP —
// with the backend issuer named in the evidence, so the fact is still on the
// report — while the frontend-only direction keeps its warning.
//
// A check that cannot obtain the facts reports neither a pass nor a
// confident failure: an environment that will not render, or an issuer bound
// through valueFrom/secretKeyRef, comes out StatusUnknown (see the
// StatusSkip vs StatusUnknown note in doctor.go).
func CheckAuthParity(_ context.Context, env *Environment) CheckResult {
	res := CheckResult{Name: "Auth issuer parity (backend ↔ frontend)"}

	// Env is "" for the project-health set (`forge doctor`), which spans
	// every environment — and an issuer typo in prod is the same incident as
	// one in dev, on the environment where it costs most. `forge env up`
	// passes a single env and gets a single-env answer.
	targets := deployRenders(env)
	if env.Env != "" {
		var scoped []envRender
		for _, r := range targets {
			if r.env == env.Env {
				scoped = append(scoped, r)
			}
		}
		targets = scoped
	}
	if len(targets) == 0 {
		// Nothing renders for this project (or for the named environment).
		// An EMPTY render is still a coherent input: it says the manifests
		// carry no issuer, which sends every side to the KCL-source fallback
		// in evalAuthParity. That keeps a project whose deploy package does
		// not yet render — a half-scaffolded one, most of all — from losing
		// the one warning that matters, without ever letting a source grep
		// override an artefact forge actually built.
		targets = []envRender{{env: orDev(env.Env)}}
	}

	verdicts := make([]parityVerdict, 0, len(targets))
	for _, r := range targets {
		v := evalAuthParity(env.ProjectDir, r)
		if len(targets) > 1 {
			// Name the environment only when several were checked, so the
			// single-env message `forge env up` embeds in its banner reads
			// exactly as it always has.
			v.message = r.env + ": " + v.message
			if v.evidence != "" {
				v.evidence = r.env + "\n" + v.evidence
			}
		}
		verdicts = append(verdicts, v)
	}

	worst := verdicts[0]
	var evidence []string
	for _, v := range verdicts {
		if paritySeverity[v.status] > paritySeverity[worst.status] {
			worst = v
		}
		if v.evidence != "" {
			evidence = append(evidence, v.evidence)
		}
	}
	res.Status = worst.status
	res.Message = worst.message
	res.Evidence = strings.Join(evidence, "\n\n")
	return res
}

// paritySeverity orders the per-environment verdicts so the roll-up reports
// the most serious one. Warn outranks Unknown deliberately: a definite
// finding in one environment must not be hidden behind another that merely
// could not be read. Pass outranks Skip so "dev agrees, prod declares
// nothing" reads as a pass rather than as nothing-to-compare.
var paritySeverity = map[Status]int{
	StatusSkip:    0,
	StatusPass:    1,
	StatusUnknown: 2,
	StatusWarn:    3,
}

// parityVerdict is one environment's answer, before the roll-up.
type parityVerdict struct {
	status   Status
	message  string
	evidence string
}

// evalAuthParity answers the parity question for ONE rendered environment.
func evalAuthParity(projectDir string, r envRender) parityVerdict {
	if r.err != nil {
		// The render is the only authoritative statement of what the backend
		// validates. Without it the honest answer is "undetermined" — NOT a
		// fallback grep of the source, which is what produced the false
		// positive this check was rewritten to kill.
		return parityVerdict{
			status:   StatusUnknown,
			message:  "could not render this environment — forge cannot read which issuer the backend validates, and will not guess",
			evidence: fmt.Sprintf("render error: %v", r.err),
		}
	}

	backend, frontend := issuerSides(r)
	srcBackend, srcFrontend := kclSourceIssuers(projectDir, r.env)

	// ONE precedence rule, applied to both halves: where the render
	// describes a side it is authoritative — it IS the variable the process
	// reads — and the KCL source is consulted only for a side the render is
	// silent about (a frontend whose typed config never becomes a k8s
	// object, or a deploy package that does not render yet).
	//
	// Applying it to only one half is not a shortcut, it is a wrong answer:
	// reading control-plane's backend from the render while reading its
	// frontend from source paired a live SUPABASE_JWT_ISSUER against a
	// NEXT_PUBLIC_OIDC_ISSUER sitting in a generated dict that nothing
	// projects, and reported a mismatch between two things that never meet.
	if !backend.declared() {
		backend.merge(srcBackend)
	}
	if !frontend.declared() {
		frontend.merge(srcFrontend)
	}

	switch {
	case len(backend.issuers) > 0 && len(frontend.issuers) > 0:
		return compareIssuers(backend, frontend)

	case len(frontend.issuers) > 0:
		// The frontend names an issuer and the backend resolved none.
		if len(backend.opaque) > 0 {
			// The backend DOES declare an issuer — its value is bound
			// outside the render (a Secret ref). Reporting "validates
			// against none" here would be the original over-claim wearing a
			// different hat.
			return parityVerdict{
				status: StatusUnknown,
				message: "the frontend names an issuer and the backend's issuer is bound outside the render " +
					"— forge cannot read it, so it cannot say whether they agree",
				evidence: "frontend: " + strings.Join(frontend.list(), ", ") +
					"\nbackend: " + strings.Join(backend.opaque, ", ") + " (valueFrom — not a literal in the manifest)",
			}
		}
		return parityVerdict{
			status: StatusWarn,
			message: "the frontend names an issuer but the backend validates against none " +
				"— every authenticated RPC will reject the token the browser obtains",
			evidence: "frontend: " + strings.Join(frontend.list(), ", ") + backendSlotNote(backend),
		}

	case len(backend.issuers) > 0:
		// Backend only. See the "WHY BACKEND ONLY IS NOT A WARNING" note on
		// CheckAuthParity: forge cannot see how a browser signs in when the
		// frontend declares no issuer variable, so this is nothing to
		// compare, not a defect. The issuer is still named in the evidence.
		return parityVerdict{
			status:  StatusSkip,
			message: fmt.Sprintf("the backend validates %d issuer(s) and no frontend declares one — nothing to compare", len(backend.issuers)),
			evidence: "backend: " + strings.Join(backend.list(), ", ") +
				"\nno frontend declares an issuer variable — a client SDK built from a project URL, a\n" +
				"backend-initiated OAuth redirect or a native shell all sign in without one, so forge\n" +
				"cannot read what the browser signs in against and does not guess.",
		}

	default:
		// Neither side resolved a value. Either nothing is configured (the
		// scaffold's default, and a correct one — no key material means
		// closed-and-bootable, not broken) or both sides bind their issuer
		// somewhere forge cannot read.
		if len(backend.opaque) > 0 || len(frontend.opaque) > 0 {
			return parityVerdict{
				status:  StatusUnknown,
				message: "both issuers are bound outside the render — forge cannot read either, so it cannot compare them",
				evidence: "backend: " + strings.Join(append(backend.opaque, backend.empty...), ", ") +
					"\nfrontend: " + strings.Join(append(frontend.opaque, frontend.empty...), ", "),
			}
		}
		return parityVerdict{
			status:  StatusSkip,
			message: "no OIDC issuer configured on either side — nothing to compare",
		}
	}
}

// compareIssuers is the case this check exists for: both halves name an
// issuer, and they can disagree.
//
// A backend may legitimately accept SEVERAL issuers (auth.Config carries a
// list of TokenValidators; control-plane validates a Supabase issuer for
// end users and a Zitadel issuer for operators), so a frontend issuer is
// wrong only when it matches NONE of them.
func compareIssuers(backend, frontend issuerSide) parityVerdict {
	var mismatched []string
	for _, f := range frontend.issuers {
		accepted := false
		for _, b := range backend.issuers {
			if sameIssuer(f.String(), b.url) {
				accepted = true
				break
			}
		}
		if !accepted {
			mismatched = append(mismatched, f.String())
		}
	}
	if len(mismatched) == 0 {
		return parityVerdict{
			status:   StatusPass,
			message:  "backend and frontend agree on the issuer",
			evidence: strings.Join(backend.list(), ", "),
		}
	}
	return parityVerdict{
		status:  StatusWarn,
		message: "backend and frontend name DIFFERENT issuers — sign-in will succeed and every RPC will still 401",
		evidence: "backend: " + strings.Join(backend.list(), ", ") +
			"\nfrontend: " + strings.Join(mismatched, ", ") +
			"\n\nNote the two-hostname case: a containerised IdP is reached at localhost:<port> by the\n" +
			"browser and at <service>:<port> from inside the network. That split is EXPECTED for the\n" +
			"JWKS URL (fetched in-network), but the ISSUER must match the browser-facing URL the token\n" +
			"is minted under. See: forge skill load auth/dev-loop",
	}
}

// backendSlotNote names an issuer variable the backend declares but leaves
// empty. An empty issuer is a filled-in-later slot, and saying so turns
// "the backend validates against none" from an accusation into a location.
func backendSlotNote(backend issuerSide) string {
	if len(backend.empty) == 0 {
		return ""
	}
	return "\nbackend: " + strings.Join(backend.empty, ", ") + " declared but empty"
}

// issuerRef is one issuer AND where it was read from. The label is what
// makes a warning actionable — "backend: SUPABASE_JWT_ISSUER=https://…"
// names the variable to go and edit, which a bare URL does not.
type issuerRef struct {
	label string
	url   string
}

func (r issuerRef) String() string { return r.label + ": " + r.url }

// issuerSide is everything one half of the identity wiring says about
// itself. The three buckets are three DIFFERENT answers and must not be
// collapsed:
//
//   - issuers: resolved values that can actually be compared.
//   - empty:   the variable is declared and renders to "" — a slot someone
//     has not filled in, which is a finding.
//   - opaque:  the variable is declared but its value is bound elsewhere
//     (valueFrom/secretKeyRef). Nothing about agreement can be concluded,
//     so this forces StatusUnknown rather than a confident verdict.
type issuerSide struct {
	issuers []issuerRef
	empty   []string
	opaque  []string
}

// declared reports whether this side said ANYTHING about an issuer — a
// value, an empty slot, or a secret ref. It is the gate on the KCL-source
// fallback: a side the render describes at all is described authoritatively,
// and a line scan must not get a second opinion in.
func (s issuerSide) declared() bool {
	return len(s.issuers)+len(s.empty)+len(s.opaque) > 0
}

func (s *issuerSide) merge(other issuerSide) {
	s.issuers = append(s.issuers, other.issuers...)
	s.empty = append(s.empty, other.empty...)
	s.opaque = append(s.opaque, other.opaque...)
	s.normalise()
}

// list renders the side's resolved issuers for evidence.
func (s issuerSide) list() []string {
	out := make([]string, 0, len(s.issuers))
	for _, r := range s.issuers {
		out = append(out, r.String())
	}
	return out
}

// normalise dedupes and orders every bucket, so a value declared on five
// replicas of the same Deployment is reported once and the evidence is
// stable between runs.
func (s *issuerSide) normalise() {
	s.issuers = dedupeRefs(s.issuers)
	s.empty = dedupeStrings(s.empty)
	s.opaque = dedupeStrings(s.opaque)
}

func dedupeRefs(in []issuerRef) []issuerRef {
	seen := make(map[string]bool, len(in))
	out := make([]issuerRef, 0, len(in))
	for _, r := range in {
		key := r.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// issuerSides splits every issuer-shaped variable the render declares into
// the browser half and the server half.
//
// The discriminator is the bundler prefix, not which workload carries the
// variable: NEXT_PUBLIC_/VITE_/EXPO_PUBLIC_ (and CRA's REACT_APP_) mean "this
// value is inlined into the browser bundle" by construction — it is the same
// dispatch internal/cli's frontendEnvPrefix uses to build those names — so a
// NEXT_PUBLIC_OIDC_ISSUER is the browser's issuer even when a project
// projects it onto its server workloads' env, which is exactly how
// control-plane's generated AppConfig projection emits it.
func issuerSides(r envRender) (backend, frontend issuerSide) {
	for _, v := range renderIssuerVars(r) {
		side := &backend
		if isBrowserVarName(v.name) {
			side = &frontend
		}
		side.absorb(v)
	}
	backend.normalise()
	frontend.normalise()
	return backend, frontend
}

// absorb files one rendered variable into the right bucket.
func (s *issuerSide) absorb(v renderVar) {
	if !v.bound {
		s.opaque = append(s.opaque, v.name)
		return
	}
	// A *_ISSUERS variable is a list (control-plane's MCP_OAUTH_ISSUERS is
	// comma-separated), and a single-issuer variable splits to itself.
	found := false
	for _, candidate := range strings.Split(v.value, ",") {
		candidate = strings.TrimSpace(candidate)
		if !isIssuerURL(candidate) {
			continue
		}
		s.issuers = append(s.issuers, issuerRef{label: v.name, url: candidate})
		found = true
	}
	if found {
		return
	}
	if strings.TrimSpace(v.value) == "" {
		s.empty = append(s.empty, v.name)
		return
	}
	// A non-empty value that is not an absolute http(s) URL is not an OIDC
	// issuer at all — OIDC requires the issuer to BE the https URL its
	// discovery document is served from. This is what keeps a cert-manager
	// `CERT_ISSUER=letsencrypt-dns01` out of an auth report; it is dropped
	// entirely rather than counted as an unfilled slot.
}

// renderVar is one env entry the render declares. bound distinguishes "has
// a literal value, possibly empty" from "points at a Secret" — see
// issuerSide.
type renderVar struct {
	name  string
	value string
	bound bool
}

// renderIssuerVars collects every issuer-shaped env var an environment's
// render declares, from BOTH shapes a workload's environment can take:
//
//   - container env on a rendered manifest (a cluster-deployed workload),
//     initContainers included — a migrate step carries the same env, and a
//     project may declare the issuer only there.
//   - the `output` contract's host services, whose env lands on the deploy
//     block rather than in a pod spec. A dev environment is normally
//     host-mode, so a manifest-only scan would find nothing at all in the
//     one environment a developer is looking at.
func renderIssuerVars(r envRender) []renderVar {
	var out []renderVar
	add := func(name, value string, bound bool) {
		if !isIssuerVarName(name) {
			return
		}
		out = append(out, renderVar{name: name, value: value, bound: bound})
	}

	for _, o := range r.objects {
		_, containers := containersOf(o)
		for _, c := range append(containers, initContainersOf(o)...) {
			rawEnv, _ := c["env"].([]any)
			for _, e := range rawEnv {
				entry, ok := e.(map[string]any)
				if !ok {
					continue
				}
				name, _ := entry["name"].(string)
				if raw, has := entry["value"]; has {
					value, _ := raw.(string)
					add(name, value, true)
					continue
				}
				add(name, "", false)
			}
		}
	}

	for _, s := range r.hostServices {
		for _, e := range append(append([]renderedEnv{}, s.EnvVars...), s.Deploy.EnvVars...) {
			add(e.Name, e.Value, true)
		}
	}
	return out
}

// browserVarPrefixes are the bundler prefixes that inline a value into the
// browser bundle. The first three are the ones forge itself emits
// (internal/cli frontendEnvPrefix: Next.js, Vite, Expo); REACT_APP_ is
// Create React App's, carried because a hand-written frontend still uses it.
var browserVarPrefixes = []string{"NEXT_PUBLIC_", "VITE_", "EXPO_PUBLIC_", "REACT_APP_"}

func isBrowserVarName(name string) bool {
	upper := strings.ToUpper(name)
	for _, p := range browserVarPrefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

// isIssuerVarName recognises an issuer-bearing variable by SHAPE.
//
// Matching the shape rather than one name is the whole point of the
// rewrite: JWT_ISSUER, OIDC_ISSUER, SUPABASE_JWT_ISSUER, ZITADEL_ISSUER and
// MCP_OAUTH_ISSUERS are all the same fact, and a check that only knew the
// forge scaffold's spelling reported a project with three of them as having
// none.
func isIssuerVarName(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	if upper == "" || isCertIssuerName(upper) {
		return false
	}
	return upper == "ISSUER" || upper == "ISSUERS" ||
		strings.HasSuffix(upper, "_ISSUER") || strings.HasSuffix(upper, "_ISSUERS")
}

// isCertIssuerName excludes the OTHER thing called an issuer in a k8s
// project: cert-manager's Issuer/ClusterIssuer, which names a certificate
// authority and has nothing to do with a token's `iss` claim. Its values
// are names ("letsencrypt-dns01"), not URLs, so isIssuerURL would drop them
// anyway — but an EMPTY one would otherwise be reported as an unfilled auth
// slot, which is a confusing thing to read in an auth report.
func isCertIssuerName(upper string) bool {
	for _, frag := range []string{"CERT", "TLS", "ACME", "CLUSTER_ISSUER"} {
		if strings.Contains(upper, frag) {
			return true
		}
	}
	return false
}

// isIssuerURL reports whether a value is usable as an OIDC issuer: an
// absolute http(s) URL with a host. OIDC defines the issuer as the URL its
// discovery document is served from, so anything else is a different kind of
// value that happens to sit in a similarly-named variable.
func isIssuerURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// orDev is the environment a source-only scan looks at when nothing named
// one. `forge doctor` runs unscoped and dev is where the halves are wired
// first, so it is the only defensible default.
func orDev(env string) string {
	if env == "" {
		return "dev"
	}
	return env
}

// kclSourceIssuers reads issuer declarations out of an environment's KCL
// SOURCE, split into the browser half and the server half exactly the way
// the render is split.
//
// It exists for two reasons, and for nothing else:
//
//   - The FRONTEND half is not in the manifests at all. A frontend's typed
//     config is projected into the `output` contract's frontend entries, and
//     a Firebase-hosted frontend never becomes a k8s object, so the objects
//     envRender parses cannot see the browser's issuer. Until envRender
//     carries frontend env vars this is the only place that half is visible.
//   - The BACKEND half falls back here only when the render says NOTHING
//     about it (see evalAuthParity), which in practice means a deploy
//     package that does not render yet. Where an artefact exists, the
//     artefact wins.
//
// Every .k file in the env directory is scanned, not just config.k: a
// project may split its declarations across files the env imports, and a
// check that reads one filename goes quiet on exactly the layouts most
// likely to have drifted.
func kclSourceIssuers(root, env string) (backend, frontend issuerSide) {
	dir := filepath.Join(root, "deploy", "kcl", orDev(env))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return backend, frontend
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".k") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		for _, lit := range readKCLIssuerLiterals(filepath.Join(dir, name)) {
			if !isIssuerURL(lit.value) {
				// A literal that is not a URL is not an OIDC issuer. Nothing
				// is recorded — not even an empty slot: a SOURCE scan cannot
				// tell an unset field from one the render fills in, and
				// claiming otherwise is how this check used to over-reach.
				continue
			}
			side := &backend
			if isBrowserVarName(lit.key) || strings.EqualFold(lit.key, "oidc_issuer") {
				// oidc_issuer is the browser's field by name in both the
				// generated frontend config schema and the AppConfig
				// projection that emits NEXT_PUBLIC_OIDC_ISSUER from it.
				side = &frontend
			}
			side.issuers = append(side.issuers, issuerRef{
				label: name + " (" + lit.key + ")",
				url:   lit.value,
			})
		}
	}
	backend.normalise()
	frontend.normalise()
	return backend, frontend
}

// kclLiteral is one `key = "value"` a line scan could read with confidence.
type kclLiteral struct {
	key   string
	value string
}

// readKCLIssuerLiterals returns every issuer-shaped key in a KCL file that
// is assigned a STRING LITERAL.
//
// Deliberately a line scan rather than a KCL evaluation — doctor reports on
// projects that may not render cleanly, and a source fallback that needs a
// working build is one that goes quiet exactly when it is needed — and
// deliberately limited to what a line scan can honestly answer:
//
//   - Both spellings count: `"KEY" = "value"` (a dict entry, what a
//     projection map emits) and `key = "value"` (a schema field).
//
//   - Keys are matched by SHAPE (isIssuerVarName), never against one
//     hard-coded scaffold field name. Looking only for `jwt_issuer` is what
//     reported control-plane — whose backend validates SUPABASE_JWT_ISSUER
//     and ZITADEL_ISSUER — as validating no issuer at all.
//
//   - A right-hand side that is not a quoted literal is REFUSED. A
//     reference, a lambda call or a conditional resolves only by evaluating
//     the package, and control-plane's dev config.k reads
//
//     oidc_issuer = idp.dev_frontend_identity["NEXT_PUBLIC_OIDC_ISSUER"]
//
//     which the old scan returned verbatim, as if that expression were a
//     URL, and then compared against a real issuer.
func readKCLIssuerLiterals(path string) []kclLiteral {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []kclLiteral
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		k = strings.Trim(strings.TrimSpace(k), `"'`)
		if !isIssuerVarName(k) {
			continue
		}
		v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), ","))
		if len(v) < 2 || (v[0] != '"' && v[0] != '\'') || v[len(v)-1] != v[0] {
			continue // not a literal — unresolvable from a line scan
		}
		if inner := v[1 : len(v)-1]; inner != "" {
			out = append(out, kclLiteral{key: k, value: inner})
		}
	}
	return out
}

// sameIssuer compares two issuer URLs the way an OIDC implementation does
// modulo the one difference that is never meaningful: a trailing slash. The
// frontend entry carries a "<label>: " prefix, which is stripped first.
func sameIssuer(frontendEntry, backend string) bool {
	_, issuer, found := strings.Cut(frontendEntry, ": ")
	if !found {
		issuer = frontendEntry
	}
	return strings.TrimRight(issuer, "/") == strings.TrimRight(backend, "/")
}
