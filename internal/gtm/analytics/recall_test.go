// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"math/rand/v2"
	"sort"
	"strings"
	"testing"
)

// Token alphabets.
const (
	base62    = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	base32    = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	lowerNum  = "0123456789abcdefghijklmnopqrstuvwxyz"
	hexDigits = "0123456789abcdef"
	nanoidABC = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz_-"
	base64ABC = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
)

// tokenShapes are the secret and id shapes of the redaction review of #3843,
// each a generator of one random sample.
var tokenShapes = map[string]func(r *rand.Rand) string{
	"stripe cus_ + base62 14": func(r *rand.Rand) string { return "cus_" + pick(r, base62, 14) },
	"base32 16":               func(r *rand.Rand) string { return pick(r, base32, 16) },
	"AWS key id AKIA + 16":    func(r *rand.Rand) string { return "AK" + "IA" + pick(r, base32, 16) },
	"/s/ + base62 16":         func(r *rand.Rand) string { return "/s/" + pick(r, base62, 16) },
	"base62 16":               func(r *rand.Rand) string { return pick(r, base62, 16) },
	"base62 22":               func(r *rand.Rand) string { return pick(r, base62, 22) },
	"?sid= + base62 20":       func(r *rand.Rand) string { return "?sid=" + pick(r, base62, 20) },
	"lowercase+digits 16":     func(r *rand.Rand) string { return pick(r, lowerNum, 16) },
	"nanoid 21":               func(r *rand.Rand) string { return pick(r, nanoidABC, 21) },
	"crockford 16":            func(r *rand.Rand) string { return pick(r, crockford, 16) },
	"sk_live_ + base62 24":    func(r *rand.Rand) string { return "sk_" + "live_" + pick(r, base62, 24) },
	"AWS secret 40":           func(r *rand.Rand) string { return pick(r, base64ABC, 40) },
	"hex 32":                  func(r *rand.Rand) string { return pick(r, hexDigits, 32) },
	"ULID 26":                 func(r *rand.Rand) string { return pick(r, "01234567", 1) + pick(r, crockford, 25) },
	"ghp_ + base62 36":        func(r *rand.Rand) string { return "gh" + "p_" + pick(r, base62, 36) },
}

func pick(r *rand.Rand, alphabet string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[r.IntN(len(alphabet))]
	}
	return string(b)
}

const (
	recallSamples = 2000
	// maxMissRate is the largest share of a shape's samples redact may keep.
	maxMissRate = 0.02
)

// TestTokenRecall draws recallSamples of each token shape from a fixed seed
// and fails a shape whose miss rate is over maxMissRate. Run with -v to see
// every shape's rate.
func TestTokenRecall(t *testing.T) {
	names := make([]string, 0, len(tokenShapes))
	for name := range tokenShapes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		gen := tokenShapes[name]
		r := rand.New(rand.NewPCG(3843, 8))
		var missed []string
		for range recallSamples {
			if v := gen(r); redact(v) != Redacted {
				missed = append(missed, v)
			}
		}
		rate := float64(len(missed)) / recallSamples
		t.Logf("%-26s miss %5.2f%% (%d/%d)", name, 100*rate, len(missed), recallSamples)
		if rate > maxMissRate {
			t.Errorf("%s: miss rate %.2f%% over %.0f%%; e.g. %s", name, 100*rate, 100*maxMissRate, strings.Join(missed[:min(5, len(missed))], " "))
		}
	}
}
