// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// testSharedState is a machine whose caches are the shared ones plus the
// default per-user locations, so the resolved-path cases do not depend on the
// machine the test runs on.
func testSharedState() sharedState {
	return sharedState{
		home: "/home/u",
		vars: map[string]string{
			"HOME":                "/home/u",
			"GOPATH":              "/home/u/go",
			"GOCACHE":             "/var/cache/workspace/go-build",
			"GOMODCACHE":          "/var/cache/workspace/go-mod",
			"npm_config_cache":    "/var/cache/workspace/npm",
			"NPM_CONFIG_CACHE":    "/var/cache/workspace/npm",
			"GOLANGCI_LINT_CACHE": "/var/cache/workspace/golangci-lint",
			"XDG_CACHE_HOME":      "/var/cache/workspace/xdg",
		},
		roots: []sharedRoot{
			{path: "/var/cache/workspace/go-build", label: "Go build cache"},
			{path: "/var/cache/workspace/go-mod", label: "Go module cache"},
			{path: "/var/cache/workspace/npm", label: "npm cache"},
			{path: "/var/cache/workspace/golangci-lint", label: "golangci-lint cache"},
			{path: "/var/cache/workspace/xdg", label: "Tool caches"},
			{path: "/home/u/.cache/go-build", label: "Go build cache"},
			{path: "/home/u/go/pkg/mod", label: "Go module cache"},
			{path: "/home/u/.npm", label: "npm cache"},
			{path: "/home/u/.cache", label: "Tool caches", loose: true},
		},
	}
}

func refusalFor(command string) string {
	return sharedStateRefusalWith(command, testSharedState)
}

