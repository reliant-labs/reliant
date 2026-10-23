package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMergeGitignore(t *testing.T) {
	const forge = "# forge header\nbin/\ngo.work\n\n!gen/go.mod\n.env.local\n"
	for _, tc := range []struct {
		name      string
		user      string
		wantAdded int
		wantLines []string // must appear, in the forge block or the user part
	}{
		{name: "empty user file", user: "", wantAdded: 4, wantLines: []string{"bin/", "go.work", "!gen/go.mod", ".env.local"}},
		{name: "user already has some", user: "# mine\n.env.local\nbin/\n", wantAdded: 2, wantLines: []string{"# mine", "go.work", "!gen/go.mod"}},
		{name: "no trailing newline", user: "node_modules", wantAdded: 4, wantLines: []string{"node_modules", "bin/"}},
		{name: "user covers everything but negations", user: "bin/\ngo.work\n.env.local\n!gen/go.mod\n", wantAdded: 1, wantLines: []string{"!gen/go.mod"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			once, added := mergeGitignore(tc.user, forge)
			if added != tc.wantAdded {
				t.Errorf("added = %d, want %d\n%s", added, tc.wantAdded, once)
			}
			userPart := strings.TrimRight(tc.user, "\n")
			if !strings.HasPrefix(once, userPart) {
				t.Errorf("user content not preserved as a prefix:\n%s", once)
			}
			for _, l := range tc.wantLines {
				if !strings.Contains(once, l+"\n") {
					t.Errorf("merged file lacks %q:\n%s", l, once)
				}
			}
			if strings.Count(once, forgeGitignoreBegin) != 1 || strings.Count(once, forgeGitignoreEnd) != 1 {
				t.Errorf("want exactly one forge block:\n%s", once)
			}
			twice, _ := mergeGitignore(once, forge)
			if twice != once {
				t.Errorf("not idempotent.\nonce:\n%s\ntwice:\n%s", once, twice)
			}
		})
	}

	t.Run("nothing missing writes no block", func(t *testing.T) {
		user := "bin/\ngo.work\n.env.local\n"
		got, added := mergeGitignore(user, "bin/\ngo.work\n")
		if got != user || added != 0 {
			t.Errorf("got %q (added %d), want the user file untouched", got, added)
		}
	})
}

func TestMergeScaffoldInto(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		name := "keep"
		if overwrite {
			name = "force"
		}
		t.Run(name, func(t *testing.T) {
			staging, target := t.TempDir(), t.TempDir()
			writeTestFile(t, filepath.Join(staging, "README.md"), "forge readme\n")
			writeTestFile(t, filepath.Join(staging, "go.mod"), "module forge\n")
			writeTestFile(t, filepath.Join(staging, "docs", "adr", "0001.md"), "forge adr\n")
			writeTestFile(t, filepath.Join(staging, ".gitignore"), "bin/\n")
			writeTestFile(t, filepath.Join(target, "README.md"), "user readme\n")
			writeTestFile(t, filepath.Join(target, "docs", "adr", "0001.md"), "user adr\n")
			writeTestFile(t, filepath.Join(target, ".gitignore"), ".env\n")

			res, err := mergeScaffoldInto(staging, target, overwrite)
			if err != nil {
				t.Fatal(err)
			}
			read := func(rel string) string {
				b, _ := os.ReadFile(filepath.Join(target, rel))
				return string(b)
			}
			if got := read("go.mod"); got != "module forge\n" {
				t.Errorf("new file go.mod = %q, want forge's", got)
			}
			if got := read(".gitignore"); !strings.HasPrefix(got, ".env\n") || !strings.Contains(got, "bin/\n") {
				t.Errorf(".gitignore not merged (force must not replace it either):\n%s", got)
			}
			collisions := []string{"README.md", "docs/adr/0001.md"}
			if overwrite {
				if !reflect.DeepEqual(res.overwritten, collisions) || len(res.kept) != 0 {
					t.Errorf("overwritten=%v kept=%v, want overwritten=%v", res.overwritten, res.kept, collisions)
				}
				if read("README.md") != "forge readme\n" {
					t.Errorf("--force did not replace README.md")
				}
				return
			}
			if !reflect.DeepEqual(res.kept, collisions) || len(res.overwritten) != 0 {
				t.Errorf("kept=%v overwritten=%v, want kept=%v", res.kept, res.overwritten, collisions)
			}
			if read("README.md") != "user readme\n" || read("docs/adr/0001.md") != "user adr\n" {
				t.Errorf("an existing file was overwritten without --force")
			}
			summary := strings.Join(inPlaceMergeSummary(res), "\n")
			if !strings.Contains(summary, "Kept your existing: README.md, docs/adr/0001.md") {
				t.Errorf("summary does not list the kept files:\n%s", summary)
			}
		})
	}
}

