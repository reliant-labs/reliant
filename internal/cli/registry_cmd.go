package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/pkg/cloudcred"
)

// `forge registry` is what a CI job needs to work from an env's DECLARATIONS
// alone: log docker in to the registries the env's workloads name, and read
// back the digest-pinned refs of what `forge env build <env> --push` pushed there.
//
// NEITHER COMMAND TAKES A REGISTRY, and neither has a flag that could carry
// one. The registry is part of a workload's `image` in deploy/kcl/workloads.k,
// so the only inputs are the env name and — for login — a credential, which is
// not a registry pointer.
//
// The env may name SEVERAL registries, because each workload declares its own.
// login authenticates every distinct host; ref prints one line per built image.

func newRegistryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Log in to and read refs from the registries an env's workloads declare",
		Long: `Work with the image registries an environment's workloads DECLARE — each
workload's ` + "`image`" + ` in deploy/kcl/workloads.k carries its own registry, and no
environment declares one.

The registry is never passed to forge: these commands read it from the workload
declarations, exactly as ` + "`forge env build <env> --push`" + ` and ` + "`forge env deploy <env>`" + ` do.
An env whose workloads name two registries is handled by both commands without
forge needing a concept for it.`,
	}
	cmd.AddCommand(newRegistryLoginCmd(), newRegistryRefCmd())
	return cmdutil.StrictGroup(cmd)
}

// dockerLoginArgs is the exact argv forge runs docker with, and the ONE place
// that decides it.
//
// THE CREDENTIAL IS NOT IN IT, and that is the property this function exists
// to make checkable. `--password <value>` would work — docker accepts it, with
// a deprecation warning — and it would also put the secret in this process's
// argv, where it is world-readable through `ps` for the life of the call, and
// in any shell history or CI trace that echoed the command. `--password-stdin`
// is the only form that cannot leak that way, so the argv is built here, by a
// pure function a test asserts over, rather than inline at the call site where
// a later edit could append one more flag.
func dockerLoginArgs(host, username string) []string {
	return []string{"login", host, "--username", username, "--password-stdin"}
}

