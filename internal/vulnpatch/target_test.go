package vulnpatch

import (
	"errors"
	"testing"
)

func TestChooseTarget(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		installed  string
		fixed      []string
		allowMajor bool
		want       string
		wantMajor  bool
		wantErr    bool
	}{
		{name: "lowest fix in the major", installed: "1.2.0", fixed: []string{"1.4.0", "1.2.5", "2.0.1"}, want: "1.2.5"},
		{name: "go spelling kept", installed: "v0.17.0", fixed: []string{"v0.31.0"}, want: "v0.31.0"},
		{name: "mixed v prefixes", installed: "v1.2.0", fixed: []string{"1.2.3"}, want: "1.2.3"},
		{name: "fix below the install is ignored", installed: "8.20.0", fixed: []string{"7.5.10", "8.21.0"}, want: "8.21.0"},
		{name: "the install is already fixed", installed: "8.21.0", fixed: []string{"8.21.0"}, want: "8.21.0"},
		{name: "major-only fix refused", installed: "1.9.0", fixed: []string{"2.0.0"}, wantMajor: true, wantErr: true},
		{name: "major-only fix with --allow-major", installed: "1.9.0", fixed: []string{"3.0.0", "2.0.0"}, allowMajor: true, want: "2.0.0"},
		{name: "same-major fix preferred under --allow-major", installed: "1.9.0", fixed: []string{"2.0.0", "1.9.4"}, allowMajor: true, want: "1.9.4"},
		{name: "v0 is one major", installed: "0.3.1", fixed: []string{"0.4.2"}, want: "0.4.2"},
		{name: "prerelease fix skipped for a release install", installed: "1.0.0", fixed: []string{"1.0.1-rc.1", "1.0.2"}, want: "1.0.2"},
		{name: "prerelease fix for a prerelease install", installed: "1.0.1-rc.0", fixed: []string{"1.0.1-rc.1"}, want: "1.0.1-rc.1"},
		{name: "no fix at or above", installed: "2.0.0", fixed: []string{"1.0.1"}, wantErr: true},
		{name: "no versions at all", installed: "1.0.0", fixed: nil, wantErr: true},
		{name: "unparseable install", installed: "1.37.0-r14x~", fixed: []string{"1.38.0"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ChooseTarget(tc.installed, tc.fixed, tc.allowMajor)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ChooseTarget = %q, want an error", got)
				}
				if errors.Is(err, ErrNeedsMajor) != tc.wantMajor {
					t.Errorf("errors.Is(ErrNeedsMajor) = %v, want %v (%v)", !tc.wantMajor, tc.wantMajor, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("ChooseTarget(%q, %v) = %q, %v; want %q", tc.installed, tc.fixed, got, err, tc.want)
			}
		})
	}
}
