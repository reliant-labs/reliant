package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/pkg/deploy"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// writeTree lays files down under a fresh directory and returns it.
func writeAppTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// nodeExpressApp is an Express API with its own Dockerfile and no compose
// file, no database and no Go.
func nodeExpressApp(t *testing.T) string {
	return writeAppTree(t, map[string]string{
		"package.json":  `{"name":"todo-api","scripts":{"dev":"node --watch src/server.js","start":"node src/server.js"},"dependencies":{"express":"^4.19.0"}}`,
		"src/server.js": "const express = require('express');\nconst app = express();\napp.get('/health', (req, res) => res.json({ok: true}));\napp.listen(process.env.PORT || 3000);\n",
		"Dockerfile":    "FROM node:20-slim\nWORKDIR /app\nCOPY package.json ./\nRUN npm install --omit=dev\nCOPY src ./src\nEXPOSE 3000\nCMD [\"node\", \"src/server.js\"]\n",
		".gitignore":    "node_modules/\n",
	})
}

// pythonComposeApp is a FastAPI app with its own Dockerfile and a compose
// file running it beside Postgres and Redis, migrating with alembic.
func pythonComposeApp(t *testing.T) string {
	return writeAppTree(t, map[string]string{
		"requirements.txt": "fastapi==0.115.0\nuvicorn[standard]==0.30.0\npsycopg[binary]==3.2.1\nalembic==1.13.2\n",
		"app/main.py":      "from fastapi import FastAPI\napp = FastAPI()\n\n@app.get(\"/health\")\ndef health():\n    return {\"ok\": True}\n",
		"alembic.ini":      "[alembic]\nscript_location = migrations\n",
		"Dockerfile":       "FROM python:3.12-slim\nWORKDIR /srv\nCOPY requirements.txt .\nRUN pip install --no-cache-dir -r requirements.txt\nCOPY . .\nCMD [\"uvicorn\", \"app.main:app\", \"--host\", \"0.0.0.0\", \"--port\", \"8000\"]\n",
		"docker-compose.yml": `services:
  web:
    build: .
    ports:
      - "8000:8000"
    environment:
      DATABASE_URL: postgresql://app:app@db:5432/notes
      LOG_LEVEL: info
      SESSION_SECRET: dev-secret
    depends_on:
      - db
  db:
    image: postgres:16
    environment:
      POSTGRES_USER: app
      POSTGRES_PASSWORD: app
      POSTGRES_DB: notes
  redis:
    image: redis:7
  adminer:
    image: adminer
`,
	})
}

// snapshotTree maps every file under root (but forge's own) to its bytes.
func snapshotAppTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		data, err := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func adoptApp(t *testing.T, root, name string, claimable, dryRun bool) error {
	t.Helper()
	return runNewApp(newAppInput{projectName: name, projectPath: root, inPlace: true, claimable: claimable, dryRun: dryRun})
}

// What --kind app reads off an Express app: one service from the root
// Dockerfile on its EXPOSEd port, dev running its `dev` script on the host,
// and no database.
func TestDetectApp_NodeExpress(t *testing.T) {
	root := nodeExpressApp(t)
	det, err := detectApp(root, "todo", false)
	if err != nil {
		t.Fatal(err)
	}
	s := det.spec
	if len(s.Workloads) != 1 || s.ComposeFile != "" || s.Database != nil || s.Migrate != nil {
		t.Fatalf("spec = %+v, want one workload, no compose, no database", s)
	}
	w := s.Workloads[0]
	if w.Name != "api" || w.Kind != "service" || w.Port != 3000 || w.Build == nil || w.Build.Dockerfile != "Dockerfile" {
		t.Errorf("workload = %+v, want service api on 3000 built from Dockerfile", w)
	}
	if strings.Join(w.DevCommand, " ") != "npm run dev" {
		t.Errorf("dev command = %v, want npm run dev", w.DevCommand)
	}
	if strings.Join(det.dockerignoreMissing, ",") != "deploy,.forge,secrets" {
		t.Errorf("dockerignore advice = %v", det.dockerignoreMissing)
	}
}

