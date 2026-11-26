package secretgen

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func gen(t *testing.T, s Spec) Values {
	t.Helper()
	v, err := Generate(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRandom(t *testing.T) {
	v := gen(t, Spec{Kind: KindRandom, Names: []string{"K"}})
	raw, err := base64.StdEncoding.DecodeString(v["K"])
	if err != nil || len(raw) != 32 {
		t.Fatalf("default is 32 bytes of standard base64: %q (%v)", v["K"], err)
	}

	v = gen(t, Spec{Kind: KindRandom, Names: []string{"K"}, Bytes: 12, Encoding: "hex", Prefix: "v1:"})
	if !strings.HasPrefix(v["K"], "v1:") {
		t.Fatalf("prefix missing: %q", v["K"])
	}
	if raw, err := hex.DecodeString(strings.TrimPrefix(v["K"], "v1:")); err != nil || len(raw) != 12 {
		t.Fatalf("hex value = %q (%v)", v["K"], err)
	}

	a, b := gen(t, Spec{Kind: KindRandom, Names: []string{"K"}}), gen(t, Spec{Kind: KindRandom, Names: []string{"K"}})
	if a["K"] == b["K"] {
		t.Error("two generations returned the same value")
	}
}

func TestRandom_PinnedStream(t *testing.T) {
	src := bytes.NewReader([]byte{0xde, 0xad, 0xbe, 0xef})
	v, err := Generate(Spec{Kind: KindRandom, Names: []string{"K"}, Bytes: 4, Encoding: "hex"}, Options{Rand: src})
	if err != nil || v["K"] != "deadbeef" {
		t.Fatalf("got %v, %v", v, err)
	}
	if _, err := Generate(Spec{Kind: KindRandom, Names: []string{"K"}, Bytes: 8}, Options{Rand: bytes.NewReader([]byte{1})}); err == nil {
		t.Error("a short entropy source must fail, not return a short value")
	}
}

func TestPassword_EveryClassEveryTime(t *testing.T) {
	spec := Spec{Kind: KindPassword, Names: []string{"P"}, Length: 8}
	classes := []*regexp.Regexp{regexp.MustCompile(`[A-Z]`), regexp.MustCompile(`[a-z]`), regexp.MustCompile(`[0-9]`), regexp.MustCompile(`[!\-_.]`)}
	for i := 0; i < 2000; i++ {
		p := gen(t, spec)["P"]
		if len(p) != 8 {
			t.Fatalf("length %d: %q", len(p), p)
		}
		for _, c := range classes {
			if !c.MatchString(p) {
				t.Fatalf("%q lacks a character matching %s", p, c)
			}
		}
	}
}

func TestPassword_CustomSymbolsAndDefaults(t *testing.T) {
	p := gen(t, Spec{Kind: KindPassword, Names: []string{"P"}, Symbols: "#"})["P"]
	if len(p) != DefaultLength || !strings.Contains(p, "#") || strings.ContainsAny(p, "!-_.") {
		t.Errorf("password = %q", p)
	}
}

func TestBelowIsUniform(t *testing.T) {
	// Bytes 252 and above are rejected, so each of the 6
	// residues is equally likely.
	counts := make([]int, 6)
	for b := 0; b < 252; b++ {
		v, err := below(bytes.NewReader([]byte{byte(b), 0}), 6)
		if err != nil {
			t.Fatal(err)
		}
		counts[v]++
	}
	for _, c := range counts {
		if c != 42 {
			t.Errorf("counts = %v, want 42 each", counts)
			break
		}
	}
	// A byte at or above 252 is skipped, not folded into a residue.
	if v, _ := below(bytes.NewReader([]byte{255, 253, 3}), 6); v != 3 {
		t.Errorf("rejection sampling returned %d, want 3", v)
	}
}

func decode(t *testing.T, s, typ string) []byte {
	t.Helper()
	b, rest := pem.Decode([]byte(s))
	if b == nil || len(bytes.TrimSpace(rest)) != 0 || b.Type != typ {
		t.Fatalf("not a single %s PEM block: %.40q", typ, s)
	}
	return b.Bytes
}

func TestECP256PKCS8(t *testing.T) {
	v := gen(t, Spec{Kind: KindECP256PKCS8, Names: []string{"KEY"}})
	k, err := x509.ParsePKCS8PrivateKey(decode(t, v["KEY"], "PRIVATE KEY"))
	if err != nil {
		t.Fatal(err)
	}
	if ec, ok := k.(*ecdsa.PrivateKey); !ok || ec.Curve != elliptic.P256() {
		t.Fatalf("key = %T", k)
	}
}

func TestRSA2048_PairMatches(t *testing.T) {
	v := gen(t, Spec{Kind: KindRSA2048, Names: []string{"PRIV", "PUB"}})
	k, err := x509.ParsePKCS8PrivateKey(decode(t, v["PRIV"], "PRIVATE KEY"))
	if err != nil {
		t.Fatal(err)
	}
	priv, ok := k.(*rsa.PrivateKey)
	if !ok || priv.N.BitLen() != 2048 {
		t.Fatalf("key = %T", k)
	}
	pubAny, err := x509.ParsePKIXPublicKey(decode(t, v["PUB"], "PUBLIC KEY"))
	if err != nil {
		t.Fatal(err)
	}
	if !priv.PublicKey.Equal(pubAny) {
		t.Error("public key does not belong to the private key")
	}
}

func TestSelfSigned_CertMatchesKey(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	v, err := Generate(Spec{Kind: KindECP256SelfSigned, Names: []string{"KEY", "CERT"}, CommonName: "registry.example.com"},
		Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	k, err := x509.ParsePKCS8PrivateKey(decode(t, v["KEY"], "PRIVATE KEY"))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(decode(t, v["CERT"], "CERTIFICATE"))
	if err != nil {
		t.Fatal(err)
	}
	if !k.(*ecdsa.PrivateKey).PublicKey.Equal(cert.PublicKey) {
		t.Error("certificate is for a different key")
	}
	if cert.Subject.CommonName != "registry.example.com" || !cert.NotBefore.Equal(now) || !cert.NotAfter.Equal(now.Add(DefaultValidFor)) {
		t.Errorf("subject %q, valid %v to %v", cert.Subject.CommonName, cert.NotBefore, cert.NotAfter)
	}
	if err := cert.CheckSignatureFrom(cert); err != nil {
		t.Errorf("not self-signed: %v", err)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		spec Spec
		want string
	}{
		{"unknown kind", Spec{Kind: "nope", Names: []string{"A"}}, "unknown kind"},
		{"too few names", Spec{Kind: KindRSA2048, Names: []string{"A"}}, "writes 2 name(s)"},
		{"too many names", Spec{Kind: KindRandom, Names: []string{"A", "B"}}, "writes 1 name(s)"},
		{"bad name", Spec{Kind: KindRandom, Names: []string{"a-b"}}, "not a valid secret name"},
		{"duplicate name", Spec{Kind: KindRSA2048, Names: []string{"A", "A"}}, "listed twice"},
		{"bytes high", Spec{Kind: KindRandom, Names: []string{"A"}, Bytes: 2000}, "bytes must be"},
		{"bytes negative", Spec{Kind: KindRandom, Names: []string{"A"}, Bytes: -1}, "bytes must be"},
		{"encoding", Spec{Kind: KindRandom, Names: []string{"A"}, Encoding: "base32"}, "encoding must be"},
		{"short password", Spec{Kind: KindPassword, Names: []string{"A"}, Length: 4}, "length must be"},
		{"quote as symbol", Spec{Kind: KindPassword, Names: []string{"A"}, Symbols: `"`}, "not in"},
		{"no common name", Spec{Kind: KindECP256SelfSigned, Names: []string{"A", "B"}}, "common name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Generate(tc.spec, Options{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestKindsAreGeneratable(t *testing.T) {
	for _, k := range Kinds() {
		names := []string{"A", "B"}[:k.Arity()]
		if _, err := Generate(Spec{Kind: k, Names: names, CommonName: "x"}, Options{}); err != nil {
			t.Errorf("%s: %v", k, err)
		}
	}
}

type memStore struct {
	m         map[string]string
	writes    []string
	failOn    string
	raceOnNth int
}

func (s *memStore) Exists(_ context.Context, name string) (bool, error) {
	_, ok := s.m[name]
	return ok, nil
}

func (s *memStore) CreateOnly(_ context.Context, name, value string) (bool, error) {
	if name == s.failOn {
		return false, errors.New("boom")
	}
	if _, ok := s.m[name]; ok {
		return false, nil
	}
	s.m[name] = value
	s.writes = append(s.writes, name)
	return true, nil
}

func TestMint(t *testing.T) {
	ctx := context.Background()
	pair := Spec{Kind: KindRSA2048, Names: []string{"PRIV", "PUB"}}

	t.Run("creates once, then is a no-op", func(t *testing.T) {
		st := &memStore{m: map[string]string{}}
		created, err := Mint(ctx, st, pair, Options{})
		if err != nil || !created {
			t.Fatalf("first mint = %v, %v", created, err)
		}
		before := st.m["PRIV"]
		created, err = Mint(ctx, st, pair, Options{})
		if err != nil || created || st.m["PRIV"] != before {
			t.Fatalf("second mint created=%v err=%v changed=%v", created, err, st.m["PRIV"] != before)
		}
		if len(st.writes) != 2 {
			t.Errorf("writes = %v", st.writes)
		}
	})

	t.Run("never completes a partial pair", func(t *testing.T) {
		st := &memStore{m: map[string]string{"PRIV": "old"}}
		created, err := Mint(ctx, st, pair, Options{})
		if !errors.Is(err, ErrPartial) || created || len(st.writes) != 0 || st.m["PRIV"] != "old" {
			t.Fatalf("created=%v err=%v writes=%v", created, err, st.writes)
		}
	})

	t.Run("a failed second write leaves a pair the next mint refuses", func(t *testing.T) {
		st := &memStore{m: map[string]string{}, failOn: "PUB"}
		if _, err := Mint(ctx, st, pair, Options{}); err == nil {
			t.Fatal("want the write error")
		}
		st.failOn = ""
		if _, err := Mint(ctx, st, pair, Options{}); !errors.Is(err, ErrPartial) {
			t.Fatalf("second mint = %v, want ErrPartial", err)
		}
	})

	t.Run("loses the race quietly", func(t *testing.T) {
		st := &racingStore{memStore: memStore{m: map[string]string{}}}
		created, err := Mint(ctx, st, Spec{Kind: KindRandom, Names: []string{"K"}}, Options{})
		if err != nil || created || st.m["K"] != "winner" {
			t.Fatalf("created=%v err=%v value=%q", created, err, st.m["K"])
		}
	})

	t.Run("invalid spec writes nothing", func(t *testing.T) {
		st := &memStore{m: map[string]string{}}
		if _, err := Mint(ctx, st, Spec{Kind: KindRandom}, Options{}); err == nil || len(st.m) != 0 {
			t.Fatalf("err=%v store=%v", err, st.m)
		}
	})
}

// racingStore reports a name absent, then has another writer take it first.
type racingStore struct{ memStore }

func (s *racingStore) CreateOnly(_ context.Context, name, _ string) (bool, error) {
	s.m[name] = "winner"
	return false, nil
}
