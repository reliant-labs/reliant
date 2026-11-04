package v1alpha1

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Validation is ALL-OR-NOTHING and runs BEFORE anything renders: every
// violation is collected and returned together (errors.Join), so an author
// fixes a spec in one pass rather than one error per deploy.
//
// These are the invariants that hold on EVERY destination. Hosted-only
// policy — the shape band, the registry allowlist, quota, the customer-prefix
// rule on SecretRef — is not here; see deploy.CheckShapeBand and the control
// plane.

var (
	envNameRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	dnsLabelRE  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	sha256RE    = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	hostnameRE  = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]*[a-z0-9])?\.)+[a-z]([-a-z0-9]*[a-z0-9])?$`)
	validDBKeys = map[DatabaseCredentialKey]bool{DatabaseKeyURI: true, DatabaseKeyHost: true, DatabaseKeyPort: true, DatabaseKeyDBName: true, DatabaseKeyUsername: true, DatabaseKeyPassword: true}
)

// ValidateImage enforces forge's image invariant: an explicit registry host and
// a pin (tag or digest).
//
// Explicit registry because forge does not build this image and must not
// resolve a bare name against a registry the author does not control.
// Pinned because an unpinned image makes a redeploy silently change what
// runs. `:latest` counts as a tag, as it did in forge's KCL check. Refusing
// it is a separate policy decision, and making it here would widen the
// merge beyond what either side enforced.
func ValidateImage(image string) error {
	if image == "" {
		return errors.New("image is required")
	}
	if strings.ContainsAny(image, " \t\n") {
		return fmt.Errorf("image %q contains whitespace", image)
	}
	host, _, found := strings.Cut(image, "/")
	if !found || !(strings.ContainsAny(host, ".:") || host == "localhost") {
		return fmt.Errorf("image %q must name its registry host explicitly (e.g. ghcr.io/acme/app:v1): forge neither builds this image nor prefixes a registry onto it", image)
	}
	last := image[strings.LastIndex(image, "/")+1:]
	if !strings.Contains(image, "@sha256:") && !strings.Contains(last, ":") {
		return fmt.Errorf("image %q must be pinned to a tag or digest (':v1.2.3' or '@sha256:…'): an unpinned image makes a redeploy silently change what runs", image)
	}
	return nil
}

// Validate checks one env var: a legal name and at most one channel.
func (e EnvVar) Validate() error {
	var errs []error
	if !envNameRE.MatchString(e.Name) {
		errs = append(errs, fmt.Errorf("env var name %q must match %s", e.Name, envNameRE))
	}
	set := 0
	if e.Value != "" {
		set++
	}
	if e.SecretRef != nil {
		set++
		if e.SecretRef.Name == "" || e.SecretRef.Key == "" {
			errs = append(errs, fmt.Errorf("env var %s: secretRef needs both name and key", e.Name))
		}
	}
	if e.ManagedSecret != nil {
		set++
		if !envNameRE.MatchString(e.ManagedSecret.Name) || len(e.ManagedSecret.Name) > 253 {
			errs = append(errs, fmt.Errorf("env var %s: managedSecret.name %q must be a bare logical name matching %s, at most 253 characters", e.Name, e.ManagedSecret.Name, envNameRE))
		}
	}
	if e.DatabaseRef != nil {
		set++
		if !dnsLabelRE.MatchString(e.DatabaseRef.Name) {
			errs = append(errs, fmt.Errorf("env var %s: databaseRef.name %q must be the ManagedDatabase's name (an RFC-1123 label)", e.Name, e.DatabaseRef.Name))
		}
		if !validDBKeys[e.DatabaseRef.EffectiveKey()] {
			errs = append(errs, fmt.Errorf("env var %s: databaseRef.key %q must be one of uri, host, port, dbname, username, password", e.Name, e.DatabaseRef.Key))
		}
	}
	if e.WorkloadURL != nil {
		set++
		if err := e.WorkloadURL.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("env var %s: %w", e.Name, err))
		}
	}
	if e.ConfigMapRef != nil {
		set++
		if e.ConfigMapRef.Name == "" || e.ConfigMapRef.Key == "" {
			errs = append(errs, fmt.Errorf("env var %s: configMapRef needs both name and key", e.Name))
		}
	}
	if e.FieldRef != nil {
		set++
		if e.FieldRef.FieldPath == "" {
			errs = append(errs, fmt.Errorf("env var %s: fieldRef needs a fieldPath", e.Name))
		}
	}
	if set > 1 {
		errs = append(errs, fmt.Errorf("env var %s sets more than one of value / secretRef / managedSecret / databaseRef / workloadURL / configMapRef / fieldRef: a spec that says two things has no correct reading", e.Name))
	}
	return errors.Join(errs...)
}

// Validate checks a workload URL reference names a workload: an RFC-1123
// label, which is what every tier's metadata.name is.
func (r WorkloadURLRef) Validate() error {
	if !dnsLabelRE.MatchString(r.Name) || len(r.Name) > 63 {
		return fmt.Errorf("workloadURL.name %q must be the target workload's name (an RFC-1123 label of at most 63 characters)", r.Name)
	}
	return nil
}

// runtimeConfigKeyRE is the set of keys a runtime config document may carry:
// JavaScript identifiers in the ASCII subset, so every key is reachable as
// window.__FORGE_CONFIG__.KEY and the document is never ambiguous about
// what a key means across the Go, KCL and TypeScript layers.
var runtimeConfigKeyRE = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// MaxRuntimeConfigEntries bounds the runtime config document. It is served
// to every browser on every page load, so it is configuration, not storage.
const MaxRuntimeConfigEntries = 128

// ValidateRuntimeConfig checks a StaticSite's runtime config: legal keys and
// exactly one of value / workloadURL per entry.
func ValidateRuntimeConfig(rc map[string]RuntimeConfigValue) error {
	var errs []error
	if len(rc) > MaxRuntimeConfigEntries {
		errs = append(errs, fmt.Errorf("runtimeConfig has %d entries; at most %d are allowed", len(rc), MaxRuntimeConfigEntries))
	}
	for _, k := range sortedKeys(rc) {
		v := rc[k]
		if !runtimeConfigKeyRE.MatchString(k) {
			errs = append(errs, fmt.Errorf("runtimeConfig key %q must be a JavaScript identifier (%s)", k, runtimeConfigKeyRE))
		}
		switch {
		case v.Value != nil && v.WorkloadURL != nil:
			errs = append(errs, fmt.Errorf("runtimeConfig.%s sets both value and workloadURL: exactly one is allowed", k))
		case v.Value == nil && v.WorkloadURL == nil:
			errs = append(errs, fmt.Errorf("runtimeConfig.%s sets neither value nor workloadURL: exactly one is required (value: \"\" for an empty string)", k))
		case v.Value != nil && len(*v.Value) > 32768:
			errs = append(errs, fmt.Errorf("runtimeConfig.%s value is longer than 32768 characters", k))
		case v.WorkloadURL != nil:
			if err := v.WorkloadURL.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("runtimeConfig.%s: %w", k, err))
			}
		}
	}
	return errors.Join(errs...)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Validate checks the request/limit pairs. Zero means "use the default" and
// is valid. Negative values and a limit below its request are refused:
// Kubernetes itself rejects the latter at admission, and control-plane
// silently RAISED the limit, which hid the authoring mistake.
func (r Resources) Validate() error {
	var errs []error
	for name, v := range map[string]int64{
		"cpuRequestMillicores": r.CPURequestMillicores, "cpuLimitMillicores": r.CPULimitMillicores,
		"memoryRequestBytes": r.MemoryRequestBytes, "memoryLimitBytes": r.MemoryLimitBytes,
	} {
		if v < 0 {
			errs = append(errs, fmt.Errorf("resources.%s must not be negative", name))
		}
	}
	d := r.WithDefaults()
	if d.CPULimitMillicores < d.CPURequestMillicores {
		errs = append(errs, fmt.Errorf("resources.cpuLimitMillicores (%d) must be at least the request (%d)", d.CPULimitMillicores, d.CPURequestMillicores))
	}
	if d.MemoryLimitBytes < d.MemoryRequestBytes {
		errs = append(errs, fmt.Errorf("resources.memoryLimitBytes (%d) must be at least the request (%d)", d.MemoryLimitBytes, d.MemoryRequestBytes))
	}
	return errors.Join(errs...)
}

func validateDomains(field string, domains []string) error {
	var errs []error
	seen := map[string]bool{}
	for _, d := range domains {
		switch {
		case strings.Contains(d, "*"):
			// Named separately from the generic hostname refusal because a
			// wildcard is a DELIBERATE request, not a typo: the platform
			// verifies ownership of each name it serves, and a wildcard
			// names an unbounded set nobody can prove ownership of.
			errs = append(errs, fmt.Errorf("%s: %q is a wildcard; declare each hostname you want served, because ownership is verified per name", field, d))
		case !hostnameRE.MatchString(d) || len(d) > 253:
			errs = append(errs, fmt.Errorf("%s: %q is not a lowercase DNS hostname", field, d))
		}
		if seen[d] {
			errs = append(errs, fmt.Errorf("%s: %q is listed twice", field, d))
		}
		seen[d] = true
	}
	return errors.Join(errs...)
}

// validateReleaseRepository checks a site's recorded release repository: a
// bare repository reference naming its registry host explicitly.
//
// A TAG OR DIGEST ON IT IS REFUSED rather than trimmed. The release is
// addressed as `<releaseRepository>@<liveDigest>`, so a repository that
// already carried a pin would compose into a reference with two of them —
// and silently trimming it would leave the spec claiming one release while
// the field named another. Empty is legal: a site has no release until its
// first deploy.
func validateReleaseRepository(repository string) error {
	if repository == "" {
		return nil
	}
	if strings.ContainsAny(repository, " \t\n") {
		return fmt.Errorf("releaseRepository %q contains whitespace", repository)
	}
	host, _, found := strings.Cut(repository, "/")
	if !found || !(strings.ContainsAny(host, ".:") || host == "localhost") {
		return fmt.Errorf("releaseRepository %q must name its registry host explicitly (e.g. ghcr.io/acme/web/static.v1): "+
			"it is the recorded address of the pushed release, and a host-less path is not pullable", repository)
	}
	if strings.Contains(repository, "@") {
		return fmt.Errorf("releaseRepository %q must not carry a digest: the release is addressed as "+
			"<releaseRepository>@<liveDigest>, so the digest belongs in liveDigest alone", repository)
	}
	if last := repository[strings.LastIndex(repository, "/")+1:]; strings.Contains(last, ":") {
		return fmt.Errorf("releaseRepository %q must not carry a tag: it is a repository, and the release it serves "+
			"is pinned by liveDigest", repository)
	}
	return nil
}

// validateAssetDir checks a StaticSite's assetDir: a clean directory path
// relative to the site's mount. A server joins it under basePath to decide
// what it may cache forever and serve from previous releases, so a leading
// "/", a trailing "/", an empty or dot segment, or a backslash is refused
// rather than normalized — normalizing would make two spellings of one
// directory, and ".." would name a directory outside the site.
func validateAssetDir(dir string) error {
	if dir == "" {
		return nil
	}
	if strings.ContainsAny(dir, "\\\x00") || strings.HasPrefix(dir, "/") || strings.HasSuffix(dir, "/") {
		return fmt.Errorf("assetDir %q must be a directory relative to the site's mount (e.g. \"assets\"), with no leading or trailing '/'", dir)
	}
	for _, seg := range strings.Split(dir, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("assetDir %q must not contain an empty, '.' or '..' segment", dir)
		}
	}
	return nil
}

// Validate checks a StaticSite spec.
func (s StaticSiteSpec) Validate() error {
	var errs []error
	if s.Bucket == "gs://" {
		errs = append(errs, errors.New("bucket 'gs://' has no bucket name after the scheme"))
	}
	if s.BasePath != "" && !strings.HasPrefix(s.BasePath, "/") {
		errs = append(errs, fmt.Errorf("basePath %q must start with '/'", s.BasePath))
	}
	if err := validateAssetDir(s.AssetDir); err != nil {
		errs = append(errs, err)
	}
	if err := validateReleaseRepository(s.ReleaseRepository); err != nil {
		errs = append(errs, err)
	}
	for field, d := range map[string]string{"liveDigest": s.LiveDigest, "previousDigest": s.PreviousDigest} {
		if d != "" && !sha256RE.MatchString(d) {
			errs = append(errs, fmt.Errorf("%s %q must be a sha256:<64 hex> digest, never a tag", field, d))
		}
	}
	for _, d := range s.RetainedDigests {
		if !sha256RE.MatchString(d) {
			errs = append(errs, fmt.Errorf("retainedDigests entry %q must be a sha256:<64 hex> digest", d))
		}
	}
	if s.KeepReleases != nil && *s.KeepReleases != 0 && *s.KeepReleases < MinKeepReleases {
		errs = append(errs, fmt.Errorf("keepReleases %d must be 0 (retain everything) or at least %d: fewer would delete the predecessor release pages loaded moments ago still fetch", *s.KeepReleases, MinKeepReleases))
	}
	for _, p := range s.Entrypoints {
		if !strings.HasPrefix(p, "/") {
			errs = append(errs, fmt.Errorf("entrypoint %q must be an absolute site path", p))
		}
	}
	if s.CDN != nil {
		if s.CDN.URLMap == "" {
			errs = append(errs, errors.New("cdn.urlMap is required: it names the existing URL map invalidations are sent to"))
		}
		switch s.CDN.Invalidate {
		case "", InvalidateEntrypoints, InvalidateNone, InvalidateAll:
		default:
			errs = append(errs, fmt.Errorf("cdn.invalidate %q must be entrypoints, none or all", s.CDN.Invalidate))
		}
		for _, p := range s.CDN.ExtraInvalidatePaths {
			if !strings.HasPrefix(p, "/") {
				errs = append(errs, fmt.Errorf("cdn.extraInvalidatePaths entry %q must be an absolute site path", p))
			}
		}
	}
	if len(s.Domains) > MaxDomains {
		errs = append(errs, fmt.Errorf("domains: %d declared; at most %d are allowed", len(s.Domains), MaxDomains))
	}
	if err := validateDomains("domains", s.Domains); err != nil {
		errs = append(errs, err)
	}
	if err := ValidateRuntimeConfig(s.RuntimeConfig); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Validate checks a ManagedDatabase spec. Out-of-range values are REFUSED, not
// clamped (see WithDefaults).
func (s ManagedDatabaseSpec) Validate() error {
	var errs []error
	d := s.WithDefaults()
	if d.Engine != EnginePostgres {
		errs = append(errs, fmt.Errorf("engine %q is not supported; the only engine is postgres", s.Engine))
	}
	if d.Instances < 1 || d.Instances > MaxDatabaseInstances {
		errs = append(errs, fmt.Errorf("instances %d must be 1-%d: a larger topology is a different destination, not a bigger number", s.Instances, MaxDatabaseInstances))
	}
	if d.StorageGiB < 1 || d.StorageGiB > MaxDatabaseStorageGiB {
		errs = append(errs, fmt.Errorf("storageGiB %d must be 1-%d", s.StorageGiB, MaxDatabaseStorageGiB))
	}
	if d.DeletionPolicy != DeletionPolicyRetain && d.DeletionPolicy != DeletionPolicyDelete {
		errs = append(errs, fmt.Errorf("deletionPolicy %q must be retain or delete", s.DeletionPolicy))
	}
	if d.Dashboards != nil {
		if err := d.Dashboards.Validate(); err != nil {
			errs = append(errs, err)
		}
	}
	if r := d.Restore; r != nil {
		if err := ValidateDatabaseName(r.SourceDatabase); err != nil {
			errs = append(errs, fmt.Errorf("restore.sourceDatabase: %w", err))
		}
		// A timestamp that does not parse would otherwise reach CNPG, which
		// accepts the Cluster and fails inside the recovery job — a restore
		// that appears to start and then crash-loops, at the moment someone
		// is least able to debug it.
		if r.PointInTime != "" {
			if _, err := time.Parse(time.RFC3339, r.PointInTime); err != nil {
				errs = append(errs, fmt.Errorf("restore.pointInTime %q must be an RFC3339 timestamp (2026-01-02T15:04:05Z)", r.PointInTime))
			}
		}
	}
	return errors.Join(errs...)
}

// ValidateDatabaseName checks a ManagedDatabase's metadata.name. That name
// becomes the Cluster name unchanged and, with hyphens folded to underscores,
// the Postgres database and role names. Postgres silently TRUNCATES an
// identifier longer than 63 bytes, so two long names could address the same
// database. 40 characters leaves room for the "_app" role suffix, and an
// over-long name is refused, never truncated.
func ValidateDatabaseName(name string) error {
	if !dnsLabelRE.MatchString(name) || len(name) > 40 {
		return fmt.Errorf("database name %q must be an RFC-1123 label of at most 40 characters", name)
	}
	return nil
}
