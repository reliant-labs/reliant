// Copyright (c) 2026 Reliant Labs

package githubrepo

import "testing"

func TestOf(t *testing.T) {
	for raw, want := range map[string]string{
		"https://github.com/Acme/Shop.git":         "acme/shop",
		"https://x-access-token:t@github.com/a/b/": "a/b",
		"https://github.com:443/a/b":               "a/b",
		"ssh://git@github.com:22/a/b.git":          "a/b",
		"git@github.com:a/b.git":                   "a/b",
		"  git@GitHub.com:a/b  ":                   "a/b",
		"git@gitlab.com:a/b.git":                   "",
		"https://github.example.com/a/b":           "",
		"ssh://deploy@github.com/a/b":              "",
		"https://github.com/a":                     "",
		"https://github.com/.a/b":                  "",
		"https://github.com/a/..":                  "",
		"/home/me/shop":                            "",
		"":                                         "",
	} {
		owner, repo, ok := Of(raw)
		got := ""
		if ok {
			got = owner + "/" + repo
		}
		if got != want {
			t.Errorf("Of(%q) = %q, want %q", raw, got, want)
		}
	}
}
