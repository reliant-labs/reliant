package oauth2

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// RFC 7636 Appendix B is the one external oracle available for PKCE, so the
// challenge derivation is pinned against it rather than against this package's
// own output. Deriving the expectation by calling Challenge twice would prove
// only that the function is deterministic — it would still pass if the hash
// were MD5, if the padding were wrong, or if the encoding were standard
// base64 instead of base64url.
//
// All three values below are transcribed from the RFC text
// (https://www.rfc-editor.org/rfc/rfc7636.txt, Appendix B), and the test
// re-derives the verifier and the challenge from the raw octets so that a
// transcription slip in the strings cannot silently agree with a bug.
var (
	// rfcVerifierOctets is the 32-octet random sequence the RFC starts from.
	rfcVerifierOctets = []byte{
		116, 24, 223, 180, 151, 153, 224, 37, 79, 250, 96, 125, 216, 173,
		187, 186, 22, 212, 37, 77, 105, 214, 191, 240, 91, 88, 5, 88, 83,
		132, 141, 121,
	}

	// rfcVerifier is the base64url encoding of rfcVerifierOctets.
	rfcVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

	// rfcChallengeHashOctets is SHA256(ASCII(rfcVerifier)) as published.
	rfcChallengeHashOctets = []byte{
		19, 211, 30, 150, 26, 26, 216, 236, 47, 22, 177, 12, 76, 152, 46,
		8, 118, 168, 120, 173, 109, 241, 68, 86, 110, 225, 137, 74, 203,
		112, 249, 195,
	}

	// rfcChallenge is the base64url encoding of rfcChallengeHashOctets.
	rfcChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

// TestRFC7636AppendixBIsSelfConsistent checks the oracle before the oracle is
// used to judge anything. If the transcribed octet arrays and strings above
// disagree with each other, every test that depends on them is meaningless,
// and the failure should name the transcription rather than the implementation.
//
// Nothing in this test calls into the package under test.
func TestRFC7636AppendixBIsSelfConsistent(t *testing.T) {
	if len(rfcVerifierOctets) != 32 {
		t.Fatalf("RFC verifier seed should be 32 octets, transcribed %d", len(rfcVerifierOctets))
	}
	if got := base64.RawURLEncoding.EncodeToString(rfcVerifierOctets); got != rfcVerifier {
		t.Fatalf("transcribed RFC octets encode to %q, but transcribed RFC verifier is %q", got, rfcVerifier)
	}
	sum := sha256.Sum256([]byte(rfcVerifier))
	if !bytes.Equal(sum[:], rfcChallengeHashOctets) {
		t.Fatalf("SHA256 of RFC verifier is %v, but transcribed RFC hash octets are %v", sum[:], rfcChallengeHashOctets)
	}
	if got := base64.RawURLEncoding.EncodeToString(rfcChallengeHashOctets); got != rfcChallenge {
		t.Fatalf("transcribed RFC hash octets encode to %q, but transcribed RFC challenge is %q", got, rfcChallenge)
	}
}

// TestChallengeMatchesRFC7636AppendixB is the load-bearing correctness test:
// the challenge this package derives must equal the value the RFC publishes
// for the same verifier.
func TestChallengeMatchesRFC7636AppendixB(t *testing.T) {
	v, err := ParseVerifier(rfcVerifier)
	if err != nil {
		t.Fatalf("ParseVerifier(RFC Appendix B verifier): %v", err)
	}

	got := v.Challenge()
	if got.Method != MethodS256 {
		t.Errorf("Challenge method = %q, want %q", got.Method, MethodS256)
	}
	if got.Value != rfcChallenge {
		t.Errorf("challenge for RFC Appendix B verifier:\n got %q\nwant %q (RFC 7636 Appendix B)", got.Value, rfcChallenge)
	}

	// Decoding the challenge must reproduce the RFC's published hash
	// octets. This catches an encoding that happens to round-trip through
	// a string comparison but is not base64url-no-pad.
	decoded, err := base64.RawURLEncoding.DecodeString(got.Value)
	if err != nil {
		t.Fatalf("challenge %q is not valid unpadded base64url: %v", got.Value, err)
	}
	if !bytes.Equal(decoded, rfcChallengeHashOctets) {
		t.Errorf("decoded challenge octets = %v, want %v (RFC 7636 Appendix B)", decoded, rfcChallengeHashOctets)
	}
}

// TestChallengeIsUnpaddedBase64URL pins the two encoding details a hand-rolled
// implementation gets wrong: base64url's alphabet ("-" and "_" rather than "+"
// and "/") and the absence of "=" padding, which RFC 7636 §4.2 requires.
func TestChallengeIsUnpaddedBase64URL(t *testing.T) {
	v, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	c := v.Challenge()

	if strings.ContainsAny(c.Value, "=+/") {
		t.Errorf("challenge %q contains padding or standard-base64 characters; RFC 7636 §4.2 requires unpadded base64url", c.Value)
	}
	// SHA-256 is 32 bytes, which is 43 characters unpadded (ceil(32*4/3)).
	// The length is derived from the digest size, not hardcoded prose.
	wantLen := (sha256.Size*8 + 5) / 6
	if len(c.Value) != wantLen {
		t.Errorf("challenge length = %d, want %d for a %d-byte digest in unpadded base64", len(c.Value), wantLen, sha256.Size)
	}
	if _, err := base64.RawURLEncoding.DecodeString(c.Value); err != nil {
		t.Errorf("challenge %q does not decode as unpadded base64url: %v", c.Value, err)
	}
}

// TestNewVerifierIsUniqueAndLegal checks the two properties a verifier
// generator can fail at independently: entropy (values must differ) and
// conformance (length and alphabet per RFC 7636 §4.1).
//
// Uniqueness is asserted over a derived set, and an empty or short set is a
// hard failure rather than a vacuous pass.
func TestNewVerifierIsUniqueAndLegal(t *testing.T) {
	const n = 256
	seen := make(map[string]int, n)
	for i := 0; i < n; i++ {
		v, err := NewVerifier()
		if err != nil {
			t.Fatalf("NewVerifier call %d: %v", i, err)
		}
		s := v.Secret()

		if err := validateVerifier(s); err != nil {
			t.Fatalf("NewVerifier produced a verifier that violates RFC 7636 §4.1 (length %d): %v", len(s), err)
		}
		for j := 0; j < len(s); j++ {
			if !strings.ContainsRune(unreservedChars, rune(s[j])) {
				t.Fatalf("verifier contains character %q at index %d, outside the unreserved set", s[j], j)
			}
		}
		if prev, dup := seen[s]; dup {
			t.Fatalf("NewVerifier returned the same verifier on calls %d and %d", prev, i)
		}
		seen[s] = i
	}

	if len(seen) != n {
		t.Fatalf("collected %d distinct verifiers from %d calls, want %d", len(seen), n, n)
	}

	// Distinct challenges follow from distinct verifiers, but check it
	// directly: a Challenge that ignored its receiver would still pass the
	// uniqueness check above.
	challenges := make(map[string]struct{}, len(seen))
	for s := range seen {
		v, err := ParseVerifier(s)
		if err != nil {
			t.Fatalf("ParseVerifier round-trip: %v", err)
		}
		challenges[v.Challenge().Value] = struct{}{}
	}
	if len(challenges) != len(seen) {
		t.Errorf("%d distinct verifiers produced only %d distinct challenges", len(seen), len(challenges))
	}
}

// TestNewVerifierLengthBounds checks that the RFC's 43..128 window is enforced
// at both edges and that the produced length is the requested one.
func TestNewVerifierLengthBounds(t *testing.T) {
	for _, n := range []int{MinVerifierLength, 64, 100, MaxVerifierLength} {
		v, err := NewVerifierLength(n)
		if err != nil {
			t.Fatalf("NewVerifierLength(%d): %v", n, err)
		}
		if got := len(v.Secret()); got != n {
			t.Errorf("NewVerifierLength(%d) produced length %d", n, got)
		}
		if err := validateVerifier(v.Secret()); err != nil {
			t.Errorf("NewVerifierLength(%d) produced an invalid verifier: %v", n, err)
		}
	}
	for _, n := range []int{0, 1, MinVerifierLength - 1, MaxVerifierLength + 1, 1000, -5} {
		if _, err := NewVerifierLength(n); err == nil {
			t.Errorf("NewVerifierLength(%d) returned no error, but %d is outside [%d, %d]", n, n, MinVerifierLength, MaxVerifierLength)
		}
	}
}

// TestNewVerifierLengthUsesWholeAlphabet guards the rejection-sampling loop.
// A modulo-biased or truncated alphabet would still produce legal-looking
// verifiers, so the check is that every character of the unreserved set shows
// up across a large sample.
func TestNewVerifierLengthUsesWholeAlphabet(t *testing.T) {
	used := map[byte]struct{}{}
	for i := 0; i < 200; i++ {
		v, err := NewVerifierLength(MaxVerifierLength)
		if err != nil {
			t.Fatalf("NewVerifierLength: %v", err)
		}
		for j := 0; j < len(v.Secret()); j++ {
			used[v.Secret()[j]] = struct{}{}
		}
	}
	if len(used) == 0 {
		t.Fatal("no characters observed; the sample set is empty")
	}
	var missing []string
	for i := 0; i < len(unreservedChars); i++ {
		if _, ok := used[unreservedChars[i]]; !ok {
			missing = append(missing, string(unreservedChars[i]))
		}
	}
	if len(missing) > 0 {
		t.Errorf("characters never produced across 200×%d samples: %v — generator may not draw from the full unreserved set", MaxVerifierLength, missing)
	}
}

func TestParseVerifierRejectsMalformed(t *testing.T) {
	valid := strings.Repeat("a", MinVerifierLength)
	cases := map[string]string{
		"empty":            "",
		"too short":        strings.Repeat("a", MinVerifierLength-1),
		"too long":         strings.Repeat("a", MaxVerifierLength+1),
		"space":            valid[:MinVerifierLength-1] + " ",
		"plus":             valid[:MinVerifierLength-1] + "+",
		"slash":            valid[:MinVerifierLength-1] + "/",
		"equals":           valid[:MinVerifierLength-1] + "=",
		"non-ascii":        valid[:MinVerifierLength-1] + "é",
		"percent-encoding": valid[:MinVerifierLength-3] + "%20",
	}
	for name, in := range cases {
		if _, err := ParseVerifier(in); err == nil {
			t.Errorf("ParseVerifier(%s) returned no error, want rejection", name)
		}
	}
	if _, err := ParseVerifier(valid); err != nil {
		t.Errorf("ParseVerifier(legal 43-char verifier) = %v, want nil", err)
	}
}

// TestVerifierErrorsDoNotLeakSecret checks that a rejection message never
// quotes the value it rejected — validation errors are prime candidates for
// being logged verbatim.
func TestVerifierErrorsDoNotLeakSecret(t *testing.T) {
	secretish := strings.Repeat("s3cr3t", 30) // too long, will be rejected
	_, err := ParseVerifier(secretish)
	if err == nil {
		t.Fatal("expected an error for an over-long verifier")
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("ParseVerifier error leaks the rejected value: %q", err.Error())
	}
}

// TestPlainMethodIsNotReachableByDefault is the guard for RFC 7636 §7.2. The
// assertion is not "the default is S256" as a literal; it is that the only
// path that yields a plain challenge is one where the caller named
// MethodPlain, and that a plain challenge is distinguishable from an S256 one
// by being equal to the verifier.
func TestPlainMethodIsNotReachableByDefault(t *testing.T) {
	v, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	def := v.Challenge()
	if def.Method == MethodPlain {
		t.Error("Verifier.Challenge produced a plain challenge; plain must never be the default")
	}
	if def.Value == v.Secret() {
		t.Error("default challenge equals the verifier, which is the plain construction; the verifier must be hashed")
	}
	// Derived, not hardcoded: the default must be the S256 construction.
	sum := sha256.Sum256([]byte(v.Secret()))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); def.Value != want {
		t.Errorf("default challenge = %q, want the S256 construction %q", def.Value, want)
	}

	// An AuthRequest built from the default must advertise S256 on the wire.
	u, err := AuthRequest{
		Endpoint:    "https://idp.example.test/authorize",
		ClientID:    "client",
		RedirectURI: "https://app.example.test/cb",
		State:       "state-value",
		Challenge:   def,
	}.URL()
	if err != nil {
		t.Fatalf("AuthRequest.URL: %v", err)
	}
	if strings.Contains(u, "code_challenge_method=plain") {
		t.Errorf("authorization URL from a default challenge advertises plain: %s", u)
	}

	// Explicitly requested plain is the verifier verbatim — the property
	// that makes it a downgrade.
	p, err := v.ChallengeWithMethod(MethodPlain)
	if err != nil {
		t.Fatalf("ChallengeWithMethod(plain): %v", err)
	}
	if p.Method != MethodPlain || p.Value != v.Secret() {
		t.Errorf("ChallengeWithMethod(plain) = {%q, %q}, want the verifier verbatim with method plain", p.Value, p.Method)
	}

	// S256 named explicitly must agree with the default.
	s, err := v.ChallengeWithMethod(MethodS256)
	if err != nil {
		t.Fatalf("ChallengeWithMethod(S256): %v", err)
	}
	if s != def {
		t.Errorf("ChallengeWithMethod(S256) = %+v, differs from Challenge() = %+v", s, def)
	}

	if _, err := v.ChallengeWithMethod(Method("S512")); err == nil {
		t.Error("ChallengeWithMethod accepted an unknown method; unrecognized input must be rejected, not defaulted")
	}
	if _, err := v.ChallengeWithMethod(Method("")); err == nil {
		t.Error("ChallengeWithMethod accepted an empty method")
	}
}

