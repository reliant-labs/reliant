package deploytarget

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestWireRolloutDecodesProtojsonInt64 pins the encoding a REAL control plane
// sends. protojson writes every 64-bit proto scalar as a JSON string, so
// `"stabilityWindowMs": "120000"` is the correct wire form and a bare number
// is what only a hand-written fixture produces.
//
// This is the regression. Every fixture in this package wrote the number
// unquoted, so the rollout path was only ever exercised against an encoding
// the server does not produce, and the field's plain `int64` type looked fine.
//
// Against the live control plane the decode returned a non-nil error, and
// readHostedRollout treats any decode error as an unreadable rollout and
// retries. So the symptom was not "the window is zero" — it was
// `forge env deploy` spending its entire 15m budget printing "rollout read
// failed, retrying", reporting the rollout UNKNOWN, and then advising a deploy
// it had already recorded, while the rollout had in fact succeeded.
//
// MUTATION VERIFIED RED: with StabilityWindowMS as a plain `int64`,
// json.Unmarshal of the protojson form returns
// "cannot unmarshal string into Go struct field .rollout.stabilityWindowMs of
// type int64". (Verified against the pre-fix struct shape directly, since the
// reverted field no longer compiles against this test's .Int64() call.)
// encoding/json does populate the fields it decoded BEFORE the bad one, so the
// loss is partial rather than total — but the error is what the caller sees,
// and that is enough to make the rollout unreadable.
func TestWireRolloutDecodesProtojsonInt64(t *testing.T) {
	const body = `{"rollout":{"promotion":{"id":"promo-1","releaseVersion":"v1"},
	  "phase":"DEPLOY_ROLLOUT_PHASE_SUCCEEDED","convergesPromotions":true,
	  "stabilityWindowMs":%s}}`

	for _, tc := range []struct {
		name, encoded string
	}{
		// What a protojson server actually sends.
		{"protojson string", `"120000"`},
		// What forge's own fixtures and any hand-written JSON send.
		{"bare number", `120000`},
		// Absent / null must stay the zero value, not an error.
		{"null", `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var resp struct {
				Rollout wireRollout `json:"rollout"`
			}
			raw := strings.Replace(body, "%s", tc.encoded, 1)
			if err := json.Unmarshal([]byte(raw), &resp); err != nil {
				t.Fatalf("decode %s: %v", tc.encoded, err)
			}
			want := int64(120000)
			if tc.encoded == `null` {
				want = 0
			}
			if got := resp.Rollout.StabilityWindowMS.Int64(); got != want {
				t.Errorf("stabilityWindowMs = %d, want %d", got, want)
			}
			// The fields beside it decode too. The point is not that they
			// were unreachable before — encoding/json fills in what it
			// reached — but that the decode now succeeds, so the caller
			// gets a usable rollout instead of an error it must retry.
			if resp.Rollout.Phase != "DEPLOY_ROLLOUT_PHASE_SUCCEEDED" {
				t.Errorf("phase = %q, want the decoded phase", resp.Rollout.Phase)
			}
			if !resp.Rollout.ConvergesPromotions {
				t.Error("convergesPromotions = false, want true")
			}
		})
	}
}

// TestWireInt64RejectsNonNumeric keeps the decoder honest: it is permissive
// about the ENCODING (string or number) and strict about the VALUE, so a
// genuinely malformed payload is still an error rather than a silent zero.
func TestWireInt64RejectsNonNumeric(t *testing.T) {
	for _, bad := range []string{`"abc"`, `"12x"`, `{}`, `[]`, `""`} {
		var v wireInt64
		if err := json.Unmarshal([]byte(bad), &v); err == nil {
			t.Errorf("%s decoded to %d, want an error", bad, v.Int64())
		}
	}
}
