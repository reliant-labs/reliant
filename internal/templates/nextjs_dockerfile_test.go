package templates

import (
	"strings"
	"testing"
)

func renderNextjsDockerfile(t *testing.T, data FrontendTemplateData) []byte {
	t.Helper()
	out, err := FrontendTemplates().Render("nextjs/Dockerfile.tmpl", data)
	if err != nil {
		t.Fatalf("render nextjs/Dockerfile.tmpl: %v", err)
	}
	return out
}

// dockerInstructions returns the Dockerfile's instruction lines — comments
// and blank lines dropped — so an assertion about what the image DOES cannot
// be satisfied by a comment that merely mentions it.
func dockerInstructions(body []byte) []string {
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func hasInstruction(lines []string, substr string) bool {
	for _, l := range lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

// TestNextjsDockerfileFollowsOutputMode is the reproduction for "the Next.js
// Dockerfile ignores the frontend's `output` mode".
//
// The template used to be one fixed file that copied .next-prod/standalone —
// the artifact ONLY `output: standalone` produces. A static frontend's build
// writes out/, and a server frontend's writes a full .next-prod for
// `next start`, so both images failed at
//
//	COPY --from=builder /app/.next-prod/standalone ./: not found
//
// and CI's docker-build job builds an image for every frontend, so a static
// frontend's CI could never go green. Each mode must copy what its build
// actually writes.
func TestNextjsDockerfileFollowsOutputMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		output   string
		want     []string // instruction substrings the image needs
		mustNot  []string // instruction substrings that break this mode
		exposure string
	}{
		{
			output: "standalone",
			want: []string{
				"FROM node:22-alpine AS runner",
				"COPY --from=builder --chown=node:node /src/frontends/web/.next-prod/standalone ./",
				`CMD ["node", "server.js"]`,
			},
			mustNot:  []string{"/out", "nginx", "next start"},
			exposure: "EXPOSE 3000",
		},
		{
			output: "static",
			want: []string{
				"FROM nginxinc/nginx-unprivileged:1.30-alpine AS runner",
				"COPY --from=builder /src/frontends/web/out /usr/share/nginx/html",
			},
			mustNot:  []string{".next-prod/standalone", "server.js", "USER node"},
			exposure: "EXPOSE 8080",
		},
		{
			output: "server",
			want: []string{
				"COPY --from=builder --chown=node:node /src/frontends/web/.next-prod ./.next-prod",
				"COPY --from=builder --chown=node:node /src/frontends/web/node_modules ./node_modules",
				`CMD ["node", "node_modules/next/dist/bin/next", "start"]`,
			},
			mustNot:  []string{".next-prod/standalone", "server.js", "nginx"},
			exposure: "EXPOSE 3000",
		},
	}
	for _, tt := range tests {
		t.Run(tt.output, func(t *testing.T) {
			t.Parallel()
			lines := dockerInstructions(renderNextjsDockerfile(t, FrontendTemplateData{FrontendName: "web", Output: tt.output}))
			for _, w := range append(tt.want, tt.exposure) {
				if !hasInstruction(lines, w) {
					t.Errorf("output=%s: no instruction containing %q\n%s", tt.output, w, strings.Join(lines, "\n"))
				}
			}
			for _, bad := range tt.mustNot {
				if hasInstruction(lines, bad) {
					t.Errorf("output=%s: an instruction references %q, which this mode's build does not produce "+
						"or its runner does not use\n%s", tt.output, bad, strings.Join(lines, "\n"))
				}
			}
		})
	}
}

