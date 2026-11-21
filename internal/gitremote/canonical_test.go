package gitremote

import "testing"

func TestCanonical(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://tok@GitHub.com/acme/Shop.git": "github.com/acme/Shop",
		"git@github.com:acme/shop.git":         "github.com/acme/shop",
		"ssh://git@github.com:22/acme/shop/":   "github.com/acme/shop",
		"github.com/acme/shop":                 "github.com/acme/shop",
		"http://localhost:3000/o/r.git":        "localhost/o/r",
		"":                                     "",
		"/home/me/shop":                        "",
		"./shop":                               "",
		"shop":                                 "",
	}
	for in, want := range cases {
		if got := Canonical(in); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeSubpath(t *testing.T) {
	t.Parallel()
	cases := map[string]string{"": "", ".": "", "./": "", "/apps/web/": "apps/web", `.\apps\web`: "apps/web", "././a": "a"}
	for in, want := range cases {
		if got := NormalizeSubpath(in); got != want {
			t.Errorf("NormalizeSubpath(%q) = %q, want %q", in, got, want)
		}
	}
}
