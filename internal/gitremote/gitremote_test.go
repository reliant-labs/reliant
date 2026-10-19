package gitremote

import "testing"

func TestRedacted_StripsUserinfo(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://ghp_secretTOKEN123@github.com/acme/repo.git":      "https://github.com/acme/repo.git",
		"https://user:p%40ss@gitlab.example.com/group/project.git": "https://gitlab.example.com/group/project.git",
		"https://github.com/acme/repo.git":                         "https://github.com/acme/repo.git",
		"git@github.com:acme/repo.git":                             "git@github.com:acme/repo.git",
		"":                                                         "",
	}
	for in, want := range cases {
		if got := Redacted(in); got != want {
			t.Errorf("Redacted(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripCredentials(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, in, want string }{
		{"x-access-token", "https://x-access-token:ghs_SECRET@github.com/acme/repo.git", "https://github.com/acme/repo.git"},
		{"oauth2", "https://oauth2:glpat-SECRET@gitlab.com/g/p.git", "https://gitlab.com/g/p.git"},
		{"bare token", "https://ghp_SECRET@github.com/acme/repo.git", "https://github.com/acme/repo.git"},
		{"user only", "https://alice@github.com/acme/repo.git", "https://github.com/acme/repo.git"},
		{"encoded password", "https://user:p%40ss%2Fword@host.example/o/r", "https://host.example/o/r"},
		{"@ in password", "https://u:p@ss@host.example/o/r", "https://host.example/o/r"},
		{"port kept", "https://u:t@git.example.com:8443/o/r.git", "https://git.example.com:8443/o/r.git"},
		{"upper scheme", "HTTPS://u:t@github.com/o/r", "HTTPS://github.com/o/r"},
		{"http", "http://u:t@localhost:3000/o/r.git", "http://localhost:3000/o/r.git"},
		{"git protocol", "git://u:t@host/o/r.git", "git://host/o/r.git"},
		{"no path", "https://u:t@github.com", "https://github.com"},
		{"query after authority", "https://u:t@github.com?x=a@b", "https://github.com?x=a@b"},
		{"@ in path is not userinfo", "https://github.com/o/r@v1", "https://github.com/o/r@v1"},
		{"unparseable by net/url", "https://u:to ken@github.com/o/r", "https://github.com/o/r"},
		{"no userinfo", "https://github.com/acme/repo.git", "https://github.com/acme/repo.git"},
		{"scp style", "git@github.com:acme/repo.git", "git@github.com:acme/repo.git"},
		{"ssh user kept", "ssh://git@github.com/acme/repo.git", "ssh://git@github.com/acme/repo.git"},
		{"ssh password stripped", "ssh://git:hunter2@host.example:2222/o/r.git", "ssh://git@host.example:2222/o/r.git"},
		{"git+ssh password stripped", "git+ssh://git:hunter2@host/o/r.git", "git+ssh://git@host/o/r.git"},
		{"file url", "file:///srv/git/repo.git", "file:///srv/git/repo.git"},
		{"absolute path", "/home/me/repos/project", "/home/me/repos/project"},
		{"windows path", `C:\Users\me\repo`, `C:\Users\me\repo`},
		{"relative path", "../sibling", "../sibling"},
		{"path with ://", "/tmp/a://b@c/d", "/tmp/a://b@c/d"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := StripCredentials(tc.in)
			if got != tc.want {
				t.Errorf("StripCredentials(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if again := StripCredentials(got); again != got {
				t.Errorf("not idempotent: %q -> %q", got, again)
			}
			if HasCredentials(tc.in) != (got != tc.in) {
				t.Errorf("HasCredentials(%q) disagrees with StripCredentials", tc.in)
			}
		})
	}
}

func TestStripCredentialsPtr(t *testing.T) {
	t.Parallel()
	if StripCredentialsPtr(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	clean := "https://github.com/o/r.git"
	if got := StripCredentialsPtr(&clean); got != &clean {
		t.Fatal("an already-clean value must come back as the same pointer")
	}
	dirty := "https://x-access-token:T@github.com/o/r.git"
	got := StripCredentialsPtr(&dirty)
	if got == nil || *got != clean {
		t.Fatalf("got %v", got)
	}
	if dirty != "https://x-access-token:T@github.com/o/r.git" {
		t.Fatal("the caller's value must not be mutated")
	}
}