// TestZeroVerifierDoesNotProduceAChallenge pins the panic on the zero value.
// Hashing "" would yield a well-formed challenge that no verifier can redeem,
// which is the "guard that cannot fail" shape: it looks like success and fails
// later at the token endpoint.
func TestZeroVerifierDoesNotProduceAChallenge(t *testing.T) {
	emptyHash := base64.RawURLEncoding.EncodeToString(func() []byte { s := sha256.Sum256(nil); return s[:] }())

	func() {
		defer func() {
			if recover() == nil {
				t.Error("Verifier{}.Challenge() did not panic; it must not silently hash the empty verifier")
			}
		}()
		got := Verifier{}.Challenge()
		if got.Value == emptyHash {
			t.Errorf("Verifier{}.Challenge() returned SHA256(\"\") = %q", got.Value)
		}
	}()

	if _, err := (Verifier{}).ChallengeWithMethod(MethodS256); err == nil {
		t.Error("Verifier{}.ChallengeWithMethod(S256) returned no error")
	}
}

// TestVerifierRedaction checks every channel through which a secret escapes by
// accident. The assertion is that the actual secret does not appear in the
// rendered output — derived from the live value, not compared to a fixed
// placeholder string.
func TestVerifierRedaction(t *testing.T) {
	v, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	secret := v.Secret()
	if secret == "" {
		t.Fatal("Secret() is empty; the redaction test would be vacuous")
	}

	renders := map[string]string{
		"%v":            fmt.Sprintf("%v", v),
		"%s":            fmt.Sprintf("%s", v),
		"%q":            fmt.Sprintf("%q", v),
		"%#v":           fmt.Sprintf("%#v", v),
		"%+v":           fmt.Sprintf("%+v", v),
		"String()":      v.String(),
		"GoString()":    v.GoString(),
		"pointer %v":    fmt.Sprintf("%v", &v),
		"in struct %v":  fmt.Sprintf("%v", struct{ V Verifier }{v}),
		"in struct %#v": fmt.Sprintf("%#v", struct{ V Verifier }{v}),
		"in slice %v":   fmt.Sprintf("%v", []Verifier{v}),
		"in map %v":     fmt.Sprintf("%v", map[string]Verifier{"k": v}),
	}
	for what, out := range renders {
		if strings.Contains(out, secret) {
			t.Errorf("%s leaked the code verifier: %s", what, out)
		}
	}

	// slog is the channel most likely to be used in anger.
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("login", "verifier", v, "wrapped", struct{ V Verifier }{v})
	if strings.Contains(buf.String(), secret) {
		t.Errorf("slog leaked the code verifier: %s", buf.String())
	}
	var jbuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&jbuf, nil)).Info("login", "verifier", v)
	if strings.Contains(jbuf.String(), secret) {
		t.Errorf("slog JSON handler leaked the code verifier: %s", jbuf.String())
	}

	// JSON must fail rather than emit the secret. A silent placeholder
	// would produce a session that stops working for no visible reason.
	if b, err := json.Marshal(v); !errors.Is(err, ErrVerifierRedacted) {
		t.Errorf("json.Marshal(Verifier) = (%s, %v), want ErrVerifierRedacted", b, err)
	} else if strings.Contains(string(b), secret) {
		t.Errorf("json.Marshal output leaked the verifier: %s", b)
	}
	if b, err := json.Marshal(struct{ V Verifier }{v}); err == nil {
		t.Errorf("json.Marshal of a struct embedding a Verifier succeeded: %s", b)
	} else if strings.Contains(err.Error(), secret) {
		t.Errorf("json.Marshal error text leaked the verifier: %v", err)
	}
	if b, err := v.MarshalText(); !errors.Is(err, ErrVerifierRedacted) {
		t.Errorf("MarshalText = (%s, %v), want ErrVerifierRedacted", b, err)
	}

	// And the deliberate path still works, or the type would be useless.
	if v.Secret() != secret {
		t.Error("Secret() is not stable across calls")
	}
}