// Every spelling of "destroy the machine's shared state" that this guard exists
// to stop. On 2026-10-26 something wiped the whole Go build cache twice.
func TestSharedStateGuard_RefusesDestructiveCommands(t *testing.T) {
	t.Parallel()
	refused := []string{
		// go clean
		`go clean -cache`,
		`go clean -modcache`,
		`go clean -testcache`,
		`go clean -fuzzcache`,
		`go clean -x -cache`,
		`go clean -cache -modcache`,
		`go clean -cache=true`,
		`go clean --cache`,
		`go clean -i -cache ./...`,
		`sudo go clean -cache`,
		`time go clean -modcache`,
		`timeout 60 go clean -cache`,
		`nice -n 10 go clean -cache`,
		`/usr/local/go/bin/go clean -cache`,
		`go -C sub clean -cache`,
		`GOFLAGS=-mod=mod go clean -cache`,
		`GOCACHE=/var/cache/workspace/go-build go clean -cache`,
		`env GOCACHE=$GOCACHE go clean -cache`,
		`GOMODCACHE=$GOMODCACHE go clean -modcache`,
		`cd src && go clean -testcache`,
		`echo ok; go clean -cache`,
		`make x || go clean -cache`,
		`if true; then go clean -cache; fi`,
		`bash -c 'go clean -cache'`,
		`sh -c "cd x; go clean -modcache"`,
		`bash -lc 'echo hi && go clean -cache'`,
		`eval "go clean -cache"`,
		"echo start\ngo clean -cache\necho end",
		"cat > notes <<EOF\nhello\nEOF\ngo clean -cache",

		// rm / unlink / rmdir against the caches
		`rm -rf $GOCACHE`,
		`rm -rf "$GOCACHE"/*`,
		`rm -rf ${GOCACHE}/??`,
		`rm -rf $GOCACHE/[0-9a-f][0-9a-f]`,
		`rm -rf $GOMODCACHE`,
		`rm -rf $GOMODCACHE/cache/download`,
		`rm -rf $GOPATH/pkg/mod`,
		`rm -rf $npm_config_cache`,
		`rm -rf $GOLANGCI_LINT_CACHE`,
		`rm -rf $XDG_CACHE_HOME`,
		`rm -rf $(go env GOCACHE)`,
		"rm -rf `go env GOMODCACHE`",
		`rm -rf /var/cache/workspace/go-build`,
		`rm -rf /var/cache/workspace/go-build/`,
		`rm -rf /var/cache/workspace/go-build/??`,
		`rm -rf /var/cache/workspace/go-build/9f/9fd1174b-d`,
		`rm -rf /var/cache/workspace/go-mod`,
		`rm -rf /var/cache/workspace/npm`,
		`rm -rf /var/cache/workspace/golangci-lint`,
		`rm -rf /var/cache/workspace/xdg`,
		`rm -rf /var/cache/workspace/xdg/forge/pinned`,
		`rm -rf /var/cache/workspace/*`,
		`rm -rf /var/cache/workspace/go-*`,
		`rm -rf /var/cache/workspace`,
		`rm -rf /var/cache/*`,
		`rm -r -f /var/cache/workspace/go-build`,
		`rm -fr /var/cache/workspace/./go-build`,
		`rm -rf /var/cache/workspace/../workspace/go-build`,
		`rm --recursive --force /var/cache/workspace/go-build`,
		`rm -f /var/cache/workspace/go-build/*/*`,
		`sudo rm -rf /var/cache/workspace/go-build`,
		`cd /var/cache/workspace && rm -rf go-build`,
		`cd /var/cache/workspace/go-build && rm -rf ./*`,
		`bash -c 'rm -rf $GOCACHE'`,
		`rm -rf $HOME/.cache/go-build`,
		`rm -rf ~/.cache/go-build`,
		`rm -rf "$HOME/go/pkg/mod"`,
		`rm -rf ~/.npm`,
		`rm -rf ~/.cache`,
		`rmdir /var/cache/workspace/go-build`,
		`unlink /var/cache/workspace/go-build/trim.txt`,
		`echo cleaning && rm -rf $GOCACHE && echo done`,

		// find -delete / -exec rm
		`find /var/cache/workspace/go-build -type f -delete`,
		`find $GOCACHE -name '*-a' -mtime +1 -delete`,
		`find $(go env GOCACHE) -delete`,
		`find /var/cache/workspace/npm -depth -delete`,
		`find /var/cache/workspace/go-build -type f -exec rm {} \;`,
		`find /var/cache/workspace/go-mod -exec rm -rf {} +`,
		`find /var/cache/workspace/go-build -name '*-d' -execdir rm -rf {} +`,
		`find /var/cache/workspace -maxdepth 1 -type d -delete`,
		`find $GOCACHE -type f | xargs rm`,
		`find /var/cache/workspace/go-build -type f -print0 | xargs -0 rm -f`,
		`ls $GOCACHE | xargs rm -rf`,

		// rsync --delete onto a cache
		`rsync -a --delete /tmp/empty/ /var/cache/workspace/go-build/`,
		`rsync -a --delete-after empty/ $GOCACHE/`,
		`rsync --delete -a empty/ "$GOMODCACHE"`,
		`rsync -a --del empty/ ~/.cache/go-build`,

		// docker
		`docker system prune`,
		`docker system prune -f`,
		`docker system prune -af`,
		`docker system prune -a --volumes`,
		`docker image prune -a`,
		`docker image prune --all -f`,
		`docker image prune -f -a`,
		`docker image prune -af`,
		`docker volume prune`,
		`docker volume prune -f`,
		`docker builder prune --all`,
		`docker builder prune -a -f`,
		`docker buildx prune --all`,
		`sudo docker system prune -f`,
		`docker --context remote system prune`,
		`docker -H unix:///var/run/docker.sock volume prune`,
		`bash -c "docker system prune -af"`,
		`echo done && docker system prune -f`,
		`podman system prune -a`,
	}
	for _, cmd := range refused {
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			got := refusalFor(cmd)
			if got == "" {
				t.Fatalf("command was not refused: %q", cmd)
			}
			if !strings.HasPrefix(got, "refused:") {
				t.Fatalf("refusal does not start with %q: %q", "refused:", got)
			}
		})
	}
}

