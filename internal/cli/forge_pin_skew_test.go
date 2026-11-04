package cli

import "testing"

func TestPinSkew(t *testing.T) {
	const pin = "v0.1.44-0.20261007082641-d6b5d722b839"
	for _, tc := range []struct {
		name, pin, binary, commit string
		skewed, ok                bool
	}{
		{"same commit", pin, pin, "", false, true},
		{"same commit, dirty local build", pin, "v0.1.44-0.20261007160916-d6b5d722b839+dirty", "", false, true},
		{"another commit", pin, "v0.1.44-0.20261007053821-3b4499b384de", "", true, true},
		{"tag vs tag", "v0.1.42", "v0.1.43", "", true, true},
		{"same tag", "v0.1.42", "v0.1.42", "", false, true},
		{"binary names no forge, commit known", pin, "(devel)", "d6b5d722b83912345678", false, true},
		{"binary names nothing", pin, "dev", "", false, false},
		{"no pin", "", pin, "", false, false},
	} {
		skewed, ok := pinSkew(tc.pin, tc.binary, tc.commit)
		if skewed != tc.skewed || ok != tc.ok {
			t.Errorf("%s: pinSkew = (%v, %v), want (%v, %v)", tc.name, skewed, ok, tc.skewed, tc.ok)
		}
	}
}