// dockerLogin runs `docker <args...>` with the credential on stdin. A var so
// tests observe the login — argv included — instead of running it.
//
// It takes the ARGV rather than a host and a username so that what a test sees
// is what the subprocess would get. A seam that re-derived the argv inside the
// real implementation would let the credential reach the command line without
// any test being able to notice.
var dockerLogin = func(ctx context.Context, args []string, password io.Reader) error {
	c := exec.CommandContext(ctx, "docker", args...)
	c.Stdin = password
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// runDockerLogin logs in to one host with one credential.
//
// The credential is a string here and a Reader at the seam because it must be
// read exactly once per login and `docker login` consumes stdin to EOF: a
// shared Reader across two hosts leaves the second one authenticating with
// nothing.
func runDockerLogin(ctx context.Context, host, username, credential string) error {
	return dockerLogin(ctx, dockerLoginArgs(host, username), strings.NewReader(credential))
}

func newRegistryLoginCmd() *cobra.Command {
	var (
		username      string
		passwordStdin bool
		passwordEnv   string
	)
	var token string
	cmd := &cobra.Command{
		Use:   "login <environment>",
		Short: "docker login to every registry the env's workloads declare",
		Long: `Log docker in to each registry host named by the images the env's workloads
declare, so that ` + "`forge env build <env> --push`" + ` and any signing / SBOM / scanning step
that follows can reach them.

The registry HOSTS come from the workload declarations and nowhere else — there
is no host argument and no flag that takes one. An env whose workloads push to
two registries logs in to both, in one command.

THE PLATFORM REGISTRY NEEDS NO CREDENTIAL FROM YOU. For the host named by an
env's forge.ControlPlane registry_host (Reliant's registry by default),
forge presents the SAME control-plane credential it reaches the control plane
with — ` + "`--token`" + `, then the env's declared token_env, then what ` + "`forge login`" + `
stored, then the credential helper $` + cloudcred.HelperEnv + ` (a host application's
session — Reliant sets it). One token, so there is nothing to mint and nothing
to rotate:

  forge registry login prod

` + "`forge env build <env> --push`" + ` and ` + "`forge env deploy <env>`" + ` do this for
themselves before their first push, so a hosted pipeline usually needs no login
step at all.

FOR ANYBODY ELSE'S REGISTRY the credential is the only thing you pass, and it is
not a registry pointer:

  echo "$GITHUB_TOKEN" | forge registry login prod --username "$GITHUB_ACTOR" --password-stdin

or name the environment variable holding it, the way forge.ControlPlane names
its token_env — so a CI config states a variable NAME (non-sensitive, belongs in
git) rather than piping a secret through a shell:

  forge registry login prod --username "$GITHUB_ACTOR" --password-env GITHUB_TOKEN

One credential is used for every foreign host. That is correct for the
overwhelmingly common case (one org, one registry, one token) and honest about
the rest: for two foreign registries needing two credentials, run the command
twice, or log the second one in with plain ` + "`docker login`" + ` — forge holds no
credential store and inventing one here would be a secrets manager, not a build
tool.

An env that pushes to the platform registry AND a foreign one logs in to both in
one run: ours from the control-plane credential, theirs from the flags.

A k3d-local registry (localhost / *.localhost) takes no credentials, so it is
skipped.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env := args[0]
			if err := validateBuildEnvArg(env); err != nil {
				return err
			}
			if passwordStdin && passwordEnv != "" {
				return cliutil.UserErr("forge registry login", "--password-stdin and --password-env both given", "",
					"pass the credential exactly one way")
			}

			declared, err := renderBuildKCL(cmd.Context(), projectDirForKCL(), env)
			if err != nil {
				return err
			}
			hosts, err := declaredRegistryHostsOf(cmd.Context(), "forge registry login "+env, env)
			if err != nil {
				return err
			}

			// Split the hosts by WHO owns the credential, because that is
			// what decides where it comes from. Ours resolves from the
			// declaration; everything else needs the flags, and a run that
			// mixes them needs both halves rather than one rule applied to
			// both.
			platform := platformRegistryHost(declared)
			var foreign []string
			ours := false
			for _, h := range hosts {
				switch {
				case h == platform:
					ours = true
				case isLocalRegistryHost(h):
					fmt.Printf("[registry] %s is a local registry (declared by a workload's image): no login needed\n", h)
				default:
					foreign = append(foreign, h)
				}
			}

			gaveFlags := username != "" || passwordStdin || passwordEnv != ""
			if ours && len(foreign) == 0 && gaveFlags {
				return errPlatformRegistryTakesNoFlags(env, platform, declaredTokenEnv(declared))
			}
			if len(foreign) > 0 && !gaveFlags {
				if ours {
					return errForeignRegistryNeedsFlags(env, foreign)
				}
				return cliutil.UserErr("forge registry login", "no credential was given", "",
					fmt.Sprintf("pipe it in: echo \"$TOKEN\" | forge registry login %s --username <user> --password-stdin\n"+
						"  or name the variable holding it: forge registry login %s --username <user> --password-env TOKEN", env, env))
			}

			loggedIn := 0
			if ours {
				if err := loginToPlatformRegistry(cmd.Context(), env, platform, token, declarationFromEntities(declared)); err != nil {
					return err
				}
				loggedIn++
			}
			if len(foreign) > 0 {
				credential, err := foreignRegistryCredential(cmd, env, username, passwordStdin, passwordEnv)
				if err != nil {
					return err
				}
				for _, h := range foreign {
					fmt.Printf("[registry] logging in to %s (declared by a workload's image in env %s)\n", h, env)
					if err := runDockerLogin(cmd.Context(), h, username, credential); err != nil {
						return err
					}
					loggedIn++
				}
			}
			if loggedIn == 0 {
				fmt.Printf("[registry] nothing to log in to: every registry env %s declares is host-local\n", env)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&username, "username", "u", "", "Registry user the credential belongs to, for a registry that is NOT the platform's (e.g. $GITHUB_ACTOR, _json_key, oauth2accesstoken, AWS)")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read a foreign registry's credential from stdin (a credential never belongs on the command line)")
	cmd.Flags().StringVar(&passwordEnv, "password-env", "", "Name of the environment variable holding a foreign registry's credential (the forge.ControlPlane token_env convention: the NAME is in git, the VALUE never is)")
	cmd.Flags().StringVar(&token, "token", "", "Control-plane credential to present to the PLATFORM registry, overriding the env's token_env and the stored 'forge login' (one-off / debugging)")
	return cmd
}

// foreignRegistryCredential reads the credential for a registry that is not
// the platform's, from exactly one of the two places a caller may put it.
//
// Read ONCE and returned as a string: stdin is not re-readable, and an env
// whose workloads name two foreign registries authenticates both with it.
func foreignRegistryCredential(cmd *cobra.Command, env, username string, passwordStdin bool, passwordEnv string) (string, error) {
	if username == "" {
		return "", cliutil.UserErr("forge registry login", "--username is required for a registry that is not the platform's", "",
			"pass the registry user the credential belongs to")
	}
	if passwordStdin {
		b, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read credential from stdin: %w", err)
		}
		return string(b), nil
	}
	if passwordEnv == "" {
		return "", cliutil.UserErr("forge registry login", "no credential was given", "",
			fmt.Sprintf("pipe it in: echo \"$TOKEN\" | forge registry login %s --username <user> --password-stdin\n"+
				"  or name the variable holding it: forge registry login %s --username <user> --password-env TOKEN", env, env))
	}
	v := os.Getenv(passwordEnv)
	if v == "" {
		return "", cliutil.UserErr("forge registry login",
			fmt.Sprintf("$%s is empty or unset, so there is no credential to log in with", passwordEnv), "",
			fmt.Sprintf("set %s in the environment (a CI secret), or pipe the credential in with --password-stdin", passwordEnv))
	}
	return v, nil
}

func newRegistryRefCmd() *cobra.Command {
	var (
		image        string
		githubOutput bool
	)
	cmd := &cobra.Command{
		Use:   "ref <environment> [--image <name>]",
		Short: "Print the digest-pinned refs `forge env build <env> --push` pushed",
		Long: `Print ` + "`<image>@<digest>`" + ` for each image the last ` + "`forge env build <env> --push`" + `
pushed — the immutable references a signing, SBOM, provenance or vulnerability
scan step should act on. They are read from .forge/state/ (what the build
recorded), so each ref names the registry its own workload declared.

By default every built image is printed, one per line, prefixed with the
workload that declared it. --image narrows to one.

--github-output also appends ref=, image= and digest= to the file GitHub
Actions names in $GITHUB_OUTPUT. With several images it writes the <workload>_
prefixed form as well, so a later step can address a specific one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env := args[0]
			if err := validateBuildEnvArg(env); err != nil {
				return err
			}
			refs, err := pushedImageRefs(cmd.Context(), projectDirForKCL(), env, image)
			if err != nil {
				return err
			}
			multiple := len(refs) > 1
			for _, r := range refs {
				if multiple {
					fmt.Printf("%s\t%s\n", r.workload, r)
				} else {
					fmt.Println(r.String())
				}
			}
			if githubOutput {
				return appendGitHubOutput(refs)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", "", "Print only this image (default: every image the build pushed)")
	cmd.Flags().BoolVar(&githubOutput, "github-output", false, "Also append ref=, image= and digest= to $GITHUB_OUTPUT")
	return cmd
}

// declaredRegistryHostsOf renders env and returns the distinct registry hosts
// its workloads' images name, or the runbook naming what to declare. context is
// the command that needed them.
//
// RESOLVED AGAINST THE ENV'S PUSH BASE, exactly as the build resolves it
// (resolvePushPlan). A BARE hosted image is legitimate since ADR-0003 F1 — it
// resolves to `<registry_host>/<org>/<project>/<name>` — and
// enumerating destinations with no base leaves every such image hostless,
// which this function then reads as "nothing to push". So `forge registry
// login prod` on a fully correct hosted project reported that no workload
// declared a pushable image, which is the exact shape ADR-0003 makes the
// DEFAULT for a hosted env.
func declaredRegistryHostsOf(ctx context.Context, context, env string) ([]string, error) {
	ents, err := renderBuildKCL(ctx, projectDirForKCL(), env)
	if err != nil {
		return nil, err
	}
	// Strict: logging in to the platform registry already needs the credential,
	// and a bare hosted image only has a host once the org is known.
	if err := requireOrg(ctx, ents); err != nil {
		return nil, err
	}
	plan := pushPlan{env: env, destinations: declaredImageDestinationsWithBase(ents, platformPushBase(ents))}
	if hosts := plan.hosts(); len(hosts) > 0 {
		return hosts, nil
	}
	return nil, noPushableImagesError(context, env, ents)
}

// registryHost is the host docker logs in to for a reference: the part before
// the first `/` when it looks like a host (has a `.` or `:`, or is localhost),
// else "" — a bare `org/img` names no host.
func registryHost(reference string) string {
	first, _, ok := strings.Cut(reference, "/")
	if ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return first
	}
	return ""
}

// isLocalRegistryHost mirrors kcl/base.k is_local_registry: a host-local
// registry (localhost, 127.0.0.1, *.localhost) takes no credentials.
func isLocalRegistryHost(host string) bool {
	name, _, _ := strings.Cut(host, ":")
	return name == "localhost" || name == "127.0.0.1" || strings.HasSuffix(name, ".localhost")
}

// imageRef is one pushed image: <repository>@<digest>, and the workload that
// declared the repository.
type imageRef struct {
	repository string // <registry-host>/<path>
	digest     string
	workload   string
}

func (r imageRef) String() string { return r.repository + "@" + r.digest }

// pushedImageRefs reads what `forge env build <env> --push` recorded and returns
// the digest-pinned ref of each pushed image, sorted by repository. only, when
// set, narrows to the image whose repository or artifact name matches it.
func pushedImageRefs(ctx context.Context, projectDir, env, only string) ([]imageRef, error) {
	var refs []imageRef
	add := func(repository, digest, workload string) {
		if repository == "" || digest == "" {
			return
		}
		for _, existing := range refs {
			if existing.repository == repository {
				return
			}
		}
		refs = append(refs, imageRef{repository: repository, digest: digest, workload: workload})
	}

	// Which workload declared each repository, for the output labels. A render
	// failure is not fatal here: the refs come from build state, and a label
	// is a convenience.
	declaredBy := map[string]string{}
	if ents, err := renderBuildKCL(ctx, projectDir, env); err == nil {
		for _, d := range declaredImageDestinations(ents) {
			declaredBy[d.repository] = d.workload
		}
	}

	if st, err := ReadBuildState(projectDir, env); err == nil && st != nil {
		add(st.Image, st.Digest, declaredBy[st.Image])
	}
	for _, st := range readAllImageBuildStates(projectDir, env) {
		add(st.Image, st.Digest, declaredBy[st.Image])
	}

	if only != "" {
		filtered := refs[:0:0]
		for _, r := range refs {
			if r.repository == only || repositoryName(r.repository) == only {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) == 0 {
			return nil, notPushedError(env, fmt.Sprintf("no image matching %q", only))
		}
		refs = filtered
	}
	if len(refs) == 0 {
		return nil, notPushedError(env, "no image")
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].repository < refs[j].repository })
	return refs, nil
}

func notPushedError(env, what string) error {
	return cliutil.UserErr("forge registry ref "+env,
		fmt.Sprintf("%s with a pushed digest recorded for env %q", what, env), "",
		fmt.Sprintf("run forge env build %s --push first — it records the digest of what it pushed to each image's declared reference", env))
}

// readAllImageBuildStates reads every per-image build state recorded for env.
func readAllImageBuildStates(projectDir, env string) []buildtarget.State {
	names, err := buildtarget.ListStates(projectDir, env)
	if err != nil {
		return nil
	}
	var out []buildtarget.State
	for _, name := range names {
		st, err := buildtarget.ReadState(projectDir, env, name)
		if err != nil || st == nil {
			continue
		}
		out = append(out, *st)
	}
	return out
}

// appendGitHubOutput writes ref=/image=/digest= for a GitHub Actions step. With
// several images the unprefixed keys name the FIRST (sorted) one and each also
// gets a <workload>_-prefixed set, so a later step can address a specific image
// without this command having to be run once per image.
func appendGitHubOutput(refs []imageRef) error {
	path := os.Getenv("GITHUB_OUTPUT")
	if path == "" {
		return cliutil.UserErr("forge registry ref --github-output", "$GITHUB_OUTPUT is not set", "",
			"run it inside a GitHub Actions step, or drop --github-output and read the refs from stdout")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open $GITHUB_OUTPUT: %w", err)
	}
	var b strings.Builder
	first := refs[0]
	fmt.Fprintf(&b, "ref=%s\nimage=%s\ndigest=%s\n", first, first.repository, first.digest)
	if len(refs) > 1 {
		for _, r := range refs {
			key := githubOutputKey(r.workload)
			if key == "" {
				key = githubOutputKey(repositoryName(r.repository))
			}
			fmt.Fprintf(&b, "%s_ref=%s\n%s_image=%s\n%s_digest=%s\n", key, r, key, r.repository, key, r.digest)
		}
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		return fmt.Errorf("write $GITHUB_OUTPUT: %w", err)
	}
	// A failed close can drop the appended outputs; later steps would then
	// read an empty ref and sign nothing.
	if err := f.Close(); err != nil {
		return fmt.Errorf("close $GITHUB_OUTPUT: %w", err)
	}
	return nil
}

// githubOutputKey folds a workload name into a GitHub Actions output key:
// anything outside [A-Za-z0-9_] becomes `_`.
func githubOutputKey(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