// What --kind app reads off a compose app: the built service on its
// published port with its literal env, DATABASE_URL from the managed
// Postgres the compose `db` becomes, the credential as a secret, alembic as
// the migration job, Redis as a hosted third-party image and adminer dev-only.
func TestDetectApp_PythonCompose(t *testing.T) {
	root := pythonComposeApp(t)
	det, err := detectApp(root, "notes", false)
	if err != nil {
		t.Fatal(err)
	}
	s := det.spec
	if s.ComposeFile != "docker-compose.yml" {
		t.Errorf("compose file = %q", s.ComposeFile)
	}
	if s.Database == nil || s.Database.Name != "notes" || s.Database.ComposeService != "db" {
		t.Fatalf("database = %+v, want notes from compose service db", s.Database)
	}
	var names []string
	byName := map[string]int{}
	for i, w := range s.Workloads {
		names = append(names, w.Name)
		byName[w.Name] = i
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "redis,web" {
		t.Fatalf("workloads = %v, want redis and web", names)
	}
	web := s.Workloads[byName["web"]]
	if web.Port != 8000 || !web.UsesDatabase || web.Env["LOG_LEVEL"] != "info" || web.Env["DATABASE_URL"] != "" || web.Env["SESSION_SECRET"] != "" {
		t.Errorf("web = %+v, want port 8000, DATABASE_URL from the database, LOG_LEVEL literal, SESSION_SECRET not literal", web)
	}
	if strings.Join(web.Secrets, ",") != "SESSION_SECRET" || strings.Join(det.secrets, ",") != "SESSION_SECRET" {
		t.Errorf("secrets = %v / %v, want SESSION_SECRET", web.Secrets, det.secrets)
	}
	if redis := s.Workloads[byName["redis"]]; redis.Build != nil || redis.Image != "docker.io/library/redis:7" || redis.Expose {
		t.Errorf("redis = %+v, want the third-party image, internal only", redis)
	}
	if s.Migrate == nil || strings.Join(s.Migrate.Command, " ") != "alembic upgrade head" || s.Migrate.Workload != "web" {
		t.Errorf("migrate = %+v, want alembic upgrade head from web's image", s.Migrate)
	}
	var devOnly []string
	for _, d := range s.DevOnly {
		devOnly = append(devOnly, d.ComposeService)
	}
	sort.Strings(devOnly)
	if strings.Join(devOnly, ",") != "adminer,db" {
		t.Errorf("dev-only services = %v, want adminer and db", devOnly)
	}
}

// --dry-run prints and writes nothing; a real run adds only forge.yaml and
// deploy/kcl (and forge's block in .gitignore), and never a Go scaffold.
func TestNewApp_WritesOnlyTheDeclarations(t *testing.T) {
	testkit.Slow(t, "the .gitignore merge execs git")
	root := nodeExpressApp(t)
	before := snapshotAppTree(t, root)
	if err := adoptApp(t, root, "todo", false, true); err != nil {
		t.Fatal(err)
	}
	if after := snapshotAppTree(t, root); len(after) != len(before) {
		t.Fatalf("--dry-run wrote files: %d before, %d after", len(before), len(after))
	}

	if err := adoptApp(t, root, "todo", false, false); err != nil {
		t.Fatal(err)
	}
	after := snapshotAppTree(t, root)
	for rel, body := range before {
		if rel == ".gitignore" {
			if !strings.HasPrefix(after[rel], body) || !strings.Contains(after[rel], "secrets/") {
				t.Errorf(".gitignore was not merged additively:\n%s", after[rel])
			}
			continue
		}
		if after[rel] != body {
			t.Errorf("the app's %s changed", rel)
		}
	}
	var added []string
	for rel := range after {
		if _, ok := before[rel]; !ok {
			added = append(added, rel)
		}
	}
	sort.Strings(added)
	want := "deploy/kcl/dev/main.k,deploy/kcl/kcl.mod,deploy/kcl/prod/main.k,deploy/kcl/staging/main.k,deploy/kcl/workloads.k,forge.yaml"
	if strings.Join(added, ",") != want {
		t.Errorf("added %v, want exactly %s", added, want)
	}
	if yaml := after["forge.yaml"]; !strings.Contains(yaml, "name: todo") || strings.Contains(yaml, "\nmodule_path:") || strings.Contains(yaml, "\nharness:") {
		t.Errorf("forge.yaml:\n%s", yaml)
	}
	// A second run refuses: the app is a forge project now.
	if err := adoptApp(t, root, "todo", false, false); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("second adoption = %v, want a refusal naming the existing forge.yaml", err)
	}
}

