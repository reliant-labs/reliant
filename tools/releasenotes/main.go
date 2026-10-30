// Command releasenotes writes a release's notes, docs/data/releases/vX.Y.Z.yaml,
// from the pull requests merged since the previous release tag.
//
//	go run ./tools/releasenotes --version v1.8.12 --since v1.8.11 [--to origin/main]
//	    [--date YYYY-MM-DD] [--repo-url URL] [--out FILE] [--force]
//
// (`task generate:docs:changelog:draft VERSION=v1.8.12 SINCE_TAG=v1.8.11`.)
//
// main squash-merges, so every first-parent commit of main is one merged pull
// request and its subject is that PR's title. Those titles are conventional
// commits, and they are grouped the way forge's CHANGELOG is:
//
//	feat -> Added    fix -> Fixed    anything else -> Changed
//
// A title whose type carries `!` (`feat(x)!:`), or whose body has a
// `BREAKING CHANGE:` line, goes under "Breaking changes" instead. Release bumps
// (`chore: release v…`) and merge commits are skipped. Each entry is the title
// less its type, with its `(#N)` turned into a link to the pull request.
//
// The output is deterministic: the same history, version and date always give
// the same file, so the release workflow (control-plane
// scripts/release-chain/reliant-bump.sh) can open the version-bump PR without a
// person or a model writing prose. A person may still edit the file in that PR.
// An existing file is never overwritten without --force: hand-written notes win.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Release mirrors the subset of tools/docgen/changelog's schema a draft fills.
type Release struct {
	Version  string
	Date     string
	Title    string
	Summary  string
	Sections []Section
}

// Section is one conventional-commit group.
type Section struct {
	Title string
	Items []Item
}

// Item is one merged pull request.
type Item struct {
	Title       string
	Description string
}

// Commit is one first-parent commit of main: a squash-merged pull request.
type Commit struct {
	Subject string
	Body    string
}

// The section order, and the heading each group renders under.
const (
	sectionBreaking = "Breaking changes"
	sectionAdded    = "Added"
	sectionChanged  = "Changed"
	sectionFixed    = "Fixed"
)

var (
	conventionalRe = regexp.MustCompile(`^([A-Za-z]+)(\(([^)]+)\))?(!)?:\s+(.+)$`)
	prSuffixRe     = regexp.MustCompile(`\s*\(#([0-9]+)\)$`)
	breakingBodyRe = regexp.MustCompile(`(?m)^BREAKING[ -]CHANGE:`)
	versionRe      = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
)

// errNoChanges: nothing releasable since the previous tag, so there is nothing to write.
var errNoChanges = errors.New("no merged pull requests since the previous tag")

func main() {
	var (
		version = flag.String("version", "", "the release being written, vX.Y.Z (required)")
		since   = flag.String("since", "", "the previous release tag; its commits are excluded (required)")
		to      = flag.String("to", "HEAD", "the last commit the release contains")
		date    = flag.String("date", "", "the release date, YYYY-MM-DD (default: today, UTC)")
		repoURL = flag.String("repo-url", "https://github.com/reliant-labs/reliant", "where #N links point")
		out     = flag.String("out", "", "the file to write (default: docs/data/releases/<version>.yaml)")
		force   = flag.Bool("force", false, "overwrite an existing file")
	)
	flag.Parse()
	if err := run(*version, *since, *to, *date, *repoURL, *out, *force); err != nil {
		fmt.Fprintf(os.Stderr, "releasenotes: %v\n", err)
		os.Exit(1)
	}
}

func run(version, since, to, date, repoURL, out string, force bool) error {
	if !versionRe.MatchString(version) {
		return fmt.Errorf("--version %q is not vX.Y.Z", version)
	}
	if since == "" {
		return errors.New("--since is required (the previous release tag)")
	}
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	if out == "" {
		out = filepath.Join("docs", "data", "releases", version+".yaml")
	}
	if _, err := os.Stat(out); err == nil && !force {
		return fmt.Errorf("%s already exists; edit it, or pass --force to replace it", out)
	}
	commits, err := gitCommits(since, to)
	if err != nil {
		return err
	}
	rel, err := Draft(version, date, since, repoURL, commits)
	if err != nil {
		return fmt.Errorf("%s..%s: %w", since, to, err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, Render(rel), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d pull requests since %s)\n", out, rel.count(), since)
	return nil
}

// gitCommits lists the first-parent commits of since..to, oldest first.
func gitCommits(since, to string) ([]Commit, error) {
	cmd := exec.Command("git", "log", "--first-parent", "--no-merges", "--reverse",
		"--format=%s%x1f%b%x1e", since+".."+to)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log %s..%s: %v: %s", since, to, err, strings.TrimSpace(stderr.String()))
	}
	var commits []Commit
	for _, rec := range strings.Split(string(raw), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		subject, body, _ := strings.Cut(rec, "\x1f")
		commits = append(commits, Commit{Subject: strings.TrimSpace(subject), Body: body})
	}
	return commits, nil
}

