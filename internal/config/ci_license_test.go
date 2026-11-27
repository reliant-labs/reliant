package config

import (
	"strings"
	"testing"
	"time"
)

const licenseHead = "name: demo\nmodule_path: github.com/example/demo\nci:\n  license:\n"

func TestCILicense_Loads(t *testing.T) {
	in := licenseHead +
		"    allowed: [Apache-2.0, MIT]\n" +
		"    exceptions:\n" +
		"      - module: github.com/hashicorp/go-version\n" +
		"        reason: MPL-2.0, imported unmodified\n" +
		"        expires: 2027-06-01\n" +
		"      - module: github.com/foo/bar\n" +
		"        reason: dual-licensed MIT/GPL; we use it under MIT\n"
	cfg, err := LoadProject([]byte(in), "forge.yaml")
	if err != nil {
		t.Fatalf("expected clean load, got: %v", err)
	}
	lic := cfg.CI.License
	if strings.Join(lic.Allowed, ",") != "Apache-2.0,MIT" || len(lic.Exceptions) != 2 {
		t.Fatalf("ci.license = %+v", lic)
	}
	// An unquoted date is still the string the user wrote.
	if got := lic.Exceptions[0].Expires; got != "2027-06-01" {
		t.Errorf("expires = %q", got)
	}
	if _, ok := lic.Exceptions[1].ExpiresAt(); ok {
		t.Error("an entry with no expires has no expiry")
	}
}

func TestCILicense_LoadErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		line             string
	}{
		{"empty reason",
			"    exceptions:\n      - module: github.com/foo/bar\n        reason: \"  \"\n",
			"ci.license.exceptions[0] (github.com/foo/bar) has no `reason`", "forge.yaml:7"},
		{"no reason key",
			"    exceptions:\n      - module: github.com/foo/bar\n",
			"has no `reason`", "forge.yaml:6"},
		{"malformed expires",
			"    exceptions:\n      - module: github.com/foo/bar\n        reason: r\n        expires: next june\n",
			"unparseable `expires: next june`", "forge.yaml:8"},
		{"duplicate module",
			"    exceptions:\n      - module: github.com/foo/bar\n        reason: r\n      - module: github.com/foo/bar\n        reason: r2\n",
			"ci.license.exceptions[1] exempts github.com/foo/bar, which ci.license.exceptions[0] already exempts", "forge.yaml:8"},
		{"no module",
			"    exceptions:\n      - reason: r\n",
			"ci.license.exceptions[0] has no `module`", "forge.yaml:6"},
		{"a license is not a module",
			"    exceptions:\n      - module: MPL 2.0\n        reason: r\n",
			"is not a module path", "forge.yaml:6"},
		{"unknown exception key",
			"    exceptions:\n      - module: github.com/foo/bar\n        reason: r\n        expiry: 2027-01-01\n",
			`unknown key "ci.license.exceptions[0].expiry" — did you mean "expires"?`, "forge.yaml:8"},
		{"unknown license key",
			"    allow: [MIT]\n",
			`unknown key "ci.license.allow"`, "forge.yaml:5"},
		{"two licenses in one entry",
			"    allowed: [\"MIT, Apache-2.0\"]\n",
			`ci.license.allowed[0] "MIT, Apache-2.0" is not one license name`, "forge.yaml:5"},
		{"duplicate license",
			"    allowed:\n      - MIT\n      - MIT\n",
			`ci.license.allowed lists "MIT" twice`, "forge.yaml:7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadProject([]byte(licenseHead+tc.body), "forge.yaml")
			if err == nil {
				t.Fatal("loaded clean; want a load error")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), tc.line) {
				t.Errorf("err = %v\nwant %q at %s", err, tc.want, tc.line)
			}
		})
	}
}

// Expiry is end of day, the ci.vuln_scan.exemptions rule.
func TestCILicenseException_Expired(t *testing.T) {
	ex := CILicenseException{Module: "m", Reason: "r", Expires: "2026-11-25"}
	day := func(s string) time.Time {
		tm, _ := time.Parse(time.DateTime, s)
		return tm
	}
	if ex.Expired(day("2026-11-25 23:59:00")) {
		t.Error("an entry is live through its expiry date")
	}
	if !ex.Expired(day("2026-11-26 00:00:01")) {
		t.Error("an entry lapses the day after its expiry date")
	}
	if (CILicenseException{Module: "m", Reason: "r"}).Expired(day("2999-01-01 00:00:00")) {
		t.Error("an entry with no expires never lapses")
	}
}
