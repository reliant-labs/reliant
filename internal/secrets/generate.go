package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
)

// GenerateSpec mirrors KCL's forge.GeneratedSecret: how to mint a store value
// that is pure random key material.
type GenerateSpec struct {
	Bytes    int    `json:"bytes"`
	Encoding string `json:"encoding"`
	Prefix   string `json:"prefix,omitempty"`
}

// Generate returns Prefix + Bytes of crypto-random data in the spec's encoding.
func (g GenerateSpec) Generate() (string, error) {
	if g.Bytes < 1 || g.Bytes > 1024 {
		return "", fmt.Errorf("generate: bytes must be 1..1024, got %d", g.Bytes)
	}
	buf := make([]byte, g.Bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate: read random bytes: %w", err)
	}
	switch g.Encoding {
	case "", "base64":
		return g.Prefix + base64.StdEncoding.EncodeToString(buf), nil
	case "hex":
		return g.Prefix + hex.EncodeToString(buf), nil
	}
	return "", fmt.Errorf("generate: unknown encoding %q (want base64 or hex)", g.Encoding)
}

// EnsureGenerated mints a value for every key in `wanted` that has a
// generate spec and is absent from BOTH `present` (the provider's resolved
// view, which may layer another checkout's store) and the file at path. It
// never overwrites: an existing value — even an empty one — is left alone,
// because replacing an encryption key destroys the data it protects. The
// store is written 0600 only when something was generated. It returns the
// generated key NAMES, never values.
func EnsureGenerated(path string, specs map[string]GenerateSpec, wanted []string, present map[string]string) ([]string, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	values, err := ReadSecretFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if values == nil {
		values = map[string]string{}
	}
	var generated []string
	for _, key := range wanted {
		spec, ok := specs[key]
		if !ok {
			continue
		}
		if _, ok := values[key]; ok {
			continue
		}
		if _, ok := present[key]; ok {
			continue
		}
		v, err := spec.Generate()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		values[key] = v
		generated = append(generated, key)
	}
	if len(generated) == 0 {
		return nil, nil
	}
	sort.Strings(generated)
	if err := WriteSecretFile(path, values); err != nil {
		return nil, err
	}
	return generated, nil
}
