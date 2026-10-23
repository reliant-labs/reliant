package crypto

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func randKeyB64(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func entry(id, keyB64 string) string { return id + ":" + keyB64 }

func mustKeyring(t *testing.T, entries ...string) *Keyring {
	t.Helper()
	kr, err := ParseKeyring(strings.Join(entries, ","))
	if err != nil {
		t.Fatalf("ParseKeyring: %v", err)
	}
	return kr
}

func TestSealOpenRoundTrip(t *testing.T) {
	kr := mustKeyring(t, entry("v1", randKeyB64(t)))
	plaintext := []byte("sk-test-abcdef-1234567890")

	for name, aad := range map[string][]byte{"no aad": nil, "with aad": []byte("org1|KEY")} {
		t.Run(name, func(t *testing.T) {
			ct, err := kr.Seal(plaintext, aad)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			if bytes.Contains(ct, plaintext) {
				t.Fatal("ciphertext contains plaintext")
			}
			got, err := kr.Open(ct, aad)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !bytes.Equal(got, plaintext) {
				t.Fatalf("round-trip: want %q got %q", plaintext, got)
			}
		})
	}
}

func TestSealIsRandomised(t *testing.T) {
	kr := mustKeyring(t, entry("v1", randKeyB64(t)))
	a, _ := kr.Seal([]byte("same"), nil)
	b, _ := kr.Seal([]byte("same"), nil)
	if bytes.Equal(a, b) {
		t.Fatal("expected distinct ciphertexts from the random nonce")
	}
}

func TestWrongAADFails(t *testing.T) {
	kr := mustKeyring(t, entry("v1", randKeyB64(t)))
	aad := []byte("scope/v1|4:org1|4:env1|3:KEY")
	ct, err := kr.Seal([]byte("s3cret"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kr.Open(ct, []byte("scope/v1|4:org2|4:env1|3:KEY")); err == nil {
		t.Fatal("different aad must fail authentication")
	}
	if _, err := kr.Open(ct, nil); err == nil {
		t.Fatal("unbound Open must not open an aad-bound blob")
	}
	ct2, _ := kr.Seal([]byte("x"), nil)
	if _, err := kr.Open(ct2, []byte("extra")); err == nil {
		t.Fatal("aad supplied on Open but not on Seal must fail")
	}
}

func TestTamperedBodyIsNotAConfigError(t *testing.T) {
	kr := mustKeyring(t, entry("v1", randKeyB64(t)))
	ct, _ := kr.Seal([]byte("secret-payload"), nil)
	ct[len(ct)-1] ^= 0xFF
	_, err := kr.Open(ct, nil)
	if err == nil {
		t.Fatal("expected failure")
	}
	if errors.Is(err, ErrUnknownKeyID) || errors.Is(err, ErrUnversionedCiphertext) {
		t.Fatalf("tampering must not look like a config error: %v", err)
	}
	if !strings.Contains(err.Error(), `"v1"`) || !strings.Contains(err.Error(), "message authentication failed") {
		t.Fatalf("want auth failure naming v1, got: %v", err)
	}
}

func TestOpenShortInput(t *testing.T) {
	kr := mustKeyring(t, entry("v1", randKeyB64(t)))
	for _, in := range [][]byte{nil, {1, 2, 3}} {
		if _, err := kr.Open(in, nil); !errors.Is(err, ErrUnversionedCiphertext) {
			t.Fatalf("Open(%v) = %v, want ErrUnversionedCiphertext", in, err)
		}
	}
	// Valid header but no room for a nonce.
	short := append(mustPrefix(t, "v1"), 0x00)
	if _, err := kr.Open(short, nil); err == nil || !strings.Contains(err.Error(), "too short") {
		t.Fatalf("want too-short error, got %v", err)
	}
}

func mustPrefix(t *testing.T, id string) []byte {
	t.Helper()
	p, err := EnvelopePrefixForKeyID(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUnknownKeyIDNamesTheMissingKey(t *testing.T) {
	sealer := mustKeyring(t, entry("va", randKeyB64(t)))
	sealed, err := sealer.Seal([]byte("sealed-under-va"), nil)
	if err != nil {
		t.Fatal(err)
	}
	opener := mustKeyring(t, entry("vb", randKeyB64(t)))
	_, err = opener.Open(sealed, nil)
	if !errors.Is(err, ErrUnknownKeyID) {
		t.Fatalf("want ErrUnknownKeyID, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), `"va"`) || !strings.Contains(err.Error(), "vb") {
		t.Fatalf("error must name the missing id and list held ids: %v", err)
	}
	if strings.Contains(err.Error(), "message authentication failed") {
		t.Fatalf("unknown key id must not surface as an auth failure: %v", err)
	}
}

func TestNonPrimaryKeyStillOpens(t *testing.T) {
	keyOld, keyNew := randKeyB64(t), randKeyB64(t)
	plaintext := []byte("sealed-before-the-rotation")
	sealed, err := mustKeyring(t, entry("v1", keyOld)).Seal(plaintext, nil)
	if err != nil {
		t.Fatal(err)
	}

	rotated := mustKeyring(t, entry("v2", keyNew), entry("v1", keyOld))
	got, err := rotated.Open(sealed, nil)
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("old-key blob must open after rotation: %q, %v", got, err)
	}
}

func TestNewWritesUseThePrimary(t *testing.T) {
	rotated := mustKeyring(t, entry("v2", randKeyB64(t)), entry("v1", randKeyB64(t)))
	sealed, err := rotated.Seal([]byte("after-rotation"), nil)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _, _, err := decodeEnvelope(sealed)
	if err != nil || keyID != "v2" {
		t.Fatalf("sealed under %q (%v), want primary v2", keyID, err)
	}
	if rotated.PrimaryKeyID() != "v2" {
		t.Fatalf("PrimaryKeyID = %q", rotated.PrimaryKeyID())
	}
	if got := rotated.KeyIDs(); len(got) != 2 || got[0] != "v2" || got[1] != "v1" {
		t.Fatalf("KeyIDs = %v", got)
	}
}

func TestKeyIDsReturnsACopy(t *testing.T) {
	kr := mustKeyring(t, entry("v1", randKeyB64(t)))
	kr.KeyIDs()[0] = "mutated"
	if kr.KeyIDs()[0] != "v1" {
		t.Fatal("KeyIDs must not expose internal state")
	}
}

func TestRoundTripUnderEveryKeyInTheKeyring(t *testing.T) {
	keys := map[string]string{"v1": randKeyB64(t), "v2": randKeyB64(t), "v3": randKeyB64(t)}
	sealedBy := map[string][]byte{}
	for id, key := range keys {
		blob, err := mustKeyring(t, entry(id, key)).Seal([]byte("payload-"+id), nil)
		if err != nil {
			t.Fatal(err)
		}
		sealedBy[id] = blob
	}
	all := mustKeyring(t, entry("v3", keys["v3"]), entry("v2", keys["v2"]), entry("v1", keys["v1"]))
	for id, blob := range sealedBy {
		got, err := all.Open(blob, nil)
		if err != nil || string(got) != "payload-"+id {
			t.Fatalf("blob %s: %q, %v", id, got, err)
		}
	}
}

// Same key material under two ids: key selection cannot reject a swapped id,
// so only header-binding in the AAD can.
func TestTamperedKeyIDFailsAuthentication(t *testing.T) {
	shared := randKeyB64(t)
	kr := mustKeyring(t, entry("vb", shared), entry("va", shared))
	sealed, err := kr.Seal([]byte("bound-to-its-header"), nil)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), sealed...)
	copy(tampered[keyIDOffset:keyIDOffset+2], "va")
	if id, _, _, derr := decodeEnvelope(tampered); derr != nil || id != "va" {
		t.Fatalf("precondition: want parse as va, got %q %v", id, derr)
	}
	_, err = kr.Open(tampered, nil)
	if err == nil {
		t.Fatal("editing the key id must fail authentication")
	}
	if errors.Is(err, ErrUnknownKeyID) || !strings.Contains(err.Error(), "message authentication failed") {
		t.Fatalf("want GCM auth failure, got: %v", err)
	}
	if _, err := kr.Open(sealed, nil); err != nil {
		t.Fatalf("untampered blob must open: %v", err)
	}
}

func TestUnversionedCiphertextIsRefused(t *testing.T) {
	kr := mustKeyring(t, entry("v1", randKeyB64(t)))
	legacy := make([]byte, 12+16+8)
	if _, err := rand.Read(legacy); err != nil {
		t.Fatal(err)
	}
	_, err := kr.Open(legacy, nil)
	if !errors.Is(err, ErrUnversionedCiphertext) {
		t.Fatalf("want ErrUnversionedCiphertext, got %T: %v", err, err)
	}
	if errors.Is(err, ErrUnknownKeyID) {
		t.Fatal("unversioned must not report as unknown key id")
	}
}

func TestCorruptEnvelopeHeader(t *testing.T) {
	kr := mustKeyring(t, entry("v1", randKeyB64(t)))
	cases := map[string][]byte{
		"zero id length":       append([]byte("CPK1"), 0x00, 'x'),
		"id length over limit": append([]byte("CPK1"), byte(maxKeyIDLen+1), 'x'),
		"id runs past end":     append([]byte("CPK1"), 0x09, 'a', 'b'),
		"illegal id chars":     append([]byte("CPK1"), 0x02, 'V', '!', 1, 2, 3),
	}
	for name, blob := range cases {
		if _, err := kr.Open(blob, nil); !errors.Is(err, ErrKeyringInvalid) {
			t.Errorf("%s: want ErrKeyringInvalid, got %v", name, err)
		}
	}
}

func TestEnvelopePrefixForV1IsStable(t *testing.T) {
	prefix := mustPrefix(t, "v1")
	if got := fmt.Sprintf("%x", prefix); got != "43504b31027631" {
		t.Fatalf("v1 prefix = %s, want 43504b31027631", got)
	}
	if len(prefix) != 7 {
		t.Fatalf("v1 prefix is %d bytes, documented substring(... FOR 7) needs 7", len(prefix))
	}
}

func TestEnvelopePrefixMatchesSealedBlobs(t *testing.T) {
	kr := mustKeyring(t, entry("v1", randKeyB64(t)))
	sealed, _ := kr.Seal([]byte("x"), nil)
	if !bytes.HasPrefix(sealed, mustPrefix(t, "v1")) {
		t.Fatalf("v1 prefix is not a prefix of a v1-sealed blob")
	}
	if bytes.HasPrefix(sealed, mustPrefix(t, "v2")) {
		t.Fatal("a v1-sealed blob must not match the v2 prefix")
	}
}

func TestEnvelopePrefixRejectsAMalformedKeyID(t *testing.T) {
	for _, bad := range []string{"", "V1", "v1 ", "v1:extra", strings.Repeat("v", maxKeyIDLen+1)} {
		if _, err := EnvelopePrefixForKeyID(bad); !errors.Is(err, ErrKeyringInvalid) {
			t.Errorf("EnvelopePrefixForKeyID(%q) = %v, want ErrKeyringInvalid", bad, err)
		}
	}
}

func TestParseKeyringRejectsMalformed(t *testing.T) {
	good := randKeyB64(t)
	cases := []struct{ name, value, wantInMsg string }{
		{"bad base64", "v1:!!!not-base64!!!", "decoding key"},
		{"wrong key length", "v1:" + base64.StdEncoding.EncodeToString(make([]byte, 16)), "must decode to 32 bytes"},
		{"bare key wrong length", base64.StdEncoding.EncodeToString([]byte("too-short")), "32 bytes"},
		{"duplicate key id", entry("v1", good) + "," + entry("v1", randKeyB64(t)), "appears more than once"},
		{"trailing comma", entry("v1", good) + ",", "is empty"},
		{"doubled comma", entry("v1", good) + ",," + entry("v2", good), "is empty"},
		{"bare key in multi-entry keyring", entry("v2", good) + "," + randKeyB64(t), "has no"},
		{"illegal key id characters", "V1 BAD:" + good, "must match"},
		{"empty key id", ":" + good, "must match"},
		{"overlong key id", strings.Repeat("a", 33) + ":" + good, "must match"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kr, err := ParseKeyring(tc.value)
			if err == nil {
				t.Fatalf("accepted malformed keyring %q", tc.value)
			}
			if kr != nil {
				t.Fatal("must not return a keyring alongside an error")
			}
			if !errors.Is(err, ErrKeyringInvalid) {
				t.Fatalf("want ErrKeyringInvalid, got %T: %v", err, err)
			}
			if !strings.Contains(err.Error(), tc.wantInMsg) {
				t.Fatalf("error should mention %q, got: %v", tc.wantInMsg, err)
			}
		})
	}
}

func TestBareKeyIsTheOneEntryKeyring(t *testing.T) {
	kr, err := ParseKeyring(randKeyB64(t))
	if err != nil {
		t.Fatal(err)
	}
	if kr.PrimaryKeyID() != "v1" || len(kr.KeyIDs()) != 1 {
		t.Fatalf("bare key: primary=%q ids=%v", kr.PrimaryKeyID(), kr.KeyIDs())
	}
	ct, _ := kr.Seal([]byte("p"), nil)
	if got, err := kr.Open(ct, nil); err != nil || string(got) != "p" {
		t.Fatalf("round-trip: %q %v", got, err)
	}
}

func TestKeyringIsWhitespaceTolerant(t *testing.T) {
	kr, err := ParseKeyring(" v2 : " + randKeyB64(t) + " ,  v1:" + randKeyB64(t))
	if err != nil {
		t.Fatalf("padded entries should parse: %v", err)
	}
	if kr.PrimaryKeyID() != "v2" {
		t.Fatalf("primary = %q", kr.PrimaryKeyID())
	}
}

func TestKeyringFromEnv(t *testing.T) {
	const name = "FORGE_CRYPTO_TEST_KEYRING"
	env := func(v string) func(string) string {
		return func(k string) string {
			if k != name {
				t.Fatalf("looked up %q, want %q", k, name)
			}
			return v
		}
	}

	if _, err := KeyringFromEnv(name, env("")); !errors.Is(err, ErrKeyNotConfigured) || !strings.Contains(err.Error(), name) {
		t.Fatalf("unset: want ErrKeyNotConfigured naming the var, got %v", err)
	}
	if _, err := KeyringFromEnv(name, env("   ")); !errors.Is(err, ErrKeyNotConfigured) {
		t.Fatalf("blank: want ErrKeyNotConfigured, got %v", err)
	}

	_, err := KeyringFromEnv(name, env("v1:!!!"))
	if !errors.Is(err, ErrKeyringInvalid) || errors.Is(err, ErrKeyNotConfigured) {
		t.Fatalf("malformed: want ErrKeyringInvalid only, got %v", err)
	}

	kr, err := KeyringFromEnv(name, env(entry("v2", randKeyB64(t))+","+entry("v1", randKeyB64(t))))
	if err != nil || kr.PrimaryKeyID() != "v2" {
		t.Fatalf("valid: %v %v", kr, err)
	}
}
