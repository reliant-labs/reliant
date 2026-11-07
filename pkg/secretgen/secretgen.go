// Package secretgen mints the secret values a project declares as generated:
// random key material, passwords that satisfy a complexity policy, and key
// pairs and certificates.
//
// A [Spec] names a [Kind], the secret names the result is stored under, and the
// kind's parameters. [Generate] turns it into [Values] (name to string) and
// either returns every value or none. [Mint] is the create-only write on top:
// it generates and stores a value only when the store holds none, so re-running
// it, from a converge Job or a promotion, never rotates a live secret.
//
// # Kinds
//
//	KindRandom            Bytes of randomness, hex or base64, with an optional prefix.   1 name
//	KindPassword          Letters, digits and symbols, with at least one of each class.  1 name
//	KindECP256PKCS8       An EC P-256 private key, PKCS#8 PEM.                           1 name
//	KindRSA2048           An RSA 2048 private key (PKCS#8 PEM), then its public key.     2 names
//	KindECP256SelfSigned  An EC P-256 private key (PKCS#8 PEM), then a self-signed cert. 2 names
//
// For a two-name kind, Names are in the order of the table: the private key
// first. The pair always comes from one generation, never one half from each of
// two runs; [Mint] refuses to fill in the missing half of a pair (see
// [ErrPartial]) because that half could not match the one already stored.
//
// # Randomness
//
// Everything comes from crypto/rand. The Random and Password kinds read from
// [Options.Rand] so tests can pin the stream; key generation always uses the
// system source, because Go's key generators ignore a caller's reader.
//
// # Consuming it
//
// Mint declares the two-method [Store] it needs. A KV-v2 client with a
// check-and-set write satisfies it with a thin adapter.
//
//forge:exclude-contract: pkg/ library: value generation over the standard library, one implementation
package secretgen

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strings"
	"time"
)

// Kind names how a value is generated. The set is closed.
type Kind string

const (
	KindRandom           Kind = "random"
	KindPassword         Kind = "password"
	KindECP256PKCS8      Kind = "ec_p256_pkcs8"
	KindRSA2048          Kind = "rsa_2048"
	KindECP256SelfSigned Kind = "ec_p256_selfsigned"
)

// Kinds lists every kind.
func Kinds() []Kind {
	return []Kind{KindRandom, KindPassword, KindECP256PKCS8, KindRSA2048, KindECP256SelfSigned}
}

// Arity is how many names a kind produces, or 0 for an unknown kind.
func (k Kind) Arity() int {
	switch k {
	case KindRandom, KindPassword, KindECP256PKCS8:
		return 1
	case KindRSA2048, KindECP256SelfSigned:
		return 2
	}
	return 0
}

// Defaults applied when a parameter is left zero.
const (
	DefaultBytes     = 32
	DefaultEncoding  = "base64"
	DefaultLength    = 32
	DefaultSymbols   = "!-_."
	DefaultValidFor  = 3700 * 24 * time.Hour
	MaxBytes         = 1024
	MinPasswordLen   = 8
	MaxPasswordLen   = 1024
	passwordClasses  = 4
	upperLetters     = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	lowerLetters     = "abcdefghijklmnopqrstuvwxyz"
	digitCharacters  = "0123456789"
	allowedSymbolSet = "!#%*+-.:=@^_~"
)

// Spec declares one generated value (or one atomic pair).
type Spec struct {
	Kind Kind
	// Names are the secret names the result is stored under, one per output of
	// the kind, in the order the package doc lists.
	Names []string

	// KindRandom: Bytes of randomness (default 32, at most 1024), written in
	// Encoding ("base64" standard, the default, or "hex"), after Prefix.
	Bytes    int
	Encoding string
	Prefix   string

	// KindPassword: Length characters (default 32; 8 to 1024) with at least one
	// upper-case letter, lower-case letter, digit and symbol. Symbols is the
	// symbol alphabet (default "!-_."), drawn from "!#%*+-.:=@^_~".
	Length  int
	Symbols string

	// KindECP256SelfSigned: the certificate's subject CommonName (required) and
	// lifetime (default 3700 days).
	CommonName string
	ValidFor   time.Duration
}