// Draft groups commits into a release. It fails with errNoChanges when no
// commit is releasable.
func Draft(version, date, since, repoURL string, commits []Commit) (Release, error) {
	groups := map[string][]Item{}
	for _, c := range commits {
		subject := c.Subject
		if subject == "" || strings.HasPrefix(subject, "chore: release v") || strings.HasPrefix(subject, "Merge ") {
			continue
		}
		pr := ""
		if m := prSuffixRe.FindStringSubmatch(subject); m != nil {
			pr = m[1]
			subject = strings.TrimSpace(subject[:len(subject)-len(m[0])])
		}
		typ, scope, desc, breaking := "", "", subject, false
		if m := conventionalRe.FindStringSubmatch(subject); m != nil {
			typ, scope, breaking, desc = strings.ToLower(m[1]), m[3], m[4] == "!", m[5]
		}
		if breakingBodyRe.MatchString(c.Body) {
			breaking = true
		}
		title := desc
		if scope != "" {
			title = scope + ": " + desc
		}
		item := Item{Title: title}
		if pr != "" {
			item.Description = fmt.Sprintf("[#%s](%s/pull/%s)", pr, strings.TrimSuffix(repoURL, "/"), pr)
		}
		section := sectionChanged
		switch {
		case breaking:
			section = sectionBreaking
		case typ == "feat":
			section = sectionAdded
		case typ == "fix":
			section = sectionFixed
		}
		groups[section] = append(groups[section], item)
	}
	rel := Release{Version: version, Date: date}
	for _, s := range []string{sectionBreaking, sectionAdded, sectionChanged, sectionFixed} {
		if len(groups[s]) > 0 {
			rel.Sections = append(rel.Sections, Section{Title: s, Items: groups[s]})
		}
	}
	if len(rel.Sections) == 0 {
		return Release{}, errNoChanges
	}
	rel.Title = headline(groups)
	rel.Summary = fmt.Sprintf("Every pull request merged since %s, grouped by its conventional-commit type.", since)
	return rel, nil
}

// headline counts the groups: "2 new features, 5 fixes and 3 other changes".
func headline(groups map[string][]Item) string {
	var parts []string
	add := func(n int, one, many string) {
		switch {
		case n == 1:
			parts = append(parts, "1 "+one)
		case n > 1:
			parts = append(parts, fmt.Sprintf("%d %s", n, many))
		}
	}
	add(len(groups[sectionBreaking]), "breaking change", "breaking changes")
	add(len(groups[sectionAdded]), "new feature", "new features")
	add(len(groups[sectionFixed]), "fix", "fixes")
	add(len(groups[sectionChanged]), "other change", "other changes")
	switch len(parts) {
	case 1:
		return capitalize(parts[0])
	default:
		return capitalize(strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1])
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (r Release) count() int {
	n := 0
	for _, s := range r.Sections {
		n += len(s.Items)
	}
	return n
}

// Render writes the release in the style of the hand-written files: two-space
// indent, every scalar double-quoted. A JSON string is a valid YAML
// double-quoted scalar, so quoting is exact for any title.
func Render(r Release) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "version: %s\n", quote(r.Version))
	fmt.Fprintf(&b, "date: %s\n", quote(r.Date))
	fmt.Fprintf(&b, "title: %s\n", quote(r.Title))
	fmt.Fprintf(&b, "summary: %s\n", quote(r.Summary))
	b.WriteString("sections:\n")
	for _, s := range r.Sections {
		fmt.Fprintf(&b, "  - title: %s\n", quote(s.Title))
		b.WriteString("    items:\n")
		for _, it := range s.Items {
			fmt.Fprintf(&b, "      - title: %s\n", quote(it.Title))
			fmt.Fprintf(&b, "        description: %s\n", quote(it.Description))
		}
	}
	return b.Bytes()
}

func quote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}
