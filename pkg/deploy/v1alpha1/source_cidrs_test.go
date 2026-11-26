package v1alpha1

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func portWithCIDRs(expose bool, cidrs ...string) WorkloadSpec {
	return WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "http", Port: 8080, Expose: expose, AllowedSourceCIDRs: cidrs}}}
}

func TestAllowedSourceCIDRsValidation(t *testing.T) {
	many := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("10.%d.%d.0/24", i/256, i%256)
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		spec    WorkloadSpec
		wantErr string // "" means valid
	}{
		{"none is unrestricted", portWithCIDRs(true), ""},
		{"v4 cidr", portWithCIDRs(true, "192.0.2.0/24"), ""},
		{"v6 cidr", portWithCIDRs(true, "2001:db8::/32"), ""},
		{"bare v4", portWithCIDRs(true, "188.34.190.188"), ""},
		{"bare v6", portWithCIDRs(true, "2001:db8::1"), ""},
		{"five importer addresses", portWithCIDRs(true, "188.34.190.188", "65.108.48.77", "37.24.226.100", "89.246.73.244", "85.115.25.67"), ""},
		{"mixed families", portWithCIDRs(true, "192.0.2.0/24", "2001:db8::/32"), ""},
		{"host bits are masked, not refused", portWithCIDRs(true, "10.0.0.5/24"), ""},
		{"exactly the maximum", portWithCIDRs(true, many(MaxSourceCIDRs)...), ""},

		{"not an address", portWithCIDRs(true, "chrono24"), "is not an IPv4 or IPv6 CIDR"},
		{"hostname", portWithCIDRs(true, "example.com"), "is not an IPv4 or IPv6 CIDR"},
		{"empty entry", portWithCIDRs(true, ""), "is not an IPv4 or IPv6 CIDR"},
		{"prefix too long", portWithCIDRs(true, "10.0.0.0/33"), "is not an IPv4 or IPv6 CIDR"},
		{"octet out of range", portWithCIDRs(true, "10.0.0.256"), "is not an IPv4 or IPv6 CIDR"},
		{"v4-mapped v6", portWithCIDRs(true, "::ffff:192.0.2.1"), "IPv4-mapped"},
		{"zoned v6", portWithCIDRs(true, "fe80::1%eth0"), "zone"},
		{"v4 allow-all", portWithCIDRs(true, "0.0.0.0/0"), "allows every client"},
		{"v6 allow-all", portWithCIDRs(true, "::/0"), "allows every client"},
		{"duplicate", portWithCIDRs(true, "192.0.2.0/24", "192.0.2.0/24"), "lists 192.0.2.0/24 twice"},
		{"duplicate after bare-ip normalization", portWithCIDRs(true, "192.0.2.7", "192.0.2.7/32"), "lists 192.0.2.7/32 twice"},
		{"duplicate after masking", portWithCIDRs(true, "10.0.0.5/24", "10.0.0.0/24"), "lists 10.0.0.0/24 twice"},
		{"too many", portWithCIDRs(true, many(MaxSourceCIDRs+1)...), "at most 64 are allowed"},
		{"unexposed port", portWithCIDRs(false, "192.0.2.0/24"), "only meaningful on the exposed port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate(ProfileFull)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// A sidecar's ports are container ports: there is no ingress to filter.
func TestAllowedSourceCIDRsRefusedOnSidecarPort(t *testing.T) {
	s := svc()
	s.Sidecars = []Container{{Name: "proxy", Image: pinnedImage, Ports: []Port{{Name: "admin", Port: 9000, AllowedSourceCIDRs: []string{"192.0.2.1"}}}}}
	err := s.Validate(ProfileFull)
	if err == nil || !strings.Contains(err.Error(), "allowedSourceCIDRs belong on the workload's own port") {
		t.Fatalf("Validate = %v", err)
	}
}

// The hosted control plane re-runs Validate(ProfileRestricted) at admission,
// so the field must be allowed there and the mask must classify it.
func TestAllowedSourceCIDRsIsAllowedUnderRestricted(t *testing.T) {
	mustPass(t, portWithCIDRs(true, "188.34.190.188"), ProfileRestricted)
	if FieldProfiles["ports.allowedSourceCIDRs"] != ProfileRestricted {
		t.Errorf("FieldProfiles[ports.allowedSourceCIDRs] = %s, want restricted", FieldProfiles["ports.allowedSourceCIDRs"])
	}
}

func TestNormalizeSourceCIDR(t *testing.T) {
	for in, want := range map[string]string{
		"188.34.190.188":   "188.34.190.188/32",
		"2001:DB8::1":      "2001:db8::1/128",
		"10.0.0.5/24":      "10.0.0.0/24",
		"2001:db8::1/32":   "2001:db8::/32",
		" 192.0.2.0/24 ":   "192.0.2.0/24",
		"192.0.2.0/24":     "192.0.2.0/24",
		"2001:db8::/64":    "2001:db8::/64",
		"203.0.113.9/32":   "203.0.113.9/32",
		"203.0.113.9/31":   "203.0.113.8/31",
		"0.0.0.0/1":        "0.0.0.0/1",
		"2001:db8:0::1/48": "2001:db8::/48",
	} {
		got, err := NormalizeSourceCIDR(in)
		if err != nil || got != want {
			t.Errorf("NormalizeSourceCIDR(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

// WithDefaults stores the canonical form, so a bare address reaches the
// renderer and the control plane as one spelling. Entries that do not
// normalize are left for Validate to refuse.
func TestWithDefaultsCanonicalizesSourceCIDRs(t *testing.T) {
	in := portWithCIDRs(true, "188.34.190.188", "10.0.0.5/24", "not-an-ip")
	got := in.WithDefaults().Ports[0].AllowedSourceCIDRs
	want := []string{"188.34.190.188/32", "10.0.0.0/24", "not-an-ip"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WithDefaults AllowedSourceCIDRs = %v, want %v", got, want)
	}
	if in.Ports[0].AllowedSourceCIDRs[0] != "188.34.190.188" {
		t.Errorf("WithDefaults edited its receiver's slice: %v", in.Ports[0].AllowedSourceCIDRs)
	}
}
