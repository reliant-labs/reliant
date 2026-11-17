// File: internal/cli/lint/lint_identity_share.go
//
// identity-share-with — the part of forge.ManagedIdentity.share_with that one
// env's render cannot see. KCL refuses a share_with that names another project,
// this env itself, or a host-run IdP, and a sharer that declares roles, smtp,
// seed users, machine users, a domain or a project name. Whether the OWNER env
// exists, declares a hosted identity of its own and is not itself a sharer (a
// chain) is a fact about two envs, so it is judged here, over every env of the
// project. The control plane re-checks it at admission, where it also sees the
// org.
package lint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/linter/finding"
)

// envIdentity is the part of an env's rendered identity this check reads.
type envIdentity struct {
	Runtime   string `json:"runtime"`
	ShareWith string `json:"share_with"`
}

// identityShareFinding is one share_with that cannot be honoured.
type identityShareFinding struct {
	Env     string
	Message string
	Fix     string
}

// checkIdentityShares judges every sharer against its owner. identities maps an
// env to its rendered identity, absent when the env declares none.
func checkIdentityShares(identities map[string]*envIdentity) []identityShareFinding {
	envs := make([]string, 0, len(identities))
	for e := range identities {
		envs = append(envs, e)
	}
	sort.Strings(envs)
	var out []identityShareFinding
	for _, env := range envs {
		id := identities[env]
		if id == nil || id.ShareWith == "" {
			continue
		}
		owner, known := identities[id.ShareWith]
		switch {
		case !known:
			out = append(out, identityShareFinding{env, fmt.Sprintf("identity.share_with %q names no env of this project (envs: %v)", id.ShareWith, envs),
				"name an env under deploy/kcl/ that declares a hosted identity; sharing another project's users is not supported"})
		case owner == nil:
			out = append(out, identityShareFinding{env, fmt.Sprintf("identity.share_with %q: that env declares no identity, so there is no instance to share", id.ShareWith),
				fmt.Sprintf("declare Bundle.identity in env %q, or drop share_with here", id.ShareWith)})
		case owner.ShareWith != "":
			out = append(out, identityShareFinding{env, fmt.Sprintf("identity.share_with %q: that env itself shares %q's instance, and sharing does not chain", id.ShareWith, owner.ShareWith),
				fmt.Sprintf("share_with %q directly: the env that owns the instance", owner.ShareWith)})
		case owner.Runtime != "hosted":
			out = append(out, identityShareFinding{env, fmt.Sprintf("identity.share_with %q: that env's identity runs on the %s, and only a hosted instance can be shared", id.ShareWith, owner.Runtime),
				"share an env whose identity runs on the control plane"})
		}
	}
	return out
}

func identityShareFindings(projectDir string, cfg *config.ProjectConfig) []identityShareFinding {
	kclDir := filepath.Join(projectDir, deployKCLDirFor(cfg))
	entries, err := os.ReadDir(kclDir)
	if err != nil {
		return nil
	}
	identities := map[string]*envIdentity{}
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
		raw, err := kclrender.Run(projectDir, envDir, []string{"env=" + strconv.Quote(e.Name())})
		if err != nil {
			continue
		}
		var doc struct {
			Output struct {
				Identity *envIdentity `json:"identity"`
			} `json:"output"`
		}
		if json.Unmarshal(raw, &doc) != nil {
			continue
		}
		identities[e.Name()] = doc.Output.Identity
	}
	return checkIdentityShares(identities)
}

func collectIdentityShareJSON(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
	var out []lintJSONFinding
	for _, f := range identityShareFindings(rc.cwd, rc.cfg) {
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
