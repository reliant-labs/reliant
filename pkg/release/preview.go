package release

// Preview environments: a per-PR or per-branch instance of a template env,
// with an expiry. The instance is an ordinary env (its own deploy/kcl/<name>/,
// its own ledger and, hosted, its own control-plane environment), so every
// env verb works on it unchanged; what makes it a preview is the
// forge.Preview it declares, recorded in its shape, which is how a control
// plane — or `forge env gc` — learns when to delete it.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ShapePreview is Shape.Preview: the env is a preview instance of the env Of,
// to be deleted once ExpiresAt (RFC 3339, UTC) has passed. Source says what
// it previews ("pr-123", "branch feat/x"), for a human reading a list.
type ShapePreview struct {
	Of        string `json:"of"`
	ExpiresAt string `json:"expires_at"`
	Source    string `json:"source,omitempty"`
}

// Expired reports whether the preview's expiry is at or before now. An
// expiry that does not parse is not expired: a reaper must never delete on a
// value it cannot read.
func (p ShapePreview) Expired(now time.Time) bool {
	at, err := time.Parse(time.RFC3339, p.ExpiresAt)
	return err == nil && !at.After(now)
}

func (p ShapePreview) validate() error {
	if !previewEnvRE.MatchString(p.Of) {
		return fmt.Errorf("%w: shape preview.of %q is not an env name", ErrInvalid, p.Of)
	}
	if _, err := time.Parse(time.RFC3339, p.ExpiresAt); err != nil {
		return fmt.Errorf("%w: shape preview.expires_at %q is not an RFC 3339 time", ErrInvalid, p.ExpiresAt)
	}
	if len(p.Source) > 200 {
		return fmt.Errorf("%w: shape preview.source is longer than 200 bytes", ErrInvalid)
	}
	return nil
}

// previewEnvRE is an env name: a directory under deploy/kcl, starting with a
// letter (forge env new's rule).
var previewEnvRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// PreviewNameMax bounds a preview instance's name. A cluster preview's
// namespace is `<project>-<name>`, a 63-byte DNS label, so the name leaves
// room for a project name of up to 22 bytes; a longer one is cut and given a
// hash suffix so two long branches never collide.
const PreviewNameMax = 40

// DefaultPreviewTemplate names a preview instance: the template env, then
// "pr-<number>" or the branch.
const DefaultPreviewTemplate = "{env}-{id}"

// PreviewKey is what a preview instance previews: exactly one of a pull
// request number or a branch.
type PreviewKey struct {
	PR     int
	Branch string
}

// ID is the key's own part of a name: "pr-<n>" or the branch, sanitized.
func (k PreviewKey) ID() (string, error) {
	switch {
	case k.PR > 0 && k.Branch != "":
		return "", fmt.Errorf("name a pull request or a branch, not both")
	case k.PR > 0:
		return "pr-" + strconv.Itoa(k.PR), nil
	case strings.TrimSpace(k.Branch) != "":
		id := sanitizeLabel(k.Branch)
		if id == "" {
			return "", fmt.Errorf("branch %q has no letters or digits to name a preview by", k.Branch)
		}
		return id, nil
	}
	return "", fmt.Errorf("name the pull request (--pr <number>) or the branch (--branch <name>) the preview is for")
}

// Source is the key as a human reads it.
func (k PreviewKey) Source() string {
	if k.PR > 0 {
		return "pr-" + strconv.Itoa(k.PR)
	}
	return "branch " + k.Branch
}

// PreviewName is the instance name for key, from template (empty: the
// default) with {env} the template env and {id} the key's ID ({pr} and
// {branch} are accepted as spellings of {id}). The result is a valid env name
// of at most PreviewNameMax bytes: lowercase, letters, digits and hyphens,
// starting with a letter. It is deterministic, so the job that creates a
// preview and the job that deletes it agree on its name without sharing state.
func PreviewName(template, env string, key PreviewKey) (string, error) {
	if !previewEnvRE.MatchString(env) {
		return "", fmt.Errorf("%q is not an env name", env)
	}
	id, err := key.ID()
	if err != nil {
		return "", err
	}
	if template == "" {
		template = DefaultPreviewTemplate
	}
	if !strings.Contains(template, "{id}") && !strings.Contains(template, "{pr}") && !strings.Contains(template, "{branch}") {
		return "", fmt.Errorf("preview name template %q names no {id}: every preview would get the same name", template)
	}
	raw := strings.NewReplacer("{env}", env, "{id}", id, "{pr}", id, "{branch}", id).Replace(template)
	name := sanitizeLabel(raw)
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		name = "p-" + name
	}
	if len(name) > PreviewNameMax {
		sum := sha256.Sum256([]byte(raw))
		suffix := "-" + hex.EncodeToString(sum[:])[:6]
		name = strings.TrimRight(name[:PreviewNameMax-len(suffix)], "-") + suffix
	}
	if !previewEnvRE.MatchString(name) {
		return "", fmt.Errorf("preview name template %q produced %q, which is not an env name", template, name)
	}
	return name, nil
}

// sanitizeLabel lowercases s and turns every run of other bytes into one
// hyphen, trimmed at both ends.
func sanitizeLabel(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// MaxPreviewTTL bounds a preview's life: one that should outlive a month is
// an env, not a preview.
const MaxPreviewTTL = 30 * 24 * time.Hour

// ParsePreviewTTL reads a TTL: a Go duration ("72h") or whole days ("7d"),
// positive and at most MaxPreviewTTL.
func ParsePreviewTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("ttl %q: expected a duration (72h) or whole days (7d)", s)
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("ttl %q: expected a duration (72h) or whole days (7d)", s)
		}
	}
	if d <= 0 || d > MaxPreviewTTL {
		return 0, fmt.Errorf("ttl %q must be positive and at most %s", s, MaxPreviewTTL)
	}
	return d, nil
}