var nameForm = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Validate reports every way the spec is unusable.
func (s Spec) Validate() error {
	var errs []error
	n := s.Kind.Arity()
	if n == 0 {
		return fmt.Errorf("secretgen: unknown kind %q (kinds: %v)", s.Kind, Kinds())
	}
	if len(s.Names) != n {
		errs = append(errs, fmt.Errorf("kind %s writes %d name(s), got %d", s.Kind, n, len(s.Names)))
	}
	seen := map[string]bool{}
	for _, name := range s.Names {
		if !nameForm.MatchString(name) {
			errs = append(errs, fmt.Errorf("%q is not a valid secret name", name))
		}
		if seen[name] {
			errs = append(errs, fmt.Errorf("name %q is listed twice", name))
		}
		seen[name] = true
	}
	switch s.Kind {
	case KindRandom:
		if b := s.bytes(); b < 1 || b > MaxBytes {
			errs = append(errs, fmt.Errorf("bytes must be 1..%d, got %d", MaxBytes, b))
		}
		if e := s.encoding(); e != "base64" && e != "hex" {
			errs = append(errs, fmt.Errorf("encoding must be \"base64\" or \"hex\", got %q", e))
		}
	case KindPassword:
		if l := s.length(); l < MinPasswordLen || l > MaxPasswordLen {
			errs = append(errs, fmt.Errorf("length must be %d..%d, got %d", MinPasswordLen, MaxPasswordLen, l))
		}
		if sym := s.symbols(); sym == "" {
			errs = append(errs, errors.New("symbols must not be empty"))
		} else {
			for _, r := range sym {
				if !strings.ContainsRune(allowedSymbolSet, r) {
					errs = append(errs, fmt.Errorf("symbol %q is not in %q", r, allowedSymbolSet))
				}
			}
		}
	case KindECP256SelfSigned:
		if strings.TrimSpace(s.CommonName) == "" {
			errs = append(errs, errors.New("common name is required"))
		}
		if s.validFor() <= 0 {
			errs = append(errs, errors.New("valid-for must be positive"))
		}
	}
	return errors.Join(errs...)
}

func (s Spec) bytes() int {
	if s.Bytes == 0 {
		return DefaultBytes
	}
	return s.Bytes
}

func (s Spec) encoding() string {
	if s.Encoding == "" {
		return DefaultEncoding
	}
	return s.Encoding
}

func (s Spec) length() int {
	if s.Length == 0 {
		return DefaultLength
	}
	return s.Length
}

func (s Spec) symbols() string {
	if s.Symbols == "" {
		return DefaultSymbols
	}
	return s.Symbols
}

func (s Spec) validFor() time.Duration {
	if s.ValidFor == 0 {
		return DefaultValidFor
	}
	return s.ValidFor
}

// Values maps secret name to value.
type Values map[string]string

// Options injects the nondeterministic inputs. The zero value uses crypto/rand
// and the wall clock.
type Options struct {
	// Rand feeds KindRandom and KindPassword. Keys never use it.
	Rand io.Reader
	// Now stamps certificates.
	Now func() time.Time
}

func (o Options) rand() io.Reader {
	if o.Rand == nil {
		return rand.Reader
	}
	return o.Rand
}

func (o Options) now() time.Time {
	if o.Now == nil {
		return time.Now()
	}
	return o.Now()
}

// Generate produces every value the spec names, or an error and no values.
func Generate(s Spec, opts Options) (Values, error) {
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("secretgen: %w", err)
	}
	var parts []string
	var err error
	switch s.Kind {
	case KindRandom:
		parts, err = genRandom(s, opts)
	case KindPassword:
		parts, err = genPassword(s, opts)
	case KindECP256PKCS8:
		parts, err = genECKey()
	case KindRSA2048:
		parts, err = genRSA()
	case KindECP256SelfSigned:
		parts, err = genSelfSigned(s, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("secretgen: %s: %w", s.Kind, err)
	}
	out := make(Values, len(parts))
	for i, name := range s.Names {
		out[name] = parts[i]
	}
	return out, nil
}

func genRandom(s Spec, o Options) ([]string, error) {
	buf := make([]byte, s.bytes())
	if _, err := io.ReadFull(o.rand(), buf); err != nil {
		return nil, err
	}
	enc := hex.EncodeToString(buf)
	if s.encoding() == "base64" {
		enc = base64.StdEncoding.EncodeToString(buf)
	}
	return []string{s.Prefix + enc}, nil
}