func TestDetectExistingFrontendApps(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "web", "package.json"), `{"dependencies":{"next":"16.0.0","react":"19"}}`)
	writeTestFile(t, filepath.Join(root, "frontend", "package.json"), `{"devDependencies":{"vite":"^6"}}`)
	writeTestFile(t, filepath.Join(root, "frontends", "admin", "package.json"), `{"dependencies":{"next":"15"}}`)
	writeTestFile(t, filepath.Join(root, "frontends", "lib", "package.json"), `{"dependencies":{"lodash":"4"}}`)

	apps := detectExistingFrontendApps(root)
	want := []existingFrontendApp{
		{name: "web", dir: "web", typ: "nextjs"},
		{name: "frontend", dir: "frontend", typ: "vite-spa"},
		{name: "admin", dir: "frontends/admin", typ: "nextjs"},
	}
	if !reflect.DeepEqual(apps, want) {
		t.Fatalf("detected %+v, want %+v", apps, want)
	}

	err := checkInPlaceFrontendCollisions(apps, []string{"web"})
	if err == nil {
		t.Fatal("--frontend web beside an existing web/ Next.js app was not refused")
	}
	for _, s := range []string{"web/ already holds a Next.js app", "Nothing was written", `path = "web"`, `type = "nextjs"`} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("refusal lacks %q:\n%s", s, err)
		}
	}
	if err := checkInPlaceFrontendCollisions(apps, []string{"dashboard"}); err != nil {
		t.Errorf("an unrelated frontend name was refused: %v", err)
	}
}

func TestDropTrackedIgnores(t *testing.T) {
	const body = "# Go\nbin/\ngo.work\ngo.work.sum\n\n/package.json\n!gen/go.mod\n/package.json\n"
	match := func(patterns []string) map[string][]string {
		for _, p := range patterns {
			if strings.HasPrefix(p, "!") {
				t.Errorf("a negation was offered to the matcher: %q", p)
			}
		}
		return map[string][]string{
			"go.work.sum":   {"go.work.sum"},
			"/package.json": {"package.json"},
		}
	}

	got, skipped := dropTrackedIgnores(body, match)
	want := "# Go\nbin/\ngo.work\n# forge left out \"go.work.sum\": this repository tracks go.work.sum\n\n" +
		"# forge left out \"/package.json\": this repository tracks package.json\n!gen/go.mod\n" +
		"# forge left out \"/package.json\": this repository tracks package.json\n"
	if got != want {
		t.Errorf("body:\n%s\nwant:\n%s", got, want)
	}
	wantSkipped := []skippedIgnore{
		{pattern: "go.work.sum", tracked: []string{"go.work.sum"}},
		{pattern: "/package.json", tracked: []string{"package.json"}},
	}
	if !reflect.DeepEqual(skipped, wantSkipped) {
		t.Errorf("skipped = %+v, want %+v", skipped, wantSkipped)
	}

	if got, skipped := dropTrackedIgnores(body, nil); got != body || skipped != nil {
		t.Errorf("outside a git work tree the body must be untouched; got %q, %v", got, skipped)
	}
}

func TestAnchorIgnorePattern(t *testing.T) {
	for _, tc := range []struct{ prefix, in, want string }{
		{"", "/package.json", "/package.json"},
		{"svc", "/package.json", "/svc/package.json"},
		{"svc", "/node_modules/", "/svc/node_modules/"},
		{"svc", "docs/adr", "/svc/docs/adr"},
		{"svc", "bin/", "bin/"},
		{"svc", "*.log", "*.log"},
		{"a/b", "/x", "/a/b/x"},
	} {
		if got := anchorIgnorePattern(tc.prefix, tc.in); got != tc.want {
			t.Errorf("anchorIgnorePattern(%q, %q) = %q, want %q", tc.prefix, tc.in, got, tc.want)
		}
	}
}

