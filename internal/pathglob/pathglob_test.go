package pathglob

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, rel string
		want         bool
	}{
		{"frontends/web/**", "frontends/web/src/app.tsx", true},
		{"frontends/web/**", "frontends/web", true},
		{"frontends/web/**", "frontends/webby/x.ts", false},
		{"frontends/web/", "frontends/web/src/app.tsx", true},
		{"frontends/web", "frontends/web/src/app.tsx", true},
		{"frontends/web", "frontends/web", true},
		{".github/workflows/deploy.yml", ".github/workflows/deploy.yml", true},
		{".github/workflows/*.yml", ".github/workflows/deploy.yml", true},
		{".github/workflows/*.yml", ".github/workflows/sub/deploy.yml", false},
		{"**/*.gen.ts", "frontends/web/src/a.gen.ts", true},
		{"**/*.gen.ts", "a.gen.ts", true},
		{"internal/**/mock_gen.go", "internal/a/b/mock_gen.go", true},
		{"internal/**/mock_gen.go", "internal/mock_gen.go", true},
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
		{"./docs/**", "docs/x.md", true},
		{"[", "[", false}, // invalid: matches nothing
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.rel); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.rel, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	for _, bad := range []string{"", "  ", "/abs/**", `a\b`, "a//b", "../x", "a/./b", "a**", "dir/a**b/c", "[", "a/[b"} {
		if Validate(bad) == nil {
			t.Errorf("Validate(%q) = nil, want an error", bad)
		}
	}
	for _, good := range []string{"a", "a/", "a/**", "**/x.go", "a/*/b", "a/[bc]/d", ".github/workflows/deploy.yml"} {
		if err := Validate(good); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", good, err)
		}
	}
}

func TestStaticPrefix(t *testing.T) {
	for pattern, want := range map[string]string{
		"frontends/web/**":    "frontends/web",
		"frontends/*/src/gen": "frontends",
		"**/*.go":             "",
		"a/b.txt":             "a/b.txt",
		"a/":                  "a",
	} {
		if got := StaticPrefix(pattern); got != want {
			t.Errorf("StaticPrefix(%q) = %q, want %q", pattern, got, want)
		}
	}
}
