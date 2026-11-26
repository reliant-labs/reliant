package deploytarget

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Cache-Control for a bucket deploy.
//
// forge decides every object's header HERE, in Go, from the assembled tree,
// and gcloud only applies it. An earlier version handed each rule's glob to
// `gcloud storage cp` as a source wildcard, but a wildcard source copies the
// matched files FLAT: `_next/static/**` uploaded `_next/static/chunks/a.js`
// as `a.js`, and the catch-all pass then uploaded the real path with no
// header at all. So the rules never reached the objects they named.
//
// The upload is one recursive `cp` per distinct header, each run from a tree
// of hard links holding exactly that header's files at their site-relative
// paths. A recursive copy of a directory keeps paths, so every object lands
// where it belongs with the header its rule gave it.

// cacheRules resolves the rules a deploy applies, first match wins.
//
// Declared rules replace the default entirely, as on OnFirebase. With none
// declared, the default is the one OnFirebase and hosted static hosting
// serve: the build's asset directory is immutable for a year (a new build
// emits new names), and everything else, above all the entry documents,
// config.js and version.json, is no-cache. Without it GCS serves a public
// object as `max-age=3600`, so a deploy, or a promotion that changed only
// config.js, stays invisible for an hour after the CDN purge.
func (s StaticSiteSpec) cacheRules() []CacheRuleSpec {
	if len(s.CacheControl) > 0 {
		return s.CacheControl
	}
	var rules []CacheRuleSpec
	if a := siteAssetRel(s.BasePath, s.AssetDir); a != "" {
		rules = append(rules, CacheRuleSpec{Pattern: a + "/**", CacheControl: cacheImmutable})
	}
	return append(rules, CacheRuleSpec{Pattern: "**", CacheControl: cacheRevalidate})
}

// cacheUploadGroup is the objects one upload pass sends, all with one
// Cache-Control header. An empty CacheControl is the objects no declared
// rule matched: they are uploaded without one.
type cacheUploadGroup struct {
	CacheControl string
	Files        []string // site-relative slash paths, sorted
}

// cacheUploadGroups assigns each file the header of the first rule that
// matches it and groups the files by header, in the order the rules first
// produce each header. Files no rule matches come last, headerless.
func cacheUploadGroups(rules []CacheRuleSpec, files []string) ([]cacheUploadGroup, error) {
	var groups []cacheUploadGroup
	index := map[string]int{}
	var unmatched []string
	for _, f := range files {
		header, matched := "", false
		for _, r := range rules {
			ok, err := cacheGlobMatch(r.Pattern, f)
			if err != nil {
				return nil, fmt.Errorf("cache_control pattern %q: %w", r.Pattern, err)
			}
			if ok {
				header, matched = r.CacheControl, true
				break
			}
		}
		if !matched {
			unmatched = append(unmatched, f)
			continue
		}
		i, seen := index[header]
		if !seen {
			i = len(groups)
			index[header] = i
			groups = append(groups, cacheUploadGroup{CacheControl: header})
		}
		groups[i].Files = append(groups[i].Files, f)
	}
	// Order groups by the first rule that produces their header, so the
	// plan reads in the order the author declared.
	order := map[string]int{}
	for i, r := range rules {
		if _, ok := order[r.CacheControl]; !ok {
			order[r.CacheControl] = i
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return order[groups[i].CacheControl] < order[groups[j].CacheControl]
	})
	if len(unmatched) > 0 {
		groups = append(groups, cacheUploadGroup{Files: unmatched})
	}
	return groups, nil
}

// cacheGlobMatch reports whether a site-relative slash path matches a
// cache rule's glob. Segments match with path.Match (`*`, `?`, `[...]`, none
// crossing a "/"); a `**` segment matches any number of segments, zero
// included. A leading "/" on the pattern is tolerated.
func cacheGlobMatch(pattern, name string) (bool, error) {
	pat := strings.Split(strings.Trim(pattern, "/"), "/")
	for _, seg := range pat {
		if _, err := path.Match(seg, ""); err != nil {
			return false, err
		}
	}
	return matchSegments(pat, strings.Split(name, "/")), nil
}