// reliant adopted forge in place and the merged .gitignore ignored the
// /package.json, /package-lock.json and go.work.sum it commits. An in-place
// merge must leave out every pattern git says matches a tracked file — at the
// repository root and in a subdirectory, where the merged .gitignore anchors
// to the subdirectory — and say so.
func TestMergeScaffoldInto_GitignoreSkipsTrackedPatterns(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds two git repositories and runs git's matcher per pattern (~4s); runs in task test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	const forgeIgnore = "# forge\n/package.json\n/package-lock.json\ngo.work.sum\nbin/\nnode_modules/\n/other.txt\n!keep.me\n"

	t.Run("repository root", func(t *testing.T) {
		repo := t.TempDir()
		initGitRepo(t, repo, map[string]string{
			"package.json":      "{}",
			"package-lock.json": "{}",
			"go.work.sum":       "",
			"bin/tool.sh":       "",
			".gitignore":        ".env\n",
		})
		staging := t.TempDir()
		writeTestFile(t, filepath.Join(staging, ".gitignore"), forgeIgnore)

		res, err := mergeScaffoldInto(staging, repo, false)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
		block := string(got)[strings.Index(string(got), forgeGitignoreBegin):]
		for _, want := range []string{"node_modules/\n", "/other.txt\n", "!keep.me\n"} {
			if !strings.Contains(block, want) {
				t.Errorf("an untracked-only pattern %q was dropped:\n%s", want, got)
			}
		}
		for _, notWant := range []string{"/package.json\n", "/package-lock.json\n", "go.work.sum\n", "bin/\n"} {
			if strings.Contains(block, notWant) {
				t.Errorf("merged a pattern that matches a tracked file, %q:\n%s", notWant, got)
			}
		}
		var patterns []string
		for _, s := range res.gitignoreSkipped {
			patterns = append(patterns, s.pattern)
		}
		if want := []string{"/package.json", "/package-lock.json", "go.work.sum", "bin/"}; !reflect.DeepEqual(patterns, want) {
			t.Errorf("skipped %v, want %v", patterns, want)
		}
		summary := strings.Join(inPlaceMergeSummary(res), "\n")
		for _, want := range []string{
			"Left 4 forge .gitignore pattern(s) out because they match files this repository tracks",
			"/package.json  (tracks package.json)",
			"bin/  (tracks bin/tool.sh)",
		} {
			if !strings.Contains(summary, want) {
				t.Errorf("summary lacks %q:\n%s", want, summary)
			}
		}

		// Idempotent: a re-run neither re-adds nor duplicates anything.
		res2, err := mergeScaffoldInto(staging, repo, false)
		if err != nil {
			t.Fatal(err)
		}
		again, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
		if string(again) != string(got) || len(res2.gitignoreSkipped) != 4 {
			t.Errorf("re-run changed the merge (skipped %d):\n%s", len(res2.gitignoreSkipped), again)
		}
	})

	t.Run("subdirectory with no .gitignore", func(t *testing.T) {
		repo := t.TempDir()
		initGitRepo(t, repo, map[string]string{
			"other.txt":        "", // tracked, but at the repo root: /other.txt in svc/.gitignore does not match it
			"svc/package.json": "{}",
			"svc/main.go":      "package main\n",
		})
		target := filepath.Join(repo, "svc")
		staging := t.TempDir()
		writeTestFile(t, filepath.Join(staging, ".gitignore"), forgeIgnore)

		res, err := mergeScaffoldInto(staging, target, false)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(filepath.Join(target, ".gitignore"))
		if !strings.Contains(string(got), "# forge left out \"/package.json\": this repository tracks package.json\n") {
			t.Errorf("a fresh .gitignore must replace the tracked pattern with a note:\n%s", got)
		}
		if !strings.Contains(string(got), "\n/other.txt\n") {
			t.Errorf("/other.txt is anchored to svc/, where nothing matches it, so it must stay:\n%s", got)
		}
		if len(res.gitignoreSkipped) != 1 || res.gitignoreSkipped[0].pattern != "/package.json" {
			t.Errorf("skipped = %+v, want only /package.json", res.gitignoreSkipped)
		}
	})
}

func TestDirHoldsProject(t *testing.T) {
	empty := t.TempDir()
	if dirHoldsProject(empty) {
		t.Error("an empty directory is not a project")
	}
	gitOnly := t.TempDir()
	writeTestFile(t, filepath.Join(gitOnly, ".git", "HEAD"), "ref: refs/heads/main\n")
	if dirHoldsProject(gitOnly) {
		t.Error("a bare `git init` is not a project")
	}
	repo := t.TempDir()
	writeTestFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeTestFile(t, filepath.Join(repo, "go.mod"), "module example.com/x\n")
	if !dirHoldsProject(repo) {
		t.Error("a directory with content beside .git is an existing project")
	}
	if dirHoldsProject(filepath.Join(empty, "missing")) {
		t.Error("a missing directory is not a project")
	}
}
