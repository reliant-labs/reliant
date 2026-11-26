package codegen

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
)

// devEnvMentionsIDP matches a dev env main.k that runs an identity provider
// some way: the scaffolded HostInfra (`engine = "zitadel"`), or a workload
// or binding naming the compose `idp` service.
var devEnvMentionsIDP = regexp.MustCompile(`zitadel|"idp"`)

// ProjectDeclaresIdentity answers "does this project have users to sign
// in?", read off the tree: deploy/kcl/identity.k, which `forge project new`
// writes for a project with a frontend and a backend unless --auth-mode none,
// is the declaration. Nothing else records the choice.
//
// It errs toward YES, because every consumer of a NO takes something away
// (the dev IdP from docker-compose.yml, the auth requirement from a born
// rpc): a project scaffolded before identity.k existed has a frontend and no
// identity.k, yet its dev env may still run the IdP, so a dev main.k that
// mentions one (or that cannot be read) also counts. An empty projectDir (no
// tree to read) counts too.
func ProjectDeclaresIdentity(projectDir string) bool {
	if projectDir == "" {
		return true
	}
	if _, err := os.Stat(filepath.Join(projectDir, filepath.FromSlash(IdentityKCLRelPath))); err == nil {
		return true
	}
	b, err := os.ReadFile(filepath.Join(projectDir, "deploy", "kcl", DevEnvName, "main.k"))
	if err != nil {
		return true
	}
	return devEnvMentionsIDP.Match(b)
}

// AuthNoneDeclaration records the explicit project-new choice in the owned
// dev main.k. Absence of managed identity alone says nothing about external
// issuers or custom validators and must never authorize public RPC scaffolding.
// Remove this declaration when adding sign-in to an initially public project.
const AuthNoneDeclaration = "# forge:auth-mode none"

// ProjectHasNoSignIn requires an explicit no-sign-in declaration, a frontend,
// and no managed identity. Unmarked existing projects retain fail-closed CRUD
// defaults even when their authentication is entirely external to forge.
func ProjectHasNoSignIn(projectDir string) bool {
	if ProjectDeclaresIdentity(projectDir) {
		return false
	}
	b, err := os.ReadFile(filepath.Join(projectDir, "deploy", "kcl", DevEnvName, "main.k"))
	if err != nil {
		return false
	}
	declared := false
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == AuthNoneDeclaration {
			declared = true
		}
	}
	if !declared {
		return false
	}
	if len(config.DiscoverInRepoFrontends(projectDir)) > 0 {
		return true
	}
	_, ok := config.DiscoverRootFrontend(projectDir)
	return ok
}
