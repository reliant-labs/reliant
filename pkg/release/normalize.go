package release

// Normalization and hashing of ONE rendered Kubernetes document.
//
// This lives in pkg/release, beside the Shape it feeds, because three
// unrelated programs have to agree on the answer:
//
//   - forge's bundle writer, which seals an object's Hash into an artifact
//     that is kept forever;
//   - forge's shape projection, which is what Live displays and what the
//     deploy plan diffs against;
//   - the control plane's converger, which compares a live cluster object
//     against the Hash a bundle recorded.
//
// If any two of those hashed differently, the only thing the comparison could
// ever prove is that the two implementations differ. So there is one
// implementation, in the module both sides already import, and the agreement
// is structural rather than maintained.
//
// The functions take a DECODED document (`any`, as a YAML or JSON decoder
// produces) rather than bytes. A caller that handed over bytes would be
// asking this package to pick a parser, and the bundle writer, the projection
// and the converger decode from three different sources.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// ArtifactPlaceholder is what a release-bound image digest becomes in the
// document a ConfigHash is taken over: `forge.dev/artifact:<key>`.
const ArtifactPlaceholder = "forge.dev/artifact:"

// secretValueFields are the two places a Kubernetes Secret holds a value.
var secretValueFields = []string{"data", "stringData"}

// RedactSecrets replaces every value under a `kind: Secret` document's `data`
// and `stringData` with the hash of that value, returning the document to
// hash or package.
//
// NO HASH INPUT AND NO PACKAGED BYTE MAY EVER CARRY A SECRET VALUE (F-13). A
// shape is stored forever and shown to every member of an org, and a bundle
// is cached and shared, so a leak in either is permanent. This is the one
// function that makes that true, which is why [ObjectHashes] calls it rather
// than trusting its caller to have called it.
//
// THE KEYS SURVIVE. "Which keys does this Secret carry" is a shape fact an
// operator needs; "what are they" is not. The hash is what keeps "a secret
// value changed" visible without the value — the signal a What-if diff shows.
//
// It is IDEMPOTENT: a value already carrying the marker is left alone, so
// redacting a document twice yields the same hash as redacting it once. Re-
// hashing a marker would make the safe entry point and the correct hash two
// different calls.
//
// Anything that is not a Secret is returned unchanged, and the input is never
// mutated — the caller still needs it.
func RedactSecrets(doc any) any {
	body, ok := doc.(map[string]any)
	if !ok || body["kind"] != "Secret" {
		return doc
	}
	out := make(map[string]any, len(body))
	for key, value := range body {
		out[key] = value
	}
	for _, field := range secretValueFields {
		values, ok := out[field].(map[string]any)
		if !ok {
			continue
		}
		redacted := make(map[string]any, len(values))
		for key, value := range values {
			redacted[key] = redactValue(value)
		}
		out[field] = redacted
	}
	return out
}

func redactValue(value any) string {
	s := fmt.Sprint(value)
	if strings.HasPrefix(s, RedactedSecretPrefix) {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return RedactedSecretPrefix + "sha256:" + hex.EncodeToString(sum[:])
}

// NormalizeImages rewrites every release-bound image DIGEST in a document to
// its artifact key, returning a copy. It is what makes a ConfigHash the
// identity of a deploy's SHAPE rather than of its images: two renders with
// equal ConfigHashes differ, at most, in which release they pin, so "promote
// a new release" and "the KCL moved" are told apart by one comparison instead
// of by re-rendering and diffing YAML.
//
// pins maps artifact key → pinned digest, as a render resolved them.
//
// ONLY THE DIGEST IS REPLACED, not the whole reference: the repository is
// part of the config (pushing an image somewhere else IS a change) and the
// digest is the only part a promotion moves.
//
// Every string in the document is searched, not just a container's `image`
// field, because an image reference is not confined to one: forge's own
// operators carry the image they launch as an env-var value, and a chart's
// values can put one anywhere. A normalizer that only looked at pod specs
// would leave a digest in the ConfigHash and make every promotion look like a
// config change.
//
// Artifacts are applied in sorted order, so two artifacts that happen to
// share a digest normalize the same way every time rather than by map
// iteration order.
func NormalizeImages(doc any, pins map[string]string) any {
	replacements := make([]string, 0, 2*len(pins))
	for _, artifact := range sortedPinKeys(pins) {
		if digest := pins[artifact]; digest != "" {
			replacements = append(replacements, digest, ArtifactPlaceholder+artifact)
		}
	}
	if len(replacements) == 0 {
		return doc
	}
	replacer := strings.NewReplacer(replacements...)
	return rewriteStrings(doc, replacer.Replace)
}

// ObjectHashes is the per-object pair a [ShapeObject] carries, and the only
// entry point a caller should need.
//
//   - hash is the document's digest after secret redaction: what drift
//     detection compares a live object against.
//   - configHash is the digest of the same document with release-bound image
//     references normalized away: equal configHash with a different hash
//     means only images changed.
//
// It redacts FIRST and unconditionally. A two-call API ("redact, then hash")
// would make the safe thing the thing a caller has to remember, and the cost
// of forgetting is a permanent leak.
func ObjectHashes(doc any, pins map[string]string) (hash, configHash string, err error) {
	redacted := RedactSecrets(doc)
	hash, err = HashDocument(redacted)
	if err != nil {
		return "", "", err
	}
	configHash, err = HashDocument(NormalizeImages(redacted, pins))
	if err != nil {
		return "", "", err
	}
	return hash, configHash, nil
}

// rewriteStrings rewrites every string in a decoded document, returning a
// copy. A copy, because the caller still needs the original — to hash it, and
// to package the bytes a deploy applies. Mutating in place would ship a
// normalized placeholder where a real image reference belongs.
func rewriteStrings(v any, rewrite func(string) string) any {
	switch t := v.(type) {
	case string:
		return rewrite(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = rewriteStrings(val, rewrite)
		}
		return out
	case map[any]any:
		out := make(map[any]any, len(t))
		for k, val := range t {
			out[k] = rewriteStrings(val, rewrite)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = rewriteStrings(val, rewrite)
		}
		return out
	default:
		return v
	}
}

// ImagesIn reports which release artifacts' pinned digests appear in a
// rendered document, as artifact key → digest. It is what populates
// [ShapeObject.Images].
//
// Searched as TEXT over the whole document, for the reason [NormalizeImages]
// is: an image reference is not confined to a container's `image` field.
// forge's own operators carry the image they launch as an env-var value
// (control-plane's workspace-controller pins the workspace base image in
// DAEMON_IMAGE), and a chart's values can put one anywhere. A reader that
// only looked at pod specs would report such an object as carrying no image
// while a deploy very much changes which bytes it runs.
func ImagesIn(renderedDoc string, pins map[string]string) map[string]string {
	var found map[string]string
	for _, artifact := range sortedPinKeys(pins) {
		digest := pins[artifact]
		if digest == "" || !strings.Contains(renderedDoc, digest) {
			continue
		}
		if found == nil {
			found = map[string]string{}
		}
		found[artifact] = digest
	}
	return found
}

func sortedPinKeys(pins map[string]string) []string {
	out := make([]string, 0, len(pins))
	for k := range pins {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