func genPassword(s Spec, o Options) ([]string, error) {
	r := o.rand()
	classes := []string{upperLetters, lowerLetters, digitCharacters, s.symbols()}
	all := strings.Join(classes, "")

	n := s.length()
	chars := make([]byte, n)
	for i := range chars {
		c, err := pick(r, all)
		if err != nil {
			return nil, err
		}
		chars[i] = c
	}
	// Overwrite one distinct, randomly chosen position per class so the policy
	// holds for every password rather than for most of them.
	positions := make([]int, n)
	for i := range positions {
		positions[i] = i
	}
	for i := 0; i < passwordClasses; i++ {
		j, err := below(r, n-i)
		if err != nil {
			return nil, err
		}
		positions[i], positions[i+j] = positions[i+j], positions[i]
		c, err := pick(r, classes[i])
		if err != nil {
			return nil, err
		}
		chars[positions[i]] = c
	}
	return []string{string(chars)}, nil
}

// pick returns a uniformly chosen byte of alphabet.
func pick(r io.Reader, alphabet string) (byte, error) {
	i, err := below(r, len(alphabet))
	if err != nil {
		return 0, err
	}
	return alphabet[i], nil
}

// below returns a uniform integer in [0, n) by rejection sampling, so no value
// is likelier than another.
func below(r io.Reader, n int) (int, error) {
	if n <= 1 {
		return 0, nil
	}
	limit := 256 - 256%n
	var b [1]byte
	for {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, err
		}
		if int(b[0]) < limit {
			return int(b[0]) % n, nil
		}
	}
}

func pemPKCS8(key any) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

func genECKey() ([]string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	p, err := pemPKCS8(key)
	return []string{p}, err
}

func genRSA() ([]string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	priv, err := pemPKCS8(key)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	pub := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	return []string{priv, pub}, nil
}

func genSelfSigned(s Spec, o Options) ([]string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	now := o.now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: s.CommonName},
		NotBefore:             now,
		NotAfter:              now.Add(s.validFor()),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true, // what `openssl req -x509` issues, and what a self-signed trust anchor is
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	priv, err := pemPKCS8(key)
	if err != nil {
		return nil, err
	}
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return []string{priv, cert}, nil
}

// ErrPartial reports that some of a spec's names exist in the store and others
// do not. The missing ones are not generated: they could not match the ones
// already stored (a private key from one run, its certificate from another).
// Delete the leftover names and mint again.
var ErrPartial = errors.New("secretgen: only some of the spec's names exist")

// Store is the create-only access [Mint] needs.
type Store interface {
	// Exists reports whether a value is stored under name.
	Exists(ctx context.Context, name string) (bool, error)
	// CreateOnly stores value under name only if nothing is stored there (a
	// KV-v2 check-and-set of 0) and reports whether it did. Losing that race
	// is not an error: it returns false.
	CreateOnly(ctx context.Context, name, value string) (created bool, err error)
}

// Mint stores the spec's values unless they are already there.
//
//   - Every name exists: nothing is generated or written; created is false.
//   - No name exists: the spec is generated and written in Names order. Losing
//     the race for the first name means another writer minted it, and Mint
//     writes nothing further and returns created = false.
//   - Some names exist: [ErrPartial].
//
// A failure after the first name was written leaves a partial pair, which the
// next Mint reports as [ErrPartial].
func Mint(ctx context.Context, st Store, s Spec, opts Options) (created bool, err error) {
	if err := s.Validate(); err != nil {
		return false, fmt.Errorf("secretgen: %w", err)
	}
	var have []string
	for _, name := range s.Names {
		ok, err := st.Exists(ctx, name)
		if err != nil {
			return false, fmt.Errorf("secretgen: checking %s: %w", name, err)
		}
		if ok {
			have = append(have, name)
		}
	}
	switch {
	case len(have) == len(s.Names):
		return false, nil
	case len(have) > 0:
		return false, fmt.Errorf("%w: present %v of %v", ErrPartial, have, s.Names)
	}
	vals, err := Generate(s, opts)
	if err != nil {
		return false, err
	}
	for i, name := range s.Names {
		ok, err := st.CreateOnly(ctx, name, vals[name])
		if err != nil {
			return false, fmt.Errorf("secretgen: writing %s: %w", name, err)
		}
		if !ok {
			if i == 0 {
				return false, nil
			}
			return false, fmt.Errorf("%w: %s appeared while minting", ErrPartial, name)
		}
	}
	return true, nil
}
