package gitremote

import "testing"

func TestTokenCloneURL(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, in, want string }{
		{"scp form", "git@github.com:reliant-labs/control-plane.git", "https://github.com/reliant-labs/control-plane.git"},
		{"scp form without .git", "git@github.com:acme/shop", "https://github.com/acme/shop.git"},
		{"scp form, host case", "git@GitHub.com:acme/shop.git", "https://github.com/acme/shop.git"},
		{"ssh url", "ssh://git@github.com/acme/shop.git", "https://github.com/acme/shop.git"},
		{"ssh url with port", "ssh://git@github.com:22/acme/shop", "https://github.com/acme/shop.git"},
		{"git+ssh url", "git+ssh://git@github.com/acme/shop.git", "https://github.com/acme/shop.git"},
		{"https kept", "https://github.com/acme/shop", "https://github.com/acme/shop"},
		{"another host's ssh kept", "git@gitlab.com:acme/shop.git", "git@gitlab.com:acme/shop.git"},
		{"enterprise host kept", "git@github.example.com:acme/shop.git", "git@github.example.com:acme/shop.git"},
		{"lookalike host kept", "git@notgithub.com:acme/shop.git", "git@notgithub.com:acme/shop.git"},
		{"not owner/repo kept", "git@github.com:acme", "git@github.com:acme"},
		{"absolute path kept", "/srv/git/shop", "/srv/git/shop"},
		{"windows path kept", `C:\src\shop`, `C:\src\shop`},
		{"relative path with colon kept", "./a:b/c", "./a:b/c"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := TokenCloneURL(tc.in); got != tc.want {
				t.Errorf("TokenCloneURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
