package bundle

// F-13: a bundle never contains a Secret value.
//
// This is a hard invariant, not a best effort, because of what a bundle IS: it
// is pushed to a registry, cached by digest on every machine that applies it,
// retained for the life of the deploy history, and readable by everyone who
// can pull from the repository. A value that lands in one cannot be taken
// back — the artifact is immutable by design, and deleting the blob does not
// reach the caches.
//
// The REAL protection is structural: parseStream (parse.go) is the only way
// into this package, and it redacts on the way through, so no unredacted
// document is ever available to be packaged. What this file adds is the PROOF
// — a scan of the final bytes, after serialization, after compression. It
// should be incapable of firing, and it is checked anyway, because "the
// redaction ran" and "the bytes are clean" are different claims and only the
// second one is the invariant.
//
// It scans the UNPACKED layer rather than the compressed bytes. A gzip stream
// does not contain its input's substrings, so a search over the compressed
// form would pass unconditionally — a guard that is present in the code and
// absent in effect, which is worse than no guard at all.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/release"
)

// ErrSecretValue is the refusal F-13 names. Separate from
// [release.ErrInvalid] so a caller can tell "this render is malformed" from
// "this build would have leaked a secret", which are a bug report and an
// incident respectively.
var ErrSecretValue = errors.New("forge: refusing to write a bundle carrying a Secret value")

// refuseSecretValues scans every blob about to be written and refuses any
// `kind: Secret` document whose data or stringData holds anything other than
// the redaction marker.
//
// Blobs are accepted as either a gzipped tar (a layer) or raw JSON (the
// config), discriminated by the gzip magic. One function, because the thing
// being asserted is about the bundle as a whole: "no blob carries a value".
func refuseSecretValues(blobs ...[]byte) error {
	for _, raw := range blobs {
		if isGzip(raw) {
			if err := refuseInLayer(raw); err != nil {
				return err
			}
			continue
		}
		if err := refuseInConfig(raw); err != nil {
			return err
		}
	}
	return nil
}

func isGzip(b []byte) bool { return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b }

// refuseInLayer walks a packed layer's entries and checks each document.
func refuseInLayer(layer []byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(layer))
	if err != nil {
		return fmt.Errorf("%w: layer is not a gzip stream: %w", ErrSecretValue, err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, nerr := tr.Next()
		if errors.Is(nerr, io.EOF) {
			return nil
		}
		if nerr != nil {
			return fmt.Errorf("%w: layer does not read back as a tar: %w", ErrSecretValue, nerr)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		data, rerr := io.ReadAll(tr)
		if rerr != nil {
			return fmt.Errorf("%w: reading %s back: %w", ErrSecretValue, hdr.Name, rerr)
		}
		// A layer entry may hold several documents (the charts layer
		// holds a whole `helm template` output).
		dec := yaml.NewDecoder(bytes.NewReader(data))
		for {
			var body any
			derr := dec.Decode(&body)
			if errors.Is(derr, io.EOF) {
				break
			}
			if derr != nil {
				return fmt.Errorf("%w: %s does not read back as YAML: %w", ErrSecretValue, hdr.Name, derr)
			}
			if err := checkSecretDocument(hdr.Name, body); err != nil {
				return err
			}
		}
	}
}

// refuseInConfig checks the BundleDoc blob: the shape's objects are hashes,
// but a Secret's redacted values are the one place a marker is expected and
// therefore the one place a non-marker would hide.
func refuseInConfig(configBlob []byte) error {
	doc, err := release.DecodeBundleDoc(configBlob)
	if err != nil {
		return fmt.Errorf("%w: the config blob does not decode, so it cannot be certified clean: %w", ErrSecretValue, err)
	}
	for _, sec := range doc.Shape.Secrets {
		// A shape secret is a NAME and a provider by type, so there is
		// no value field to inspect. What CAN go wrong is a name that
		// is itself a value, which is unknowable — so the check that
		// remains is the one that is decidable: the type carries no
		// value-bearing field, and release.Shape.Validate has already
		// refused anything structurally unexpected.
		if strings.TrimSpace(sec.Name) == "" {
			return fmt.Errorf("%w: the shape holds a nameless secret", ErrSecretValue)
		}
	}
	return nil
}

// checkSecretDocument is the actual F-13 predicate: in a `kind: Secret`, every
// value under data/stringData must be the redaction marker.
//
// It checks for the MARKER rather than scanning for things that look
// secret-ish. An allow-list of one exact shape is decidable; a blocklist of
// "what a password looks like" is not, and would pass the first value nobody
// thought of.
func checkSecretDocument(where string, body any) error {
	doc, ok := body.(map[string]any)
	if !ok || doc["kind"] != "Secret" {
		return nil
	}
	name, _ := doc["metadata"].(map[string]any)["name"].(string)
	for _, field := range secretValueFields {
		values, ok := doc[field].(map[string]any)
		if !ok {
			continue
		}
		for key, value := range values {
			s, isString := value.(string)
			if isString && strings.HasPrefix(s, release.RedactedSecretPrefix) {
				continue
			}
			return fmt.Errorf("%w: %s: Secret %q key %s.%s is not redacted "+
				"(a bundle is pushed, cached and kept forever, so the value would leak permanently)",
				ErrSecretValue, where, name, field, key)
		}
	}
	// Redacted or not, a Secret does not belong in the layer: the reconciler
	// would apply the marker over the cluster's real value and own the
	// object's lifetime. See splitSecrets.
	return fmt.Errorf("%w: %s: Secret %q is in the manifest layer; Secrets are synced by forge, never reconciled from a bundle",
		ErrSecretValue, where, name)
}

// secretValueFields are the two places a Kubernetes Secret holds a value.
var secretValueFields = []string{"data", "stringData"}