// The false positives. Quoted text, heredoc bodies, and the same tools aimed
// at the project are not shared state.
func TestSharedStateGuard_AllowsEverythingElse(t *testing.T) {
	t.Parallel()
	allowed := []string{
		// go clean that touches only the project (or nothing)
		`go clean`,
		`go clean ./...`,
		`go clean -i ./mypkg`,
		`go clean -r`,
		`go clean -x ./cmd/tool`,
		`go clean -n -cache`, // a dry run only prints
		`go clean -cache=false`,
		`GOCACHE=/tmp/private-gocache go clean -cache`,
		`env GOMODCACHE=/tmp/private-mod go clean -modcache`,
		`go build ./...`,
		`go test -count=1 -run TestX ./internal/x`,
		`go build -a ./cmd/reliant`,
		`go env GOCACHE`,
		`go clean -cachedir`, // not a flag go has; not ours to second-guess

		// reading shared state is fine
		`ls $GOCACHE`,
		`du -sh /var/cache/workspace/go-build`,
		`cat /var/cache/workspace/go-build/trim.txt`,
		`find /var/cache/workspace/go-build -maxdepth 1`,
		`find $GOCACHE -name '*-a' -mtime +1 -print`,
		`rsync -a /var/cache/workspace/go-build/ /backup/go-build/`,
		`rsync -a --delete src/ /backup/x/`,

		// rm / find / rsync inside the project
		`rm -rf ./build`,
		`rm -rf build dist`,
		`rm -rf node_modules`,
		`rm -rf /home/u/project/dist`,
		`rm -rf "$HOME/project/build"`,
		`rm -rf /tmp/scratchpad/out`,
		`rm -rf $TMPDIR/x`,
		`rm -rf /var/cache/workspace-other`,
		`rm -rf /var/cache/workspace/tmp`,
		`rm -rf /var/cache/other`,
		`rm -rf ~/.cache/some-tool`,
		`cd build && rm -rf ./*`,
		`cd /tmp/x && rm -rf ./*`,
		`find . -name '*.tmp' -delete`,
		`find ./build -type f -delete`,
		`find /tmp/scratchpad -name x -exec rm {} \;`,
		`ls build | xargs rm -rf`,
		`rsync -a --delete build/ dist/`,

		// docker that removes only what the caller names, or only dangling data
		`docker ps`,
		`docker system df`,
		`docker image prune`, // dangling layers only
		`docker image prune -f`,
		`docker builder prune`,
		`docker builder prune -f`,
		`docker container prune -f`,
		`docker rm -fv mycontainer`,
		`docker rmi myimage`,
		`docker volume ls`,
		`docker volume rm myvolume`,
		`docker build -t x .`,
		`docker compose down -v`,
		`docker run --rm alpine echo docker system prune`,
		`docker run --rm -v $PWD:/w alpine rm -rf /w/build`,

		// the words, as data
		`echo "go clean -cache"`,
		`echo 'rm -rf $GOCACHE'`,
		`printf '%s\n' "docker system prune -af"`,
		`git commit -m "docs: never run go clean -cache; docker system prune is refused"`,
		`git commit -m "remove stale cache
go clean -cache is refused now"`,
		`rg -n "go clean -cache" docs/`,
		`grep -rn 'docker system prune' .`,
		`# go clean -cache
ls`,
		"cat >> notes.md <<'EOF'\nrun go clean -cache\nrm -rf $GOCACHE\ndocker system prune -af\nfind /var/cache/workspace/go-build -delete\nEOF",
		"cat > notes.md <<EOF\nrun go clean -cache\nrm -rf /var/cache/workspace/go-build\nEOF\nls",
		"cat > notes.md <<-EOF\n\tgo clean -modcache\n\tEOF\nls",
		"cat > x <<\"END\"\ndocker volume prune -f\nEND",
		"python - <<'PY'\nimport subprocess\nsubprocess.run(['go','clean','-cache'])\nPY",
		`bash -c 'echo go clean -cache'`,
		`sh -c "echo rm -rf $GOCACHE"`,
	}
	for _, cmd := range allowed {
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			if got := refusalFor(cmd); got != "" {
				t.Fatalf("a safe command was refused: %q\n%s", cmd, got)
			}
		})
	}
}

func TestSharedStateGuard_RefusalNamesTheSafeAlternative(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{`go clean -cache`, `rm -rf $GOCACHE`, `docker system prune -af`, `find $GOCACHE -delete`} {
		got := refusalFor(cmd)
		for _, want := range []string{
			cmd,                        // says which command
			"machine-disk",             // the skill
			"steward",                  // who trims
			"reliant forge storage gc", // the safe tool
			"shared",                   // why
			"There is no override",     // and that asking again will not help
			"2026-10-26",               // what this is about
		} {
			if !strings.Contains(got, want) {
				t.Errorf("refusal of %q does not mention %q:\n%s", cmd, want, got)
			}
		}
	}
	if got := refusalFor(`go clean -cache`); !strings.Contains(got, "go test -count=1") {
		t.Errorf("go clean refusal does not offer a way to bypass the cache without deleting it:\n%s", got)
	}
	if got := refusalFor(`docker system prune -af`); !strings.Contains(got, "docker rm -fv <name>") {
		t.Errorf("docker refusal does not offer the by-name removal:\n%s", got)
	}
}

