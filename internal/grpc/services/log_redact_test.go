package services

import "testing"

func TestRemoteURLForLog_StripsUserinfo(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://ghp_secretTOKEN123@github.com/acme/repo.git":      "https://github.com/acme/repo.git",
		"https://user:p%40ss@gitlab.example.com/group/project.git": "https://gitlab.example.com/group/project.git",
		"https://github.com/acme/repo.git":                         "https://github.com/acme/repo.git",
		"git@github.com:acme/repo.git":                             "git@github.com:acme/repo.git",
		"":                                                         "",
	}
	for in, want := range cases {
		if got := remoteURLForLog(in); got != want {
			t.Errorf("remoteURLForLog(%q) = %q, want %q", in, got, want)
		}
	}
}