func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(name); i++ {
				if matchSegments(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], name[0]); !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// stagedFiles lists every regular file in the assembled tree as a sorted
// site-relative slash path.
func stagedFiles(stagingDir string) ([]string, error) {
	var files []string
	err := filepath.Walk(stagingDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(stagingDir, p)
		if rerr != nil {
			return rerr
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list staging tree: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

// uploadCmd is the one upload pass for a tree whose every file takes the
// same header: a recursive copy of the cwd into the release prefix.
func uploadCmd(cacheControl, dst string) []string {
	argv := []string{"gcloud", "storage", "cp", "--recursive"}
	if cacheControl != "" {
		argv = append(argv, "--cache-control="+cacheControl)
	}
	return append(argv, ".", dst)
}

// uploadRelease uploads the assembled tree into its immutable release
// prefix, each object carrying the Cache-Control its rule assigns. A tree
// whose files all take one header is uploaded straight from the staging
// dir; otherwise each header's files are hard-linked (copied where the
// filesystem refuses a link) into a sibling tree uploaded on its own.
func uploadRelease(ctx context.Context, runner commandRunner, spec StaticSiteSpec, stagingDir, digest string) error {
	files, err := stagedFiles(stagingDir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("the assembled tree %s is empty", stagingDir)
	}
	groups, err := cacheUploadGroups(spec.cacheRules(), files)
	if err != nil {
		return err
	}
	dst := spec.releaseURI(digest)
	if len(groups) == 1 {
		return runInDir(ctx, runner, stagingDir, nil, uploadCmd(groups[0].CacheControl, dst))
	}
	for _, g := range groups {
		if err := uploadGroup(ctx, runner, stagingDir, g, dst); err != nil {
			return err
		}
	}
	return nil
}

func uploadGroup(ctx context.Context, runner commandRunner, stagingDir string, g cacheUploadGroup, dst string) error {
	dir, err := os.MkdirTemp(filepath.Dir(stagingDir), "forge-static-site-upload-")
	if err != nil {
		return fmt.Errorf("upload tree: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for _, rel := range g.Files {
		src := filepath.Join(stagingDir, filepath.FromSlash(rel))
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("upload tree: %w", err)
		}
		if os.Link(src, target) == nil {
			continue
		}
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("upload tree: %w", err)
		}
		if err := copyFile(src, target, info.Mode()); err != nil {
			return fmt.Errorf("upload tree: %w", err)
		}
	}
	return runInDir(ctx, runner, dir, nil, uploadCmd(g.CacheControl, dst))
}

// printCachePlan renders the dry-run's upload lines. The files are not
// known until the build runs, so it prints the rules each object is
// matched against and the command shape each header's pass runs.
func printCachePlan(w io.Writer, spec StaticSiteSpec, digest string) {
	rules := spec.cacheRules()
	source := "declared cache_control"
	if len(spec.CacheControl) == 0 {
		source = "default"
	}
	_, _ = fmt.Fprintf(w, "    cache:        %s, first match wins\n", source)
	catchAll := false
	for _, r := range rules {
		_, _ = fmt.Fprintf(w, "                    %-24s %s\n", r.Pattern, r.CacheControl)
		if strings.Trim(r.Pattern, "/") == "**" {
			catchAll = true
		}
	}
	if !catchAll {
		_, _ = fmt.Fprintf(w, "                    %-24s (no Cache-Control header)\n", "(no rule matched)")
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if seen[r.CacheControl] {
			continue
		}
		seen[r.CacheControl] = true
		_, _ = fmt.Fprintf(w, "    [DRY-RUN] would exec (cwd: the files taking this header): %s\n",
			strings.Join(uploadCmd(r.CacheControl, spec.releaseURI(digest)), " "))
	}
	if !catchAll {
		_, _ = fmt.Fprintf(w, "    [DRY-RUN] would exec (cwd: the files no rule matched): %s\n",
			strings.Join(uploadCmd("", spec.releaseURI(digest)), " "))
	}
}
