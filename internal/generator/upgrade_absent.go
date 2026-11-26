package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
)

// Which absent scaffold-once file `forge project upgrade` may write.
//
// An absent Tier-2 file used to be written whenever the birth ledger
// (.forge/scaffolded/) did not record it. The ledger is younger than most
// projects, so "not recorded" covered every file a project deleted before it
// existed, and every file it never had because it uses another one. Bumping
// reliant's pin wrote docker-compose.yml, which reliant#1539 had deleted, and
// frontends/web/eslint.config.mjs beside the frontend's own eslint.config.js;
// reliant had to `forge project disown --absent` eight files to keep them out.
//
// An unrecorded absence is not evidence that the project wants the file. So
// upgrade now writes one only when the file is NEW across the upgrade's range
// (its template first shipped in a release after the project's old pin and no
// later than the target) and the project has nothing that stands in for it.
// Everything else it leaves absent:
//
//   - recorded in the ledger, or disowned: the project said no (LeftDeleted);
//   - deleted in git history: the project said no before the ledger could
//     hear it (LeftDeleted, DeletedIn);
//   - a sibling variant exists (eslint.config.js for eslint.config.mjs,
//     compose.yaml for docker-compose.yml): the project has its own
//     (Equivalent);
//   - otherwise, a template the project simply never had: offered, never
//     written (Offered). `forge project rescaffold <path>` adds it.
//
// Naming the path after --force is the adoption gesture and writes it.

// UpgradeOptions is one UpgradeWith run's input.
type UpgradeOptions struct {
	// Force names the user-modified files the run may overwrite, and the
	// absent scaffold-once files it may add.
	Force ForceSelection
	// CheckOnly writes nothing and reports what would change.
	CheckOnly bool
	// NewSince reports whether a template that first shipped in release v
	// (a managed entry's since) is new across this upgrade: v is after the
	// project's old pin and the target reaches it. nil means nothing is new,
	// so an absent scaffold-once file is offered and never written.
	NewSince func(v string) bool
}

// absentScaffold decides an absent Tier-2 file. ok is false when upgrade may
// write it; otherwise res is the skip to report.
func absentScaffold(projectDir string, f managedFile, opts UpgradeOptions, deleted map[string]string) (res UpgradeResult, ok bool) {
	if !checksums.ScaffoldMayBeBorn(projectDir, f.destPath) {
		return UpgradeResult{Path: f.destPath, Status: UpgradeSkipped, LeftDeleted: true}, true
	}
	if opts.Force.Names(f.destPath) {
		return UpgradeResult{}, false
	}
	if sha, gone := deleted[filepath.ToSlash(f.destPath)]; gone {
		return UpgradeResult{Path: f.destPath, Status: UpgradeSkipped, LeftDeleted: true, DeletedIn: sha}, true
	}
	if eq := scaffoldEquivalent(projectDir, f.destPath); eq != "" {
		return UpgradeResult{Path: f.destPath, Status: UpgradeSkipped, Equivalent: eq}, true
	}
	if f.since != "" && opts.NewSince != nil && opts.NewSince(f.since) {
		return UpgradeResult{}, false
	}
	return UpgradeResult{Path: f.destPath, Status: UpgradeSkipped, Absent: true, Offered: true, Since: f.since}, true
}

// scaffoldAliases are other names a project's own copy of a managed file may
// go by, beyond the extension families scaffoldEquivalent already tries.
var scaffoldAliases = map[string][]string{
	"docker-compose.yml": {"compose.yml", "compose.yaml"},
	"Taskfile.yml":       {"taskfile.yml", "taskfile.yaml", "Taskfile.dist.yml", "Taskfile.dist.yaml"},
	"eslint.config.mjs": {
		".eslintrc", ".eslintrc.js", ".eslintrc.cjs", ".eslintrc.json", ".eslintrc.yml", ".eslintrc.yaml",
	},
}

// extFamilies are extensions a tool treats as the same file: a project with
// .golangci.yaml has the .golangci.yml the template ships.
var extFamilies = [][]string{
	{".yml", ".yaml"},
	{".js", ".mjs", ".cjs", ".ts", ".mts", ".cts"},
}

// scaffoldEquivalent returns the project-relative path of a file beside
// relPath that stands in for it, or "" when there is none.
func scaffoldEquivalent(projectDir, relPath string) string {
	dir, base := filepath.Split(relPath)
	var names []string
	ext := filepath.Ext(base)
	if stem := strings.TrimSuffix(base, ext); stem != "" && ext != "" {
		for _, fam := range extFamilies {
			if !containsString(fam, ext) {
				continue
			}
			for _, e := range fam {
				if e != ext {
					names = append(names, stem+e)
				}
			}
		}
	}
	names = append(names, scaffoldAliases[base]...)
	for _, n := range names {
		if st, err := os.Stat(filepath.Join(projectDir, dir, n)); err == nil && !st.IsDir() {
			return filepath.ToSlash(filepath.Join(dir, n))
		}
	}
	return ""
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// deletedInGit maps each of paths (project-relative) that git history shows
// deleted to the short hash of the latest commit that deleted it. One `git
// log` over all of them: a project that is not a git checkout, or a git that
// cannot run, yields an empty map, and the other rules still decide.
func deletedInGit(projectDir string, paths []string) map[string]string {
	out := map[string]string{}
	if len(paths) == 0 {
		return out
	}
	args := []string{"-C", projectDir, "log", "--no-renames", "--diff-filter=D", "--relative",
		"--format=commit %h", "--name-only", "--"}
	for _, p := range paths {
		args = append(args, filepath.ToSlash(p))
	}
	raw, err := exec.Command("git", args...).Output()
	if err != nil {
		return out
	}
	var sha string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "commit "):
			sha = strings.TrimPrefix(line, "commit ")
		default:
			// Newest first: the first deletion seen is the latest.
			if _, seen := out[line]; !seen {
				out[line] = sha
			}
		}
	}
	return out
}

// absentScaffoldCandidates are the Tier-2 files absent from disk that the
// ledger would still let forge write: the only ones whose git history can
// change the verdict.
func absentScaffoldCandidates(projectDir string, files []managedFile, cs *FileChecksums) []string {
	var out []string
	for _, f := range files {
		if f.tier != Tier2 {
			continue
		}
		if _, err := os.Stat(filepath.Join(projectDir, f.destPath)); !os.IsNotExist(err) {
			continue
		}
		if cs.IsDisowned(f.destPath) || !checksums.ScaffoldMayBeBorn(projectDir, f.destPath) {
			continue
		}
		out = append(out, f.destPath)
	}
	return out
}
