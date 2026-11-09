package scaffold

import "testing"

func TestValidateScenarioName(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty", "", true},
		{"leading-digit", "1scenario", true},
		{"uppercase", "GitHub", true},
		{"trailing-hyphen", "scenario-", true},
		{"consecutive-hyphens", "a--b", true},
		{"underscore", "github_connected", true},
		{"dot", "x.y", true},
		{"ok-simple", "default", false},
		{"ok-kebab", "github-connected", false},
		{"ok-with-digits", "a1b2-c3", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateScenarioName(tc.input)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateScenarioName(%q): got err=%v, wantErr=%v", tc.input, err, tc.wantErr)
			}
		})
	}
}
