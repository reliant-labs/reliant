package release

// The hashes a render is identified by. Two, at two scales.
//
// PER OBJECT, [ObjectHashes] gives Hash and ConfigHash, which a [ShapeObject]
// carries and drift detection compares a live object against.
//
// PER RENDER, [ConfigDigest] gives the whole env's config digest — doc §2.1's
// second digest, and the one the names make confusing, so: a bundle has
//
//   - `digest`, the OCI manifest digest of the whole artifact, pins included.
//     It is the bundle's identity and it changes on every release. The
//     transport computes it; nothing here does.
//   - `config_digest`, from this file: the render with every release-bound
//     image reference normalized to its artifact key. It is the SHAPE of the
//     deploy independent of which release it pins.
//
// That split is what makes "same config, new images" (a promotion) and "same
// images, new config" (a spec-change deploy) both one comparison rather than
// a 2 MB YAML diff.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// HashDocument is the object hash: sha256 over the document's CANONICAL JSON,
// returned as `sha256:<hex>`.
//
// Canonical JSON rather than the rendered YAML, because the hash has to mean
// "this object" and not "these bytes". encoding/json sorts an object's keys,
// so a renderer that reorders a map, reflows a list or changes its
// indentation produces the same hash — while any change to a VALUE changes
// it. Comparing YAML text would make every cosmetic render change look like
// drift, which is the signal this hash exists to carry.
//
// It does NOT redact. Callers hashing a rendered document want [ObjectHashes];
// this is exported for the one case that has already redacted and needs the
// digest of some other canonical value.
func HashDocument(doc any) (string, error) {
	canonical, err := json.Marshal(jsonable(doc))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ConfigDigest is the digest of one env's whole render with every
// release-bound image reference normalized to its artifact key (doc §2.1).
//
// It is computed from the per-object ConfigHashes a shape already holds,
// rather than over the manifest bytes, and that is the point: the objects are
// taken in their canonical (sorted) order, so a render that emits the same
// objects in a different sequence — or reflows their YAML — produces the same
// config digest. A digest over the packed layer would move on both, and would
// therefore report a cosmetic render change as a config change on every
// deploy.
//
// It covers the object identities as well as their hashes. Renaming an object
// while leaving its body alone IS a config change, and hashing only the
// per-object hashes would miss it.
func ConfigDigest(shape Shape) (string, error) {
	canonical := shape.Canonical()
	type entry struct {
		Key        ObjectKey `json:"key"`
		ConfigHash string    `json:"config_hash"`
		// Tree is part of the object's identity in a reconciler: moving
		// it is a change even when its bytes are not. Omitted when empty,
		// so a flat bundle's digest is what it always was.
		Tree string `json:"tree,omitempty"`
	}
	entries := make([]entry, 0, len(canonical.Objects))
	for _, o := range canonical.Objects {
		configHash := o.ConfigHash
		if configHash == "" {
			// An object whose ConfigHash was never computed binds no
			// release image, so its Hash already IS its config hash.
			// Substituting it keeps the digest total over the render
			// instead of silently ignoring the object.
			configHash = o.Hash
		}
		if configHash == "" {
			return "", fmt.Errorf("%w: shape object %s has no hash, so the render has no config digest", ErrInvalid, o.Key())
		}
		entries = append(entries, entry{Key: o.Key(), ConfigHash: configHash, Tree: o.Tree})
	}
	return HashDocument(entries)
}

// jsonable converts a decoded document into something encoding/json can
// marshal deterministically. yaml.v3 decodes a mapping into map[string]any
// when the target is `any`, but a mapping with a NON-STRING key decodes into
// map[any]any, which json refuses outright; such a key is stringified rather
// than failing the whole render's hash over one odd document.
func jsonable(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = jsonable(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = jsonable(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = jsonable(val)
		}
		return out
	default:
		return v
	}
}