// Through the real tool: nothing refused reaches the daemon, and a safe
// neighbour still does.
func TestShellTool_RefusesSharedStateCommandsBeforeDispatch(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{
		`go clean -cache`,
		`rm -rf $GOCACHE/??`,
		`rm -rf /var/cache/workspace/go-build`,
		`docker system prune -af`,
	} {
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			d, resp := runShellAgainst(t, "/home/u/project", cmd)
			if len(d.ran) != 0 {
				t.Fatalf("command reached the daemon: %q", d.ran)
			}
			if !resp.IsError || !strings.Contains(resp.Content, "reliant forge storage gc") {
				t.Fatalf("expected a refusal naming the safe remedy, got IsError=%v content=%q", resp.IsError, resp.Content)
			}
		})
	}
	for _, cmd := range []string{`go clean -i ./mypkg`, `rm -rf ./build`, `echo "go clean -cache"`} {
		d, resp := runShellAgainst(t, "/home/u/project", cmd)
		if resp.IsError || len(d.ran) != 1 {
			t.Fatalf("%q: ran=%v resp=%q", cmd, d.ran, resp.Content)
		}
	}
	// Background commands go through the same gate.
	d := &requestRecordingDaemon{}
	tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1", Project: &db.Project{ID: "p", Path: "/home/u/project"}, Daemon: d}
	resp, err := (&shellTool{}).Execute(tc, ShellParams{Command: "go clean -modcache", RunInBackground: true})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.IsError || len(d.reqs) != 0 {
		t.Fatalf("a background go clean -modcache was dispatched: %+v %+v", resp, d.reqs)
	}
}

// What the guard protects on a real machine comes from the environment the
// daemon runs in, with the cloud workspace's fixed locations always included.
func TestDiscoverSharedState(t *testing.T) {
	t.Parallel()
	env := diskwatch.Environment{
		Getenv: func(k string) string {
			return map[string]string{"GOCACHE": "/data/gocache", "XDG_CACHE_HOME": "/home/u/.cache"}[k]
		},
		Home: "/home/u",
		GOOS: "linux",
	}
	st := discoverSharedState(env)

	if got := st.vars["GOCACHE"]; got != "/data/gocache" {
		t.Errorf("GOCACHE = %q, want the environment's value", got)
	}
	if got := st.vars["GOMODCACHE"]; got != "/home/u/go/pkg/mod" {
		t.Errorf("an unset GOMODCACHE is where go puts it, $GOPATH/pkg/mod; got %q", got)
	}
	if got := st.vars["HOME"]; got != "/home/u" {
		t.Errorf("HOME = %q", got)
	}
	has := func(path string, loose bool) bool {
		for _, r := range st.roots {
			if r.path == path && r.loose == loose {
				return true
			}
		}
		return false
	}
	for _, want := range []string{
		"/data/gocache", "/var/cache/workspace/go-build", "/var/cache/workspace/go-mod",
		"/var/cache/workspace/npm", "/var/cache/workspace/golangci-lint", "/var/cache/workspace/xdg",
	} {
		if !has(want, false) {
			t.Errorf("shared root %s missing: %+v", want, st.roots)
		}
	}
	// A home-directory ~/.cache holds unrelated tools' data: protected as a
	// whole, but not child by child.
	if !has("/home/u/.cache", true) {
		t.Errorf("~/.cache should be a loose root: %+v", st.roots)
	}

	if got := sharedStateRefusalWith("rm -rf /data/gocache/ab", func() sharedState { return st }); got == "" {
		t.Error("the environment's GOCACHE is not protected")
	}
	if got := sharedStateRefusalWith("rm -rf $GOCACHE", func() sharedState { return st }); got == "" {
		t.Error("$GOCACHE does not resolve to the environment's cache")
	}
}

// A caller's own env for the command wins over the daemon's: pointing GOCACHE
// at a private directory makes `go clean -cache` private too.
func TestSharedStateGuard_CallerEnvOverridesTheCacheLocation(t *testing.T) {
	t.Parallel()
	load := func() sharedState {
		return withEnvOverrides(testSharedState(), map[string]string{"GOCACHE": "/tmp/mine"})
	}
	if got := sharedStateRefusalWith("go clean -cache", load); got != "" {
		t.Errorf("a private GOCACHE was refused:\n%s", got)
	}
	if got := sharedStateRefusalWith("rm -rf $GOCACHE", load); got != "" {
		t.Errorf("rm of a private GOCACHE was refused:\n%s", got)
	}
	if got := sharedStateRefusalWith("go clean -modcache", load); got == "" {
		t.Error("overriding GOCACHE must not unprotect the module cache")
	}
}

// A variable set earlier in the same script counts for the later command.
func TestSharedStateGuard_TracksAssignmentsWithinAScript(t *testing.T) {
	t.Parallel()
	if got := refusalFor("export GOCACHE=/tmp/private\ngo clean -cache"); got != "" {
		t.Errorf("a private cache set by export was refused:\n%s", got)
	}
	if got := refusalFor("C=/var/cache/workspace/go-build\nrm -rf $C"); got == "" {
		t.Error("a shared path held in a variable was not refused")
	}
	if got := refusalFor("export GOCACHE=/var/cache/workspace/go-build; go clean -cache"); got == "" {
		t.Error("export of the shared cache then go clean was not refused")
	}
}