// The build runs at the frontend's REPOSITORY path, not /app. From /app the
// committed tsconfig pin "../../node_modules/<pkg>" named /node_modules, which
// webpack re-rooted onto /app/node_modules: a pin that resolved in the image
// and nowhere else, which is what made a bundle split surface only in Docker.
// And the install is `npm ci` — lockfile-exact — never `npm install`.
func TestNextjsDockerfileBuildsAtTheRepoPath(t *testing.T) {
	t.Parallel()

	for _, output := range []string{"standalone", "static", "server"} {
		lines := dockerInstructions(renderNextjsDockerfile(t, FrontendTemplateData{
			FrontendName: "console", RepoPath: "apps/console", Output: output,
		}))
		if !hasInstruction(lines, "WORKDIR /src/apps/console") {
			t.Errorf("output=%s: build does not run at the frontend's repo path /src/apps/console:\n%s",
				output, strings.Join(lines, "\n"))
		}
		if hasInstruction(lines, "WORKDIR /app") && !hasInstruction(lines, "AS runner") {
			t.Errorf("output=%s: builder still uses WORKDIR /app", output)
		}
		if !hasInstruction(lines, "npm ci") || hasInstruction(lines, "npm install") {
			t.Errorf("output=%s: the image must install with `npm ci`, never `npm install`:\n%s",
				output, strings.Join(lines, "\n"))
		}
	}

	// A payload without RepoPath defaults to where the scaffolder writes.
	lines := dockerInstructions(renderNextjsDockerfile(t, FrontendTemplateData{FrontendName: "web", Output: "standalone"}))
	if !hasInstruction(lines, "WORKDIR /src/frontends/web") {
		t.Errorf("RepoPath did not default to frontends/<name>:\n%s", strings.Join(lines, "\n"))
	}
}

// A static export built with a base_path emits every URL under it, so the
// files must be served from there — the same mount the StaticSite deploy uses.
func TestNextjsDockerfileStaticMountsUnderBasePath(t *testing.T) {
	t.Parallel()

	lines := dockerInstructions(renderNextjsDockerfile(t, FrontendTemplateData{
		FrontendName: "web", Output: "static", BasePath: "/admin",
	}))
	for _, want := range []string{
		"COPY --from=builder /src/frontends/web/out /usr/share/nginx/html/admin",
		"'    error_page 404 /admin/404.html;'",
		"http://127.0.0.1:8080/admin/",
	} {
		if !hasInstruction(lines, want) {
			t.Errorf("static+base_path image lacks %q:\n%s", want, strings.Join(lines, "\n"))
		}
	}
}

// The node runners execute `node` and nothing else, and every HIGH CVE a scan
// of node:22-alpine reports sits in the npm/corepack trees the base image
// bundles. CI scans every frontend image with Trivy at HIGH,CRITICAL, so a
// runner that keeps them fails that gate for code it never runs.
func TestNextjsDockerfileNodeRunnerDropsNpm(t *testing.T) {
	t.Parallel()

	for _, output := range []string{"standalone", "server"} {
		body := string(renderNextjsDockerfile(t, FrontendTemplateData{FrontendName: "web", Output: output}))
		runner := body[strings.Index(body, "AS runner"):]
		if !strings.Contains(runner, "rm -rf /usr/local/lib/node_modules/npm") {
			t.Errorf("output=%s: the node runner keeps npm and its vulnerable dependency tree", output)
		}
	}
}

// The frontend's image builds with the frontend directory as its context, and
// a context reads only its OWN .dockerignore — so the scaffold must ship one
// beside the Dockerfile, or a host node_modules (native binaries for the
// developer's OS) is copied over the image's `npm ci`.
func TestNextjsScaffoldShipsDockerignore(t *testing.T) {
	t.Parallel()

	body, err := FrontendTemplates().Render("nextjs/.dockerignore", FrontendTemplateData{FrontendName: "web"})
	if err != nil {
		t.Fatalf("nextjs/.dockerignore is not in the template tree: %v", err)
	}
	files, err := ListFrontendTree("nextjs")
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, f := range files {
		if f.Rel == ".dockerignore" {
			listed = true
		}
	}
	if !listed {
		t.Error(".dockerignore is not part of the rendered nextjs tree, so the scaffold never writes it")
	}
	for _, want := range []string{"node_modules/", ".next-prod/", "out/", "public/config.js"} {
		if !strings.Contains(string(body), "\n"+want+"\n") {
			t.Errorf(".dockerignore does not exclude %q", want)
		}
	}
}
