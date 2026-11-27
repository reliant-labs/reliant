// File: internal/cli/lint/lint_identity_share.go
//
// identity-share-with — the part of forge.ManagedIdentity.share_with that one
// env's render cannot see. KCL refuses a share_with that names another project,
// this env itself, or a host-run IdP, and a sharer that declares roles, smtp,
// seed users, machine users, a domain or a project name. Whether the OWNER env
// exists, declares a hosted identity of its own and is not itself a sharer (a
// chain) is a fact about two envs, so it is judged here, over every env of the
// project. The control plane re-checks it at admission, where it also sees the
// org. With several identities in an env (Bundle.identities) it is judged per
// name: each sharer against the owner's identity of the same name.
package lint

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/linter/finding"
)

// envIdentity is the part of an env's rendered identity this check reads.
type envIdentity struct {
	Name      string `json:"name"`
	Runtime   string `json:"runtime"`
	ShareWith string `json:"share_with"`
}

// identityShareFinding is one share_with that cannot be honoured.
type identityShareFinding struct {
	Env     string
	Message string
	Fix     string
}

// checkIdentityShares judges every sharer against its owner. identities maps
// an env to the identities it renders (`identity`, or several in
// `identities`), empty when it declares none. share_with is PER NAME: a
// sharer reuses the owner env's identity of the same name. An env of one
// identity sharing an owner of one identity is matched whatever the names,
// as before names mattered.
func checkIdentityShares(identities map[string][]envIdentity) []identityShareFinding {
	envs := make([]string, 0, len(identities))
	for e := range identities {
		envs = append(envs, e)
	}
	sort.Strings(envs)
	var out []identityShareFinding
	for _, env := range envs {
		ids := identities[env]
		for _, id := range ids {
			if id.ShareWith == "" {
				continue
			}
			label := "identity"
			if len(ids) > 1 {
				label = "identities[" + id.Name + "]"
			}
			owners, known := identities[id.ShareWith]
			owner, found := identityNamed(owners, id.Name)
			if !found && len(owners) == 1 && len(ids) == 1 {
				owner, found = owners[0], true
			}
			switch {
			case !known:
				out = append(out, identityShareFinding{env, fmt.Sprintf("%s.share_with %q names no env of this project (envs: %v)", label, id.ShareWith, envs),
					"name an env under deploy/kcl/ that declares a hosted identity; sharing another project's users is not supported"})
			case len(owners) == 0:
				out = append(out, identityShareFinding{env, fmt.Sprintf("%s.share_with %q: that env declares no identity, so there is no instance to share", label, id.ShareWith),
					fmt.Sprintf("declare Bundle.identity in env %q, or drop share_with here", id.ShareWith)})
			case !found:
				out = append(out, identityShareFinding{env, fmt.Sprintf("%s.share_with %q: that env declares no identity named %q (it declares %v), and share_with reuses the instance of the same name", label, id.ShareWith, id.Name, identityNames(owners)),
					fmt.Sprintf("declare an identity named %q in env %q, or rename this one to the owner's", id.Name, id.ShareWith)})
			case owner.ShareWith != "":
				out = append(out, identityShareFinding{env, fmt.Sprintf("%s.share_with %q: that env itself shares %q's instance, and sharing does not chain", label, id.ShareWith, owner.ShareWith),
					fmt.Sprintf("share_with %q directly: the env that owns the instance", owner.ShareWith)})
			case owner.Runtime != "hosted":
				out = append(out, identityShareFinding{env, fmt.Sprintf("%s.share_with %q: that env's identity runs on the %s, and only a hosted instance can be shared", label, id.ShareWith, owner.Runtime),
					"share an env whose identity runs on the control plane"})
			}
		}
	}
	return out
}

func identityNamed(ids []envIdentity, name string) (envIdentity, bool) {
	for _, i := range ids {
		if i.Name == name {
			return i, true
		}
	}
	return envIdentity{}, false
}

func identityNames(ids []envIdentity) []string {
	out := make([]string, 0, len(ids))
	for _, i := range ids {
		out = append(out, i.Name)
	}
	return out
}

func identityShareFindings(ctx context.Context, projectDir string, cfg *config.ProjectConfig) []identityShareFinding {
	kclDir := filepath.Join(projectDir, deployKCLDirFor(cfg))
	entries, err := os.ReadDir(kclDir)
	if err != nil {
		return nil
	}
	identities := map[string][]envIdentity{}
	for _, e := range entries {
		envDir := filepath.Join(kclDir, e.Name())
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(envDir, "main.k")); err != nil {
			continue
		}
		// A render that fails is the render's own error to report (`forge env
		// render`); this judges only what it can read.
		raw, err := lintKCLRun(ctx, projectDir, envDir, []string{"env=" + strconv.Quote(e.Name())})
		if err != nil {
			continue
		}
		var doc struct {
			Output struct {
				Identity   *envIdentity  `json:"identity"`
				Identities []envIdentity `json:"identities"`
			} `json:"output"`
		}
		if json.Unmarshal(raw, &doc) != nil {
			continue
		}
		ids := doc.Output.Identities
		if doc.Output.Identity != nil {
			ids = []envIdentity{*doc.Output.Identity}
		}
		identities[e.Name()] = ids
	}
	return checkIdentityShares(identities)
}

func collectIdentityShareJSON(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
	var out []lintJSONFinding
	for _, f := range identityShareFindings(rc.ctx, rc.cwd, rc.cfg) {
		out = append(out, lintJSONFinding{
			File:     deployKCLDirFor(rc.cfg) + "/" + f.Env,
			Severity: string(finding.SeverityError),
			Rule:     "identity-share-with",
			Message:  fmt.Sprintf("env %s: %s", f.Env, f.Message),
			FixHint:  f.Fix,
		})
	}
	return out, len(out) > 0, nil
}
