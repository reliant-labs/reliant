package vulnpatch

import (
	"errors"
	"testing"
)

func TestPyVersionOrder(t *testing.T) {
	t.Parallel()
	// Each is lower than the next: PEP 440's own ordering example, plus
	// epochs, zero padding and local labels.
	ordered := []string{
		"1.0.dev456", "1.0a1", "1.0a2.dev456", "1.0a12.dev456", "1.0a12",
		"1.0b1.dev456", "1.0b2", "1.0b2.post345.dev456", "1.0b2.post345",
		"1.0rc1.dev456", "1.0rc1", "1.0", "1.0+abc.5", "1.0.post456.dev34",
		"1.0.post456", "1.0.1", "1.1.dev1", "1.2", "1.10", "2!0.1",
	}
	for i := 0; i+1 < len(ordered); i++ {
		if c := comparePyVersions(ordered[i], ordered[i+1]); c >= 0 {
			t.Errorf("compare(%s, %s) = %d, want < 0", ordered[i], ordered[i+1], c)
		}
		if c := comparePyVersions(ordered[i+1], ordered[i]); c <= 0 {
			t.Errorf("compare(%s, %s) = %d, want > 0", ordered[i+1], ordered[i], c)
		}
	}
	for _, eq := range [][2]string{{"2.31", "2.31.0"}, {"1.0-1", "1.0.post1"}, {"1.0RC1", "1.0rc1"}, {"v1.0", "1.0"}, {"1.0alpha", "1.0a0"}, {"0!1.0", "1.0"}} {
		if !pyEqual(eq[0], eq[1]) {
			t.Errorf("pyEqual(%s, %s) = false", eq[0], eq[1])
		}
	}
	for _, bad := range []string{"", "latest", "1.0-beta-x", "1..0"} {
		if _, ok := parsePyVersion(bad); ok {
			t.Errorf("parsePyVersion(%q) parsed", bad)
		}
	}
}

func TestChoosePythonTarget(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		installed string
		fixed     []string
		major     bool
		want      string
		needMajor bool
	}{
		{installed: "2.31.0", fixed: []string{"2.32.4", "2.32.0"}, want: "2.32.0"},
		{installed: "2.31.0", fixed: []string{"3.0.1"}, needMajor: true},
		{installed: "2.31.0", fixed: []string{"3.0.1"}, major: true, want: "3.0.1"},
		{installed: "2.31.0", fixed: []string{"2.32.0rc1", "2.32.1"}, want: "2.32.1"}, // no pre-release for a final install
		{installed: "2.32.0b1", fixed: []string{"2.32.0rc1", "2.32.1"}, want: "2.32.0rc1"},
		{installed: "1.0.0.1", fixed: []string{"1.0.0.2"}, want: "1.0.0.2"}, // four segments: not semver
		{installed: "0.4", fixed: []string{"0.4.1"}, want: "0.4.1"},
		{installed: "2023.10", fixed: []string{"2024.1"}, needMajor: true}, // calendar versions are majors
	} {
		got, err := choosePythonTarget(tc.installed, tc.fixed, tc.major)
		if tc.needMajor {
			if !errors.Is(err, ErrNeedsMajor) {
				t.Errorf("%s %v: err = %v, want ErrNeedsMajor", tc.installed, tc.fixed, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s %v = %q, %v; want %q", tc.installed, tc.fixed, got, err, tc.want)
		}
	}
	if _, err := choosePythonTarget("2.31.0", []string{"2.30.0"}, false); err == nil {
		t.Error("a fix below the install was chosen")
	}
	if _, err := choosePythonTarget("weird", []string{"1.0"}, false); err == nil {
		t.Error("an unparseable install was accepted")
	}
}

func TestPySpecifiers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		spec, version string
		admits, known bool
	}{
		{"", "1.0", true, true},
		{">=2.0,<3", "2.32.4", true, true},
		{">=2.0, <2.32", "2.32.4", false, true},
		{"==2.31.0", "2.32.4", false, true},
		{"==2.*", "2.32.4", true, true},
		{"!=2.32.*", "2.32.4", false, true},
		{"~=2.31", "2.32.4", true, true},
		{"~=2.31.0", "2.32.4", false, true},
		{"===2.32.4", "2.32.4", true, true},
		{"==1.0", "1.0+local", true, true},
		{"> 2", "2.32.4", true, true},
		{"~=2", "2.1", false, false}, // ~= needs two segments
		{"bogus", "1.0", false, false},
	} {
		admits, known := admitsPEP440(tc.spec, tc.version)
		if admits != tc.admits || known != tc.known {
			t.Errorf("admitsPEP440(%q, %s) = %v, %v; want %v, %v", tc.spec, tc.version, admits, known, tc.admits, tc.known)
		}
	}
	for _, tc := range []struct {
		constraint, version string
		admits, known       bool
	}{
		{"^2.31", "2.32.4", true, true},
		{"^2.31", "3.0.0", false, true},
		{"^0.2.3", "0.2.9", true, true},
		{"^0.2.3", "0.3.0", false, true},
		{"^0.0.3", "0.0.4", false, true},
		{"~2.31.0", "2.32.4", false, true},
		{"~2.31", "2.31.9", true, true},
		{"~2", "2.32.4", true, true},
		{"2.31.0", "2.32.4", false, true},
		{"2.*", "2.32.4", true, true},
		{"*", "9.9", true, true},
		{">= 2.0, < 3.0", "2.32.4", true, true},
		{">=2.0 <2.32", "2.32.4", false, true},
		{"^1.0 || ^2.0", "2.32.4", true, true},
		{"~=2.31", "2.32.4", true, true},
		{"^nope", "1.0", false, false},
	} {
		admits, known := admitsPoetry(tc.constraint, tc.version)
		if admits != tc.admits || known != tc.known {
			t.Errorf("admitsPoetry(%q, %s) = %v, %v; want %v, %v", tc.constraint, tc.version, admits, known, tc.admits, tc.known)
		}
	}
}

func TestPEP508AndNames(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"PyYAML": "pyyaml", "zope.interface": "zope-interface", "Foo__Bar-.baz": "foo-bar-baz", " requests ": "requests"} {
		if got := pyName(in); got != want {
			t.Errorf("pyName(%q) = %q, want %q", in, got, want)
		}
	}
	r, ok := parsePEP508(`Requests[socks, security] (>=2.0,<3) ; python_version < "3.13"`)
	if !ok || r.name != "requests" || len(r.extras) != 2 || r.spec != ">=2.0,<3" || !r.markers || r.url {
		t.Errorf("parsed %+v", r)
	}
	if r, _ := parsePEP508("pkg @ https://example.com/pkg.whl"); !r.url {
		t.Errorf("a URL requirement: %+v", r)
	}
	if v, ok := (pyRequirement{spec: "== 2.31.0"}).pin(); !ok || v != "2.31.0" {
		t.Errorf("pin = %q, %v", v, ok)
	}
	for _, spec := range []string{">=2.0", "==2.*", "==2.0,!=2.1", ""} {
		if _, ok := (pyRequirement{spec: spec}).pin(); ok {
			t.Errorf("%q read as a pin", spec)
		}
	}
}