func TestNewApp_Refusals(t *testing.T) {
	root := nodeExpressApp(t)
	if err := runNewApp(newAppInput{projectName: "todo", projectPath: root}); err == nil || !strings.Contains(err.Error(), "--in-place") {
		t.Errorf("without --in-place: %v, want a refusal naming --in-place", err)
	}
	empty := writeAppTree(t, map[string]string{"main.py": "print('hi')\n"})
	if err := adoptApp(t, empty, "x", false, false); err == nil || !strings.Contains(err.Error(), "no Dockerfile") {
		t.Errorf("no Dockerfile: %v, want a refusal naming the Dockerfile", err)
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 1 {
		t.Errorf("a refusal wrote files: %d entries", len(entries))
	}
}

// The bare --in-place refusal over a Dockerfile app names --kind app.
func TestProjectNewInPlaceOverADockerfileAppPointsAtKindApp(t *testing.T) {
	root := nodeExpressApp(t)
	err := runNew(t.Context(), "todo", root, "", "", nil, nil, "", true, false, nil, "reliant", true, "local", "per-service", false, false, false, false, newFrontendOptions{})
	if err == nil || !strings.Contains(err.Error(), "--kind app") {
		t.Fatalf("want a refusal naming --kind app, got %v", err)
	}
}

func TestAppNextStepsArePasteable(t *testing.T) {
	det, err := detectApp(pythonComposeApp(t), "notes", false)
	if err != nil {
		t.Fatal(err)
	}
	lines := appNextSteps(det, false)
	want := map[string]bool{"env render": false, "env up": false, "env deploy": false, "secret set": false, "obs check": false, "skill load": false, "project new": false}
	for _, line := range lines {
		for _, args := range pasteableInvocations(line, Name()) {
			if path := resolvePasteable(t, args); path != "" {
				if _, ok := want[path]; ok {
					want[path] = true
				}
			}
		}
	}
	for cmd, seen := range want {
		if !seen {
			t.Errorf("app next steps never name `%s`:\n%s", cmd, strings.Join(lines, "\n"))
		}
	}
}

// The polyglot path, end to end: an Express app and a FastAPI app with a
// compose file and Postgres are adopted by `--kind app`, and every env
// renders and passes `forge env new --check`: dev on the host / through
// compose, staging and prod on hosting with the managed database, the
// migration job and the secret. The claimable variant fits the claimable
// plan.
func TestNewApp_PolyglotAppsAdoptAndRender(t *testing.T) {
	testkit.Slow(t, "renders six envs through native KCL")

	node := nodeExpressApp(t)
	if err := adoptApp(t, node, "todo", false, false); err != nil {
		t.Fatal(err)
	}
	dev, err := RenderKCL(t.Context(), node, "dev")
	if err != nil {
		t.Fatalf("render node dev: %v", err)
	}
	api := dev.FindWorkload("api")
	if api == nil || !api.OnRuntime(RuntimeHost) || strings.Join(api.Spec.Command, " ") != "npm run dev" || api.HostEnv()["PORT"] == "" {
		t.Fatalf("node dev api = %+v, want npm run dev on the host with PORT", api)
	}
	for _, env := range []string{"staging", "prod"} {
		e, err := RenderKCL(t.Context(), node, env)
		if err != nil {
			t.Fatalf("render node %s: %v", env, err)
		}
		api := e.FindWorkload("api")
		if api == nil || !api.OnRuntime(RuntimeHosted) || api.Build.Type != "docker" || api.Spec.Probes == nil || !api.Spec.Probes.TCP {
			t.Fatalf("node %s api = %+v, want the hosted DockerBuild workload with a tcp probe", env, api)
		}
	}

	py := pythonComposeApp(t)
	if err := adoptApp(t, py, "notes", false, false); err != nil {
		t.Fatal(err)
	}
	dev, err = RenderKCL(t.Context(), py, "dev")
	if err != nil {
		t.Fatalf("render python dev: %v", err)
	}
	for _, name := range []string{"web", "db", "redis", "adminer"} {
		if w := dev.FindWorkload(name); w == nil || !w.OnRuntime(RuntimeCompose) {
			t.Errorf("python dev %s = %+v, want the compose service", name, w)
		}
	}
	for _, env := range []string{"staging", "prod"} {
		e, err := RenderKCL(t.Context(), py, env)
		if err != nil {
			t.Fatalf("render python %s: %v", env, err)
		}
		web := e.FindWorkload("web")
		if web == nil || !web.OnRuntime(RuntimeHosted) {
			t.Fatalf("python %s web = %+v, want hosted", env, web)
		}
		var dbRef, secret bool
		for _, v := range web.Spec.Env {
			dbRef = dbRef || (v.Name == "DATABASE_URL" && v.DatabaseRef != nil && v.DatabaseRef.Name == "notes")
		}
		secret = hasManagedSecret(web, "SESSION_SECRET")
		if !dbRef || !secret {
			t.Errorf("python %s web env %+v, want DATABASE_URL from the managed database and SESSION_SECRET from the store", env, web.Spec.Env)
		}
		if m := e.FindWorkload("migrate"); m == nil || m.Kind != "job" || len(m.Spec.Before) == 0 || strings.Join(m.Spec.Command, " ") != "alembic upgrade head" {
			t.Errorf("python %s migrate = %+v, want the pre-rollout alembic job", env, m)
		}
		if r := e.FindWorkload("redis"); r == nil || !r.OnRuntime(RuntimeHosted) || r.Build.Type != "" && r.Build.Type != "none" {
			t.Errorf("python %s redis = %+v, want the hosted third-party image", env, r)
		}
		if e.FindWorkload("db") != nil || e.FindWorkload("adminer") != nil {
			t.Errorf("python %s hosts a dev-only compose service", env)
		}
		if len(e.Databases) != 1 || e.Databases[0].Name != "notes" {
			t.Errorf("python %s databases = %+v, want notes", env, e.Databases)
		}
	}
	for _, root := range []string{node, py} {
		for _, env := range []string{"dev", "staging", "prod"} {
			if err := checkEnv(t.Context(), root, env, filepath.Join(root, "deploy", "kcl", env)); err != nil {
				t.Errorf("forge env new %s --check in %s: %v", env, filepath.Base(root), err)
			}
		}
	}

	// --claimable: prod fits the anonymous plan, with no secret values.
	claim := writeAppTree(t, map[string]string{
		"Dockerfile":       "FROM python:3.12-slim\nCOPY . .\nCMD [\"uvicorn\", \"app.main:app\", \"--port\", \"8000\"]\n",
		"requirements.txt": "fastapi\npsycopg2-binary\n",
		"manage.py":        "",
	})
	if err := adoptApp(t, claim, "claimed", true, false); err != nil {
		t.Fatal(err)
	}
	prod, err := RenderKCL(t.Context(), claim, "prod")
	if err != nil {
		t.Fatalf("render claimable prod: %v", err)
	}
	if !declaresClaimable(prod) {
		t.Fatal("claimable prod does not declare claimable = True")
	}
	if reasons := claimableIneligibility(prod, deploy.ClaimableLimitsFallback()); len(reasons) > 0 {
		t.Errorf("claimable prod does not fit the plan: %v", reasons)
	}
}

// A compose build.target naming a stage that is not the last reads that stage
// alone: a later stage's EXPOSE, CMD and ENV PORT are not the target's.
func TestParseDockerfile_TargetStageIsReadAlone(t *testing.T) {
	root := writeAppTree(t, map[string]string{
		"Dockerfile": "FROM node:20 AS dev\nENV PORT=5173\nEXPOSE 5173\nCMD [\"npm\", \"run\", \"dev\"]\n\n" +
			"FROM node:20-slim AS prod\nENV PORT=3000\nEXPOSE 3000\nCMD [\"node\", \"server.js\"]\n",
	})
	path := filepath.Join(root, "Dockerfile")
	dev, err := parseDockerfile(path, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(dev.expose) != "[5173]" || dev.envPort != 5173 || strings.Join(dev.cmd, " ") != "npm run dev" || dev.base != "node:20" {
		t.Errorf("target dev = %+v, want only the dev stage: EXPOSE 5173, PORT 5173, npm run dev, node:20", dev)
	}
	last, err := parseDockerfile(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(last.expose) != "[3000]" || strings.Join(last.cmd, " ") != "node server.js" {
		t.Errorf("no target = %+v, want the final stage: EXPOSE 3000, node server.js", last)
	}
}

// detectApp follows compose's build.target to the first of two stages.
func TestDetectApp_ComposeTargetIsAnEarlierStage(t *testing.T) {
	root := writeAppTree(t, map[string]string{
		"package.json": `{"name":"shop","scripts":{"start":"node server.js"}}`,
		"Dockerfile": "FROM node:20 AS dev\nEXPOSE 5173\nCMD [\"npm\", \"run\", \"dev\"]\n\n" +
			"FROM node:20-slim AS prod\nENV PORT=3000\nEXPOSE 3000\nCMD [\"node\", \"server.js\"]\n",
		"docker-compose.yml": "services:\n  web:\n    build:\n      context: .\n      target: dev\n",
	})
	det, err := detectApp(root, "shop", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(det.spec.Workloads) != 1 {
		t.Fatalf("workloads = %+v, want one", det.spec.Workloads)
	}
	w := det.spec.Workloads[0]
	if w.Port != 5173 {
		t.Errorf("web port = %d, want 5173 from the dev stage the compose target names", w.Port)
	}
}

// A compose worker that shares the web Dockerfile runs its compose command on
// every runtime, not the image's web CMD; and the migration job reads the
// database URL under the name its owner does, with the owner's secrets.
func TestNewApp_ComposeCommandAndMigrationEnvReachHosting(t *testing.T) {
	root := writeAppTree(t, map[string]string{
		"requirements.txt": "flask==3.0.0\ngunicorn==22.0.0\ncelery==5.4.0\nalembic==1.13.2\n",
		"app/main.py":      "from flask import Flask\napp = Flask(__name__)\n",
		"Dockerfile":       "FROM python:3.12-slim\nWORKDIR /srv\nCOPY . .\nCMD [\"gunicorn\", \"-b\", \"0.0.0.0:8000\", \"app.main:app\"]\n",
		"docker-compose.yml": `services:
  web:
    build: .
    ports:
      - "8000:8000"
    environment:
      SQLALCHEMY_DATABASE_URI: postgresql://app:app@db:5432/shop
      SECRET_KEY: dev-secret
  jobs:
    build: .
    command: celery -A app.tasks worker
    environment:
      SQLALCHEMY_DATABASE_URI: postgresql://app:app@db:5432/shop
      SECRET_KEY: dev-secret
      FLASK_APP: app.main
  migrate:
    build: .
    command: alembic upgrade head
  db:
    image: postgres:16
    environment:
      POSTGRES_USER: app
      POSTGRES_PASSWORD: app
      POSTGRES_DB: shop
`,
	})
	det, err := detectApp(root, "shop", false)
	if err != nil {
		t.Fatal(err)
	}
	files, err := generator.RenderAppScaffold(det.spec, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = string(f.Content)
	}
	wk := got["deploy/kcl/workloads.k"]
	if !strings.Contains(wk, `args = ["celery", "-A", "app.tasks", "worker"]`) || !strings.Contains(wk, "name = \"jobs\"\n    kind = \"worker\"") {
		t.Errorf("the jobs worker lost its compose command:\n%s", wk)
	}
	// args, not command: compose's command replaces CMD and keeps ENTRYPOINT.
	if strings.Count(wk, "\n    args = ") != 1 || strings.Count(wk, "\n    command = ") != 1 { // jobs; the migrate job
		t.Errorf("only the overridden worker carries args, and only the migrate job a command:\n%s", wk)
	}
	if !strings.Contains(wk, "    env = jobs.env\n") {
		t.Errorf("the migrate job must carry its owner's literal env:\n%s", wk)
	}
	prod := got["deploy/kcl/prod/main.k"]
	var migrate string
	for _, l := range strings.Split(prod, "\n") {
		if strings.Contains(l, "wl.migrate") {
			migrate = l
		}
	}
	for _, want := range []string{"env.DATABASE_URL = _database_url", "env.SQLALCHEMY_DATABASE_URI = _database_url", `env.SECRET_KEY = forge.ManagedSecret {name = "SECRET_KEY"}`} {
		if !strings.Contains(migrate, want) {
			t.Errorf("prod's migrate job lacks %s: %q", want, migrate)
		}
	}
}

// A compose string command is argv, not a shell script: words and quotes
// split, `&&` and `$VAR` stay literal, as compose passes them to the
// entrypoint.
func TestSplitComposeCommand(t *testing.T) {
	for in, want := range map[string][]string{
		`celery -A app.tasks worker`:            {"celery", "-A", "app.tasks", "worker"},
		`sh -c "migrate && serve --port $PORT"`: {"sh", "-c", "migrate && serve --port $PORT"},
		`echo 'a b' c\ d`:                       {"echo", "a b", "c d"},
		`  spaced   out `:                       {"spaced", "out"},
		`unterminated "quote`:                   {"unterminated", `"quote`},
	} {
		if got := splitComposeCommand(in); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("splitComposeCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

// A credential travels in a value as often as in a name: an authenticated
// URL under any name becomes a secret, never a literal in a generated file
// or the --dry-run output (which prints those files and the summary), in
// claimable prod too.
func TestDetectApp_CredentialURLsBecomeSecrets(t *testing.T) {
	root := writeAppTree(t, map[string]string{
		"Dockerfile": "FROM python:3.12-slim\nCOPY . .\nCMD [\"gunicorn\", \"-b\", \"0.0.0.0:8000\", \"app:app\"]\n",
		"docker-compose.yml": `services:
  web:
    build: .
    ports:
      - "8000:8000"
    environment:
      REDIS_URL: redis://default:hunter2redis@cache:6379/0
      BROKER: amqp://guest:hunter2amqp@mq:5672//
      UPSTREAM: https://hunter2token@api.example.com/v1
      SEARCH_ENDPOINT: https://search.example.com/q?api_key=hunter2query
      CACHE_HOST: redis://cache:6379/0
`,
	})
	for _, claimable := range []bool{false, true} {
		det, err := detectApp(root, "shop", claimable)
		if err != nil {
			t.Fatal(err)
		}
		w := det.spec.Workloads[0]
		if strings.Join(w.Secrets, ",") != "REDIS_URL,BROKER,UPSTREAM,SEARCH_ENDPOINT" {
			t.Errorf("secrets = %v, want every authenticated URL", w.Secrets)
		}
		if w.Env["CACHE_HOST"] != "redis://cache:6379/0" {
			t.Errorf("env = %v, want the credential-free URL kept literal", w.Env)
		}
		files, err := generator.RenderAppScaffold(det.spec, "")
		if err != nil {
			t.Fatal(err)
		}
		out := strings.Join(append(append(det.detected, det.assumed...), det.skipped...), "\n")
		for _, f := range files {
			out += "\n" + string(f.Content)
		}
		if strings.Contains(out, "hunter2") {
			t.Errorf("claimable=%v: a credential reached the generated files or summary:\n%s", claimable, out)
		}
	}
}

// A compose build arg is baked into the image, and workloads.k is
// source-controlled: one that carries a credential, by name or by value, is
// left out of the build and named (never valued) in the summary, for adoption
// and for a claimable deploy alike. A plain arg is kept.
func TestDetectApp_CredentialBuildArgsAreWithheld(t *testing.T) {
	root := writeAppTree(t, map[string]string{
		"Dockerfile": "FROM python:3.12-slim\nARG NODE_ENV\nCOPY . .\nCMD [\"gunicorn\", \"-b\", \"0.0.0.0:8000\", \"app:app\"]\n",
		"docker-compose.yml": `services:
  web:
    build:
      context: .
      args:
        NODE_ENV: production
        API_TOKEN: hunter2token
        NPM_REGISTRY: https://deploy:hunter2npm@npm.example.com/
    ports:
      - "8000:8000"
`,
	})
	for _, claimable := range []bool{false, true} {
		det, err := detectApp(root, "shop", claimable)
		if err != nil {
			t.Fatal(err)
		}
		w := det.spec.Workloads[0]
		if w.Build == nil || len(w.Build.Args) != 1 || w.Build.Args["NODE_ENV"] != "production" {
			t.Errorf("claimable=%v: build args = %v, want only NODE_ENV kept", claimable, w.Build)
		}
		files, err := generator.RenderAppScaffold(det.spec, "")
		if err != nil {
			t.Fatal(err)
		}
		summary := strings.Join(append(append(det.detected, det.assumed...), det.skipped...), "\n")
		out := summary
		for _, f := range files {
			out += "\n" + string(f.Content)
		}
		if strings.Contains(out, "hunter2") {
			t.Errorf("claimable=%v: a credential build arg reached the generated files or summary:\n%s", claimable, out)
		}
		for _, k := range []string{"API_TOKEN", "NPM_REGISTRY"} {
			if !strings.Contains(summary, "build arg "+k+" carries a credential") {
				t.Errorf("claimable=%v: the summary does not name withheld build arg %s:\n%s", claimable, k, summary)
			}
		}
	}
}

// A compose command carrying a credential is not adopted: its argv would be
// copied into workloads.k (a workload's args or the migrate job) and printed,
// and dropping the argument would change the command. Adoption refuses,
// naming the service and the argument's position, never its value, for
// adoption and for a claimable deploy alike. A command that reads the
// credential from the environment, or names a file, is adopted.
func TestDetectApp_CredentialCommandsAreRefused(t *testing.T) {
	compose := func(command string) string {
		return `services:
  web:
    build: .
    ports:
      - "8000:8000"
  jobs:
    build: .
    command: ` + command + `
  migrate:
    build: .
    command: alembic upgrade head
`
	}
	for _, tc := range []struct {
		name, command string
		refused       bool
	}{
		{name: "broker URL with userinfo", command: "celery -A app worker --broker amqp://guest:hunter2amqp@mq:5672//", refused: true},
		{name: "token flag value", command: "worker --api-token hunter2token", refused: true},
		{name: "token flag=value", command: "worker --api-token=hunter2token", refused: true},
		{name: "query credential", command: `["worker", "--upstream", "https://api.example.com/v1?api_key=hunter2query"]`, refused: true},
		{name: "shell-wrapped password", command: `["sh", "-c", "worker --password hunter2pw"]`, refused: true},
		{name: "shell-wrapped broker URL", command: `["bash", "-c", "exec celery -A app worker --broker amqp://u:hunter2amqp@mq//"]`, refused: true},
		{name: "a literal password containing a dollar", command: "worker --password hunter2$dollar", refused: true},
		{name: "from the environment", command: "celery -A app worker --broker ${BROKER_URL}"},
		{name: "shell-wrapped, from the environment", command: `["sh", "-c", "worker --password \"$$WORKER_PASSWORD\""]`},
		{name: "an empty default", command: "worker --password ${WORKER_PASSWORD:-}"},
		{name: "a default that is itself a reference", command: "worker --password ${WORKER_PASSWORD:-${FALLBACK_PASSWORD}}"},
		{name: "a literal password default", command: "worker --password ${WORKER_PASSWORD:-hunter2default}", refused: true},
		{name: "a literal default under :=", command: "worker --password=${WORKER_PASSWORD:=hunter2assign}", refused: true},
		{name: "a literal alternative under :+", command: "worker --password ${WORKER_PASSWORD:+hunter2alt}", refused: true},
		{name: "a literal default without the colon", command: "worker --password ${WORKER_PASSWORD-hunter2plain}", refused: true},
		{name: "a shell-wrapped literal default", command: `["sh", "-c", "worker --password $${WORKER_PASSWORD:-hunter2shell}"]`, refused: true},
		{name: "a secret file", command: "worker --token-file /run/secrets/token"},
		{name: "a numeric password", command: "worker --password 20262026", refused: true},
		{name: "a numeric token flag=value", command: "worker --api-token=20262026", refused: true},
		{name: "a numeric env-style password", command: "worker DB_PASSWORD=20262026", refused: true},
		{name: "a boolean-looking password", command: "worker --password true", refused: true},
		{name: "a numeric password under a word containing a setting word", command: "worker --user-password 20262026", refused: true},
		{name: "a hyphenated API key flag", command: "worker --api-key hunter2key", refused: true},
		{name: "a hyphenated API key flag=value", command: "worker --api-key=hunter2key", refused: true},
		{name: "a hyphenated private key flag", command: "worker --private-key hunter2pk", refused: true},
		{name: "a shell-wrapped hyphenated API key", command: `["sh", "-c", "worker --api-key=hunter2key"]`, refused: true},
		{name: "a hyphenated query credential", command: `["worker", "--upstream", "https://api.example.com/v1?api-key=hunter2query"]`, refused: true},
		{name: "a password starting with a slash", command: "worker --password /hunter2slash", refused: true},
		{name: "a password starting with a dot", command: "worker --password=.hunter2dot", refused: true},
		{name: "a password starting with a tilde", command: "worker --password ~hunter2tilde", refused: true},
		{name: "a password starting with a dash", command: "worker --password -hunter2dash", refused: true},
		{name: "a password file", command: "worker --password-file ./secrets/pw"},
		{name: "a key from the environment", command: "worker --api-key ${API_KEY}"},
		{name: "a token lifetime as a duration", command: "worker --token-ttl 1h"},
		{name: "a setting flag followed by another flag", command: "worker --max-tokens --verbose"},
		{name: "a number", command: "worker --max-tokens 512"},
		{name: "a camelCase setting", command: "worker --passwordMinLength=12"},
		{name: "a token lifetime", command: "worker --token-ttl 3600"},
		{name: "a credential switch", command: "worker --require-password=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeAppTree(t, map[string]string{
				"requirements.txt":   "flask==3.0.0\ngunicorn==22.0.0\ncelery==5.4.0\n",
				"Dockerfile":         "FROM python:3.12-slim\nCOPY . .\nCMD [\"gunicorn\", \"-b\", \"0.0.0.0:8000\", \"app:app\"]\n",
				"docker-compose.yml": compose(tc.command),
			})
			for _, claimable := range []bool{false, true} {
				det, err := detectApp(root, "shop", claimable)
				if !tc.refused {
					if err != nil {
						t.Fatalf("claimable=%v: %v", claimable, err)
					}
					continue
				}
				if err == nil {
					files, _ := generator.RenderAppScaffold(det.spec, "")
					t.Fatalf("claimable=%v: adopted a credential-bearing command (%d files)", claimable, len(files))
				}
				if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "20262026") || !strings.Contains(err.Error(), `compose service "jobs"`) {
					t.Errorf("claimable=%v: error %q must name the service and never the value", claimable, err)
				}
			}
		})
	}
	// A migration service's command is refused the same way.
	root := writeAppTree(t, map[string]string{
		"requirements.txt":   "flask==3.0.0\ngunicorn==22.0.0\n",
		"Dockerfile":         "FROM python:3.12-slim\nCOPY . .\nCMD [\"gunicorn\", \"-b\", \"0.0.0.0:8000\", \"app:app\"]\n",
		"docker-compose.yml": "services:\n  web:\n    build: .\n    ports:\n      - \"8000:8000\"\n  migrate:\n    build: .\n    command: alembic -x dburl=postgres://app:hunter2pg@db/app upgrade head\n",
	})
	if _, err := detectApp(root, "shop", false); err == nil || strings.Contains(err.Error(), "hunter2") || !strings.Contains(err.Error(), `compose service "migrate"`) {
		t.Errorf("migration command: err = %v, want a value-free refusal naming the service", err)
	}
	// ...and so is one wrapped in a shell, in claimable mode too.
	root = writeAppTree(t, map[string]string{
		"requirements.txt":   "flask==3.0.0\ngunicorn==22.0.0\n",
		"Dockerfile":         "FROM python:3.12-slim\nCOPY . .\nCMD [\"gunicorn\", \"-b\", \"0.0.0.0:8000\", \"app:app\"]\n",
		"docker-compose.yml": "services:\n  web:\n    build: .\n    ports:\n      - \"8000:8000\"\n  migrate:\n    build: .\n    command: [\"sh\", \"-c\", \"alembic -x db_password=hunter2pg upgrade head\"]\n",
	})
	for _, claimable := range []bool{false, true} {
		if _, err := detectApp(root, "shop", claimable); err == nil || strings.Contains(err.Error(), "hunter2") || !strings.Contains(err.Error(), `compose service "migrate"`) {
			t.Errorf("shell-wrapped migration command, claimable=%v: err = %v, want a value-free refusal naming the service", claimable, err)
		}
	}
	// ...and a literal fallback in a migration command, direct or shell-wrapped.
	for _, command := range []string{
		"alembic -x db_password=${DB_PASSWORD:-hunter2pgdefault} upgrade head",
		`["sh", "-c", "alembic -x db_password=$${DB_PASSWORD:-hunter2pgshell} upgrade head"]`,
	} {
		root = writeAppTree(t, map[string]string{
			"requirements.txt":   "flask==3.0.0\ngunicorn==22.0.0\n",
			"Dockerfile":         "FROM python:3.12-slim\nCOPY . .\nCMD [\"gunicorn\", \"-b\", \"0.0.0.0:8000\", \"app:app\"]\n",
			"docker-compose.yml": "services:\n  web:\n    build: .\n    ports:\n      - \"8000:8000\"\n  migrate:\n    build: .\n    command: " + command + "\n",
		})
		for _, claimable := range []bool{false, true} {
			if _, err := detectApp(root, "shop", claimable); err == nil || strings.Contains(err.Error(), "hunter2") || !strings.Contains(err.Error(), `compose service "migrate"`) {
				t.Errorf("migration command %q, claimable=%v: err = %v, want a value-free refusal naming the service", command, claimable, err)
			}
		}
	}
}

// A worker sharing the web Dockerfile inherits its EXPOSE and ENV PORT, but
// its compose command serves neither: it is a worker, with no public port
// and no TCP probe.
func TestDetectApp_SharedImageWorkerIgnoresWebPort(t *testing.T) {
	root := writeAppTree(t, map[string]string{
		"requirements.txt": "flask==3.0.0\ngunicorn==22.0.0\ncelery==5.4.0\n",
		"Dockerfile":       "FROM python:3.12-slim\nENV PORT=8000\nEXPOSE 8000\nCOPY . .\nCMD [\"gunicorn\", \"-b\", \"0.0.0.0:8000\", \"app:app\"]\n",
		"docker-compose.yml": `services:
  web:
    build: .
    ports:
      - "8000:8000"
  jobs:
    build: .
    command: celery -A app.tasks worker
`,
	})
	det, err := detectApp(root, "shop", false)
	if err != nil {
		t.Fatal(err)
	}
	var jobs *generator.AppWorkload
	for i := range det.spec.Workloads {
		if det.spec.Workloads[i].Name == "jobs" {
			jobs = &det.spec.Workloads[i]
		}
	}
	if jobs == nil || jobs.Kind != "worker" || jobs.Port != 0 || jobs.Expose {
		t.Fatalf("jobs = %+v, want a worker with no port, not exposed", jobs)
	}
	files, err := generator.RenderAppScaffold(det.spec, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path != "deploy/kcl/workloads.k" {
			continue
		}
		body := string(f.Content)
		i := strings.Index(body, "name = \"jobs\"")
		if i < 0 {
			t.Fatalf("no jobs workload:\n%s", body)
		}
		block := body[i:]
		if j := strings.Index(block[1:], "name = \""); j >= 0 {
			block = block[:j+1]
		}
		if strings.Contains(block, "port") || strings.Contains(strings.ToLower(block), "tcp") {
			t.Errorf("the worker carries a port or tcp probe:\n%s", block)
		}
	}
}

// A credential in a URL's query is recognized under its hyphenated spelling
// too, whatever the variable holding the URL is called.
func TestCredentialValueRecognizesHyphenatedQueryKeys(t *testing.T) {
	for _, v := range []string{"https://api.example.com/v1?api-key=x", "https://s3.example.com/b?access-key=x", "https://api.example.com/v1?api_key=x"} {
		if !credentialValue(v) {
			t.Errorf("credentialValue(%q) = false, want true", v)
		}
	}
	if credentialValue("https://api.example.com/v1?monkey=x") {
		t.Error("credentialValue: a query parameter that only ends in key is not a credential")
	}
}

// With no compose file, dev runs the Dockerfile's CMD or the Procfile's web
// process on the host, copied into deploy/kcl/dev/main.k and the summary. A
// credential in either is refused the way a compose command's is: by naming
// the workload, never the value.
func TestDetectApp_CredentialDevCommandsAreRefused(t *testing.T) {
	dockerfile := func(cmd string) string {
		return "FROM python:3.12-slim\nCOPY . .\nEXPOSE 8000\nCMD " + cmd + "\n"
	}
	for _, tc := range []struct {
		name    string
		files   map[string]string
		refused bool
	}{
		{name: "a token flag in the CMD", refused: true, files: map[string]string{
			"Dockerfile": dockerfile(`["gunicorn", "-b", "0.0.0.0:8000", "app:app", "--api-token", "hunter2cmd"]`),
		}},
		{name: "a credentialed URL in a shell-form CMD", refused: true, files: map[string]string{
			"Dockerfile": dockerfile(`celery -A app worker --broker amqp://u:hunter2amqp@mq//`),
		}},
		{name: "a password in the Procfile's web process", refused: true, files: map[string]string{
			"Dockerfile": dockerfile(`["gunicorn", "-b", "0.0.0.0:8000", "app:app"]`),
			"Procfile":   "web: gunicorn app:app --password=hunter2proc\n",
		}},
		{name: "a CMD reading its credential from the environment", files: map[string]string{
			"Dockerfile": dockerfile(`["sh", "-c", "gunicorn -b 0.0.0.0:8000 app:app --api-token $API_TOKEN"]`),
		}},
		{name: "a plain Procfile web process", files: map[string]string{
			"Dockerfile": dockerfile(`["gunicorn", "-b", "0.0.0.0:8000", "app:app"]`),
			"Procfile":   "web: gunicorn app:app --bind 0.0.0.0:8000\n",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"requirements.txt": "flask==3.0.0\ngunicorn==22.0.0\n"}
			for k, v := range tc.files {
				files[k] = v
			}
			root := writeAppTree(t, files)
			for _, claimable := range []bool{false, true} {
				det, err := detectApp(root, "shop", claimable)
				if !tc.refused {
					if err != nil {
						t.Fatalf("claimable=%v: %v", claimable, err)
					}
					continue
				}
				if err == nil {
					files, _ := generator.RenderAppScaffold(det.spec, "")
					t.Fatalf("claimable=%v: adopted a credential-bearing dev command (%d files)", claimable, len(files))
				}
				if strings.Contains(err.Error(), "hunter2") || !strings.Contains(err.Error(), "workload api") {
					t.Errorf("claimable=%v: error %q must name the workload and never the value", claimable, err)
				}
			}
		})
	}
}
