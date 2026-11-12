package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/generator"
)

// What `forge project new --in-place --kind app` reads off an existing app
// to fill the adopt skill's templates: its Dockerfile(s) and compose
// services, the port each listens on, its Postgres, its migration command
// and its dev command. Everything it found is reported as detected, and
// every value it had to guess as assumed, so the summary is a review list.
// It reads; it never writes a file of the app's.

// appDetection is what --kind app found.
type appDetection struct {
	spec generator.AppSpec
	// detected / assumed / skipped are the summary's three lists.
	detected []string
	assumed  []string
	skipped  []string
	// secrets are the credential names staging and prod deliver from the
	// store, each set once with `forge secret set`.
	secrets []string
	// installs are the app's own install commands dev needs on the host.
	installs []string
	// dockerignoreMissing are entries .dockerignore should list.
	dockerignoreMissing []string
}

// composeFileNames are the compose files `docker compose` reads by default,
// in its order.
var composeFileNames = []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}

// findComposeFile is the compose file at root ("" when none).
func findComposeFile(root string) string {
	for _, n := range composeFileNames {
		if fileExists(filepath.Join(root, n)) {
			return n
		}
	}
	return ""
}

// findDockerfiles lists the Dockerfiles an app's workloads build from: one
// at the root, and one in each top-level directory (a monorepo's api/,
// worker/), relative to root.
func findDockerfiles(root string) []string {
	var out []string
	if fileExists(filepath.Join(root, "Dockerfile")) {
		out = append(out, "Dockerfile")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || skipAppDir[e.Name()] {
			continue
		}
		if fileExists(filepath.Join(root, e.Name(), "Dockerfile")) {
			out = append(out, e.Name()+"/Dockerfile")
		}
	}
	return out
}

var skipAppDir = map[string]bool{"node_modules": true, "vendor": true, "deploy": true, "dist": true, "build": true, "out": true, "target": true, "venv": true, "testdata": true, "examples": true}

// hasAppToAdopt reports whether root holds something --kind app adopts: a
// Dockerfile, or a compose service it builds.
func hasAppToAdopt(root string) bool {
	if len(findDockerfiles(root)) > 0 {
		return true
	}
	if f := findComposeFile(root); f != "" {
		if c, err := parseComposeFile(filepath.Join(root, f)); err == nil {
			for _, s := range c.services {
				if s.build != nil {
					return true
				}
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// compose
// ---------------------------------------------------------------------------

type composeService struct {
	name        string
	build       *generator.AppBuild
	image       string
	ports       []int
	environment map[string]string
	envOrder    []string
	dependsOn   []string
	command     []string
	commandRaw  string
	// withheldArgs are build args left out of the build because they carry
	// a credential (credentialBuildArg); named, never valued, in the summary.
	withheldArgs []string
}

type composeFile struct {
	services []composeService
}

func parseComposeFile(path string) (*composeFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	out := &composeFile{}
	names := make([]string, 0, len(raw.Services))
	for n := range raw.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		node := raw.Services[n]
		var svc map[string]yaml.Node
		if err := node.Decode(&svc); err != nil {
			return nil, fmt.Errorf("parse %s: service %q: %w", filepath.Base(path), n, err)
		}
		s := composeService{name: n, environment: map[string]string{}}
		if b, ok := svc["build"]; ok {
			s.build, s.withheldArgs = composeBuild(b)
		}
		if i, ok := svc["image"]; ok {
			s.image = i.Value
		}
		if p, ok := svc["ports"]; ok {
			s.ports = append(s.ports, composePorts(p)...)
		}
		if p, ok := svc["expose"]; ok {
			s.ports = append(s.ports, composePorts(p)...)
		}
		if e, ok := svc["environment"]; ok {
			s.environment, s.envOrder = composeEnvironment(e)
		}
		if d, ok := svc["depends_on"]; ok {
			s.dependsOn = composeDependsOn(d)
		}
		if c, ok := svc["command"]; ok {
			s.command, s.commandRaw = composeCommand(c)
		}
		out.services = append(out.services, s)
	}
	return out, nil
}

// refuseCredentialCommands refuses to adopt a compose file whose service
// command carries a credential. Adoption copies a command into workloads.k
// (a workload's args, the migrate job) and prints it in the summary, both of
// which are source-controlled or shared, and dropping the argument instead
// would change what the command does. So adoption stops and names the
// service and the argument by position only, never by value.
func refuseCredentialCommands(composeName string, c *composeFile) error {
	for _, s := range c.services {
		if i := credentialArg(s.command); i >= 0 {
			return fmt.Errorf("compose service %q in %s: argument %d of its command carries a credential, so forge will not copy it into workloads.k or print it: "+
				"have the command read it from an environment variable compose sets (for example ${BROKER_URL}, set from a secret), then adopt again", s.name, composeName, i+1)
		}
	}
	return nil
}

// credentialArg is the index of the first argument of argv that carries a
// credential, or -1: a value credentialValue recognizes (a URL with userinfo
// or a credential query parameter), a `--name=value` or `NAME=value` whose
// name credentialArgName recognizes, or the value after such a flag. An
// argument that is itself a script (`sh -c "worker --password x"`: it holds
// whitespace) is split into words and judged the same way, so a credential
// wrapped in a shell is found too.
func credentialArg(argv []string) int {
	for i, a := range argv {
		if credentialValue(a) {
			return i
		}
		if strings.ContainsAny(a, " \t\n") && credentialArg(splitComposeCommand(a)) >= 0 {
			return i
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !credentialArgName(name) {
			continue
		}
		if hasValue && secretShaped(name, value) {
			return i
		}
		if !hasValue && strings.HasPrefix(a, "-") && i+1 < len(argv) && flagValue(name, argv[i+1]) && secretShaped(name, argv[i+1]) {
			return i + 1
		}
	}
	return -1
}

// credentialArgName reports an argument name credentialName recognizes,
// reading a flag's '-' as the '_' an environment variable would spell it
// with: `--api-key` and `--private-key` are API_KEY and PRIVATE_KEY.
func credentialArgName(name string) bool {
	return credentialName.MatchString(strings.ReplaceAll(name, "-", "_"))
}

// flagValue reports whether next, the argument after the credential-named
// flag name, is that flag's value rather than the next flag. A literal
// password may begin with '-', so for a flag that takes the credential itself
// it always is. Only a setting or a file flag (`--token-ttl`, `--token-file`)
// is followed by another flag instead, and a lone "-" (stdin) is never a
// credential.
func flagValue(name, next string) bool {
	if next == "-" {
		return false
	}
	if strings.HasPrefix(next, "-") {
		return !settingQualified(name) && !fileValued(name)
	}
	return true
}

// envReferenceRE matches a value that is exactly one environment-variable
// reference: $NAME, ${NAME}, or ${NAME<op>word} for the expansion operators
// that carry a word (:- - := = :+ + :? ?), each optionally in compose's
// escaped $$ form, which it leaves for the container's shell. Group 2 is the
// operator, group 3 the word.
var envReferenceRE = regexp.MustCompile(`^\$\$?(?:\{[A-Za-z_][A-Za-z0-9_]*(?:(:?[-=?+])(.*))?\}|[A-Za-z_][A-Za-z0-9_]*)$`)

// envReference reports whether v names no credential itself: it is exactly
// one environment reference whose word, if any, is empty or is itself an
// environment reference. `${PASSWORD:-hunter2}` carries a literal fallback,
// which is the credential the command would run with.
func envReference(v string) bool {
	m := envReferenceRE.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	return m[1] == "" || m[2] == "" || envReference(m[2])
}

// secretShaped reports whether v, the value of the credential-named argument
// name, could be the credential itself. Everything is, except: an empty
// value; exactly a reference to an environment variable; a path, after a
// flag whose name says it takes a file (fileValued: `--token-file
// /run/secrets/token`), never inferred from the value's first character alone,
// since a password may begin with '/'; and a number, boolean or duration
// after a flag whose name says it is a setting about credentials
// (settingQualified: `--max-tokens 512`, `--token-ttl 1h`), never after the
// credential itself (`--password 123456`). A literal that merely contains '$'
// is still a credential.
func secretShaped(name, v string) bool {
	if v == "" || envReference(v) {
		return false
	}
	if fileValued(name) && strings.ContainsAny(v[:1], "/.~") {
		return false
	}
	if !settingQualified(name) {
		return true
	}
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		return false
	}
	if _, err := strconv.ParseBool(v); err == nil {
		return false
	}
	if _, err := time.ParseDuration(v); err == nil {
		return false
	}
	return true
}

// fileWords are the words that make a credential-named argument take a file
// holding the credential rather than the credential: `--token-file`,
// `--password-path`, `--key-dir`, `--client-cert`.
var fileWords = map[string]bool{
	"file": true, "path": true, "dir": true, "location": true,
	"pem": true, "cert": true, "crt": true,
}

// fileValued reports whether the argument name holds one of fileWords as a
// whole word (argWords).
func fileValued(name string) bool {
	return hasArgWord(name, fileWords)
}

// settingWords are the words that make a credential-named argument a setting
// whose value is a count, a limit, a duration or a switch, not the credential:
// `--max-tokens`, `--token-ttl`, `--password-min-length`, `--use-api-key`.
var settingWords = map[string]bool{
	"tokens": true, "max": true, "min": true, "limit": true, "size": true,
	"count": true, "len": true, "length": true, "ttl": true, "timeout": true,
	"expiry": true, "expires": true, "lifetime": true, "age": true,
	"bits": true, "rounds": true, "iterations": true, "enable": true,
	"enabled": true, "disable": true, "disabled": true, "require": true,
	"required": true, "allow": true, "use": true, "skip": true,
	"verify": true, "no": true,
}

// settingQualified reports whether the argument name holds one of
// settingWords as a whole word (argWords): `--user-password` is not
// qualified by "use", `--maxTokens` is by "max".
func settingQualified(name string) bool {
	return hasArgWord(name, settingWords)
}

// hasArgWord reports whether one of name's words (argWords) is in words.
func hasArgWord(name string, words map[string]bool) bool {
	for _, w := range argWords(name) {
		if words[strings.ToLower(w)] {
			return true
		}
	}
	return false
}

// argWords splits an argument name into words on '-', '_', '.' and camelCase.
func argWords(name string) []string {
	var words []string
	start := 0
	for i := 1; i <= len(name); i++ {
		boundary := i == len(name) || strings.ContainsRune("-_.", rune(name[i])) ||
			(name[i] >= 'A' && name[i] <= 'Z' && name[i-1] >= 'a' && name[i-1] <= 'z')
		if !boundary {
			continue
		}
		if w := strings.Trim(name[start:i], "-_."); w != "" {
			words = append(words, w)
		}
		start = i
	}
	return words
}

// credentialBuildArg reports a build arg that carries a credential, by name
// or by value. A build arg is baked into the image and its history, and
// workloads.k is source-controlled, so such an arg is never copied: it
// belongs in a build secret (`RUN --mount=type=secret`), set by hand.
func credentialBuildArg(k, v string) bool {
	return credentialName.MatchString(k) || credentialValue(v)
}

// composeBuildMapping reads a compose `build:` mapping into b; false when it
// is not one. withheld names the args it left out (credentialBuildArg).
func composeBuildMapping(n yaml.Node, b *generator.AppBuild) (withheld []string, ok bool) {
	var m map[string]yaml.Node
	if n.Decode(&m) != nil {
		return nil, false
	}
	if c, ok := m["context"]; ok {
		b.Context = c.Value
	}
	if d, ok := m["dockerfile"]; ok {
		b.Dockerfile = d.Value
	}
	if t, ok := m["target"]; ok {
		b.Target = t.Value
	}
	if a, ok := m["args"]; ok {
		args, order := composeEnvironment(a)
		for _, k := range order {
			v := args[k]
			switch {
			case strings.Contains(v, "${"):
				delete(args, k)
			case credentialBuildArg(k, v):
				delete(args, k)
				withheld = append(withheld, k)
			}
		}
		if len(args) > 0 {
			b.Args = args
		}
	}
	return withheld, true
}

func composeBuild(n yaml.Node) (*generator.AppBuild, []string) {
	b := &generator.AppBuild{Dockerfile: "Dockerfile"}
	var withheld []string
	if n.Kind == yaml.ScalarNode {
		b.Context = n.Value
	} else {
		var ok bool
		if withheld, ok = composeBuildMapping(n, b); !ok {
			return nil, nil
		}
	}
	b.Context = cleanRel(b.Context)
	// compose resolves dockerfile against the context; forge resolves it
	// against the project root.
	b.Dockerfile = cleanRel(filepath.Join(b.Context, b.Dockerfile))
	if b.Context == "." {
		b.Context = ""
	}
	return b, withheld
}

func cleanRel(p string) string {
	if p == "" {
		return "."
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// composePorts reads the CONTAINER side of each `ports:` / `expose:` entry:
// "3000", "8080:3000", "127.0.0.1:8080:3000/tcp", or {target: 3000}.
func composePorts(n yaml.Node) []int {
	var out []int
	for _, item := range n.Content {
		var v string
		if item.Kind == yaml.MappingNode {
			var m map[string]yaml.Node
			if item.Decode(&m) == nil {
				v = m["target"].Value
			}
		} else {
			v = item.Value
		}
		v = strings.SplitN(v, "/", 2)[0]
		if i := strings.LastIndex(v, ":"); i >= 0 {
			v = v[i+1:]
		}
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
			out = append(out, p)
		}
	}
	return out
}

// composeEnvironment reads a map or a `K=V` list.
func composeEnvironment(n yaml.Node) (map[string]string, []string) {
	out := map[string]string{}
	var order []string
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			out[k] = n.Content[i+1].Value
			order = append(order, k)
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			k, v, ok := strings.Cut(item.Value, "=")
			if !ok {
				v = "${" + k + "}" // `- NAME` passes the shell's value through
			}
			out[k] = v
			order = append(order, k)
		}
	}
	return out, order
}

func composeDependsOn(n yaml.Node) []string {
	var out []string
	switch n.Kind {
	case yaml.SequenceNode:
		for _, item := range n.Content {
			out = append(out, item.Value)
		}
	case yaml.MappingNode:
		for i := 0; i < len(n.Content); i += 2 {
			out = append(out, n.Content[i].Value)
		}
	}
	return out
}

func composeCommand(n yaml.Node) ([]string, string) {
	if n.Kind == yaml.ScalarNode {
		return splitComposeCommand(n.Value), n.Value
	}
	var argv []string
	for _, item := range n.Content {
		argv = append(argv, item.Value)
	}
	return argv, strings.Join(argv, " ")
}

// splitComposeCommand splits a compose string `command` into argv the way
// compose does: shell-style words and quotes, but no shell runs it, so `&&`,
// pipes and `$VAR` stay literal arguments to the image's entrypoint. An
// unterminated quote (which compose itself refuses) falls back to whitespace
// fields.
func splitComposeCommand(s string) []string {
	var out []string
	var cur strings.Builder
	inWord, quote, escaped := false, rune(0), false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, inWord = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 || escaped {
		return strings.Fields(s)
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}

// imageRepo is an image reference's repository, lowercase, with no tag,
// digest or registry host: "docker.io/library/postgres:16" → "postgres".
func imageRepo(image string) string {
	ref := strings.ToLower(image)
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	parts := strings.Split(ref, "/")
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		parts = parts[1:]
	}
	if len(parts) > 1 && parts[0] == "library" {
		parts = parts[1:]
	}
	return strings.Join(parts, "/")
}

func isPostgresImage(image string) bool {
	repo := imageRepo(image)
	switch repo {
	case "postgres", "postgis/postgis", "bitnami/postgresql", "timescale/timescaledb", "pgvector/pgvector", "supabase/postgres":
		return true
	}
	return strings.HasSuffix(repo, "/postgres") || strings.HasSuffix(repo, "/postgresql")
}

// cacheImages are third-party services that hold nothing worth keeping, so
// hosting can run them as an ordinary workload.
var cacheImages = map[string]bool{"redis": true, "valkey/valkey": true, "memcached": true, "bitnami/redis": true}

// devToolImages are dev conveniences a deployed env never runs.
var devToolImages = map[string]bool{"adminer": true, "dpage/pgadmin4": true, "mailhog/mailhog": true, "axllent/mailpit": true, "phpmyadmin": true, "phpmyadmin/phpmyadmin": true, "maildev/maildev": true}

// normalizeImage gives a third-party image its registry host, so hosting
// copies it from the right registry: "redis:7" → "docker.io/library/redis:7".
func normalizeImage(image string) string {
	parts := strings.Split(image, "/")
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		return image
	}
	if len(parts) == 1 {
		return "docker.io/library/" + image
	}
	return "docker.io/" + image
}

// ---------------------------------------------------------------------------
// Dockerfile
// ---------------------------------------------------------------------------

type dockerfileFacts struct {
	expose  []int
	envPort int
	cmd     []string // the final stage's CMD (exec form, or sh -c)
	cmdRaw  string
	base    string // the final stage's FROM image
}

var dockerVarRef = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}?`)

// parseDockerfile reads the facts of a Dockerfile's final stage (or the
// stage named target).
func parseDockerfile(path, target string) (dockerfileFacts, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return dockerfileFacts{}, err
	}
	var lines []string
	var cur strings.Builder
	for _, raw := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(raw)
		if strings.HasPrefix(t, "#") && cur.Len() == 0 {
			continue
		}
		if strings.HasSuffix(t, "\\") {
			cur.WriteString(strings.TrimSuffix(t, "\\") + " ")
			continue
		}
		cur.WriteString(t)
		if s := strings.TrimSpace(cur.String()); s != "" {
			lines = append(lines, s)
		}
		cur.Reset()
	}
	var facts, chosen dockerfileFacts
	vars := map[string]string{}
	found := false
	for _, l := range lines {
		instr, rest, _ := strings.Cut(l, " ")
		rest = strings.TrimSpace(rest)
		switch strings.ToUpper(instr) {
		case "FROM":
			if found {
				// The target stage ended: nothing after it is the target's.
				return chosen, nil
			}
			fields := strings.Fields(rest)
			facts = dockerfileFacts{}
			if len(fields) > 0 {
				facts.base = fields[0]
			}
			if len(fields) >= 3 && strings.EqualFold(fields[1], "AS") && target != "" && fields[2] == target {
				found = true
			}
		case "ARG", "ENV":
			for k, v := range dockerKV(rest) {
				vars[k] = v
				if strings.EqualFold(instr, "ENV") && k == "PORT" {
					if p, err := strconv.Atoi(expandDockerVars(v, vars)); err == nil {
						facts.envPort = p
					}
				}
			}
		case "EXPOSE":
			for _, f := range strings.Fields(rest) {
				f = strings.SplitN(expandDockerVars(f, vars), "/", 2)[0]
				if p, err := strconv.Atoi(f); err == nil {
					facts.expose = append(facts.expose, p)
				}
			}
		case "CMD":
			facts.cmd, facts.cmdRaw = dockerCommand(rest)
		}
		if found {
			chosen = facts
		}
	}
	if found {
		return chosen, nil
	}
	return facts, nil
}

// dockerKV reads `K=V K2="V 2"` or the legacy `K V`.
func dockerKV(rest string) map[string]string {
	out := map[string]string{}
	if !strings.Contains(strings.Fields(rest + " x")[0], "=") {
		k, v, _ := strings.Cut(rest, " ")
		out[k] = strings.Trim(strings.TrimSpace(v), `"'`)
		return out
	}
	for _, f := range shellFields(rest) {
		if k, v, ok := strings.Cut(f, "="); ok {
			out[k] = v
		}
	}
	return out
}

func expandDockerVars(s string, vars map[string]string) string {
	return dockerVarRef.ReplaceAllStringFunc(s, func(m string) string {
		sub := dockerVarRef.FindStringSubmatch(m)
		if v, ok := vars[sub[1]]; ok && v != "" {
			return v
		}
		return sub[2]
	})
}

func dockerCommand(rest string) ([]string, string) {
	if strings.HasPrefix(rest, "[") {
		var argv []string
		if json.Unmarshal([]byte(rest), &argv) == nil {
			return argv, strings.Join(argv, " ")
		}
	}
	return []string{"sh", "-c", rest}, rest
}

// shellFields splits on spaces outside quotes, dropping the quotes.
func shellFields(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	in := false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote, in = r, true
		case quote == 0 && r == ' ':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

var cmdPortFlag = regexp.MustCompile(`(?:--port[= ]|-p |--bind[= ](?:[0-9.]*):|-b (?:[0-9.]*):|\b0\.0\.0\.0:|\blocalhost:)(\d{2,5})\b`)

// portInCommand is a port a start command binds explicitly
// (`uvicorn … --port 8000`, `gunicorn -b 0.0.0.0:8000`), 0 when none.
func portInCommand(cmd string) int {
	if m := cmdPortFlag.FindStringSubmatch(cmd); m != nil {
		if p, err := strconv.Atoi(m[1]); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// the app's own files (package.json, requirements, …)
// ---------------------------------------------------------------------------

// appSource is what an app directory (a build context) is written in.
type appSource struct {
	dir      string // relative to the root, "" = the root
	lang     string // node, python, ruby, go, java, ""
	deps     map[string]bool
	scripts  map[string]string
	pm       string // npm, pnpm, yarn, bun
	text     string // the python/ruby dependency files, lowercased
	procfile map[string]string
}

func readAppSource(root, dir string) appSource {
	abs := filepath.Join(root, dir)
	s := appSource{dir: dir, deps: map[string]bool{}, scripts: map[string]string{}, procfile: map[string]string{}}
	if data, err := os.ReadFile(filepath.Join(abs, "package.json")); err == nil {
		var pkg struct {
			Scripts         map[string]string `json:"scripts"`
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			s.lang = "node"
			s.scripts = pkg.Scripts
			for d := range pkg.Dependencies {
				s.deps[d] = true
			}
			for d := range pkg.DevDependencies {
				s.deps[d] = true
			}
		}
		s.pm = "npm"
		switch {
		case fileExists(filepath.Join(abs, "pnpm-lock.yaml")):
			s.pm = "pnpm"
		case fileExists(filepath.Join(abs, "yarn.lock")):
			s.pm = "yarn"
		case fileExists(filepath.Join(abs, "bun.lockb")), fileExists(filepath.Join(abs, "bun.lock")):
			s.pm = "bun"
		}
	}
	var text strings.Builder
	for _, f := range []string{"requirements.txt", "pyproject.toml", "Pipfile", "setup.py"} {
		if data, err := os.ReadFile(filepath.Join(abs, f)); err == nil {
			if s.lang == "" {
				s.lang = "python"
			}
			text.Write(data)
			text.WriteString("\n")
		}
	}
	if data, err := os.ReadFile(filepath.Join(abs, "Gemfile")); err == nil {
		if s.lang == "" {
			s.lang = "ruby"
		}
		text.Write(data)
	}
	if s.lang == "" && fileExists(filepath.Join(abs, "go.mod")) {
		s.lang = "go"
		if data, err := os.ReadFile(filepath.Join(abs, "go.mod")); err == nil {
			text.Write(data)
		}
	}
	if s.lang == "" && (fileExists(filepath.Join(abs, "pom.xml")) || fileExists(filepath.Join(abs, "build.gradle")) || fileExists(filepath.Join(abs, "build.gradle.kts"))) {
		s.lang = "java"
	}
	s.text = strings.ToLower(text.String())
	if data, err := os.ReadFile(filepath.Join(abs, "Procfile")); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if k, v, ok := strings.Cut(l, ":"); ok {
				s.procfile[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	return s
}

func (s appSource) has(dep string) bool { return s.deps[dep] }

// mentions reports a Python/Ruby/Go dependency by name.
func (s appSource) mentions(names ...string) bool {
	for _, n := range names {
		if regexp.MustCompile(`(?m)(^|[\s"'/\[,])` + regexp.QuoteMeta(n) + `($|[\s"'<>=~!\[;,@])`).MatchString(s.text) {
			return true
		}
	}
	return false
}

// usesPostgres reports whether the app's own dependencies name a Postgres
// driver, and which.
func (s appSource) usesPostgres(root string) (bool, string) {
	for _, d := range []string{"pg", "postgres", "pg-promise", "slonik", "@neondatabase/serverless"} {
		if s.has(d) {
			return true, "package.json depends on " + d
		}
	}
	if s.has("prisma") || s.has("@prisma/client") {
		if data, err := os.ReadFile(filepath.Join(root, s.dir, "prisma", "schema.prisma")); err == nil && strings.Contains(string(data), "postgres") {
			return true, "prisma/schema.prisma's datasource is postgresql"
		}
	}
	if s.mentions("psycopg", "psycopg2", "psycopg2-binary", "asyncpg", "psycopg-binary") {
		return true, "the Python dependencies name a Postgres driver"
	}
	if s.lang == "ruby" && s.mentions("pg") {
		return true, "the Gemfile names pg"
	}
	if s.mentions("github.com/lib/pq", "github.com/jackc/pgx/v5", "github.com/jackc/pgx/v4") {
		return true, "go.mod names a Postgres driver"
	}
	return false, ""
}

// migrationCommand is the command that migrates the schema, as run in the
// app's directory (and in its image), with where it came from.
func (s appSource) migrationCommand(root string) ([]string, string) {
	abs := filepath.Join(root, s.dir)
	run := func(script string) []string {
		if s.pm == "npm" {
			return []string{"npm", "run", script}
		}
		return []string{s.pm, "run", script}
	}
	for _, name := range []string{"migrate:deploy", "db:migrate:deploy", "migrate", "db:migrate", "migration:run", "migrate:latest"} {
		if _, ok := s.scripts[name]; ok {
			return run(name), fmt.Sprintf("package.json script %q", name)
		}
	}
	switch {
	case s.has("prisma") || s.has("@prisma/client"):
		return []string{"npx", "prisma", "migrate", "deploy"}, "prisma in package.json"
	case s.has("knex"):
		return []string{"npx", "knex", "migrate:latest"}, "knex in package.json"
	case s.has("sequelize-cli"):
		return []string{"npx", "sequelize-cli", "db:migrate"}, "sequelize-cli in package.json"
	case s.has("drizzle-kit"):
		return []string{"npx", "drizzle-kit", "migrate"}, "drizzle-kit in package.json"
	case fileExists(filepath.Join(abs, "alembic.ini")):
		return []string{"alembic", "upgrade", "head"}, "alembic.ini"
	case fileExists(filepath.Join(abs, "manage.py")):
		return []string{"python", "manage.py", "migrate"}, "Django's manage.py"
	case s.lang == "ruby" && fileExists(filepath.Join(abs, "bin", "rails")) && dirExists(filepath.Join(abs, "db", "migrate")):
		return []string{"bin/rails", "db:migrate"}, "Rails' db/migrate"
	}
	return nil, ""
}

// devCommand is what runs the app on the host in development, and where it
// came from; fallback is the image's start command.
func (s appSource) devCommand(fallback []string, fallbackRaw string, port int) ([]string, string) {
	run := func(script string) []string {
		switch {
		case s.pm == "npm" && script == "start":
			return []string{"npm", "start"}
		case s.pm == "npm":
			return []string{"npm", "run", script}
		}
		return []string{s.pm, "run", script}
	}
	for _, name := range []string{"dev", "start:dev", "start"} {
		if _, ok := s.scripts[name]; ok {
			return run(name), fmt.Sprintf("dev runs package.json script %q", name)
		}
	}
	if web, ok := s.procfile["web"]; ok {
		return []string{"sh", "-c", portToEnv(web, port)}, "dev runs the Procfile's web process"
	}
	if len(fallback) > 0 {
		raw := fallbackRaw
		if len(fallback) == 3 && fallback[0] == "sh" && fallback[1] == "-c" {
			raw = fallback[2]
		}
		return []string{"sh", "-c", portToEnv(raw, port)}, "dev runs the Dockerfile's CMD on the host"
	}
	return []string{"sh", "-c", "echo 'set the dev command in deploy/kcl/dev/main.k' >&2; exit 1"}, "no dev command found: write one"
}

// portToEnv makes a command that binds port literally bind $PORT instead, so
// dev can give it whichever port is free.
func portToEnv(cmd string, port int) string {
	if port <= 0 {
		return cmd
	}
	return regexp.MustCompile(`(^|[\s:=])`+strconv.Itoa(port)+`\b`).ReplaceAllString(cmd, "${1}$$PORT")
}

func (s appSource) installCommand() string {
	switch s.lang {
	case "node":
		if s.pm == "npm" {
			return "npm ci"
		}
		return s.pm + " install"
	case "python":
		switch {
		case strings.Contains(s.text, "[tool.poetry]"):
			return "poetry install"
		case strings.Contains(s.text, "[project]"):
			return "pip install -e ."
		}
		return "pip install -r requirements.txt"
	case "ruby":
		return "bundle install"
	}
	return ""
}

func (s appSource) defaultPort(cmd string) (int, string) {
	c := strings.ToLower(cmd)
	switch {
	case strings.Contains(c, "uvicorn"), strings.Contains(c, "gunicorn"), strings.Contains(c, "manage.py"), strings.Contains(c, "fastapi"):
		return 8000, "the usual port of " + firstOf(c, "uvicorn", "gunicorn", "manage.py", "fastapi")
	case strings.Contains(c, "flask"):
		return 5000, "Flask's default port"
	case s.lang == "node", s.lang == "ruby":
		return 3000, "the usual " + s.lang + " port"
	case s.lang == "python":
		return 8000, "the usual Python port"
	}
	return 8080, "a common default"
}

func firstOf(s string, words ...string) string {
	for _, w := range words {
		if strings.Contains(s, w) {
			return w
		}
	}
	return words[0]
}

// ---------------------------------------------------------------------------
// detectApp
// ---------------------------------------------------------------------------

var credentialName = regexp.MustCompile(`(?i)(SECRET|PASSWORD|PASSWD|TOKEN|API_?KEY|PRIVATE_?KEY|CREDENTIAL|_KEY$|^KEY$|DSN)`)

// credentialURL matches a URL carrying userinfo (redis://:pass@cache,
// amqp://user:pass@mq, https://token@host) or a credential in its query.
var credentialURL = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^/?#\s@]+@|[?&](password|passwd|pwd|token|secret|api[_-]?key|access[_-]?key|sig|signature)=`)

// credentialValue reports a value that carries a credential whatever its
// variable is named, so it becomes a secret and never a literal in
// workloads.k or --dry-run output.
func credentialValue(v string) bool {
	return credentialURL.MatchString(v)
}

var invalidWorkloadChars = regexp.MustCompile(`[^a-z0-9-]+`)

// workloadName makes a compose service or directory name a DNS label.
func workloadName(s string) string {
	n := strings.Trim(invalidWorkloadChars.ReplaceAllString(strings.ToLower(strings.ReplaceAll(s, "_", "-")), "-"), "-")
	if n == "" || n[0] < 'a' || n[0] > 'z' {
		n = "app-" + n
	}
	return strings.TrimRight(n, "-")
}

// kclReserved are identifiers workloads.k and the env files already bind.
var kclReserved = map[string]bool{"forge": true, "fw": true, "tiers": true, "plugin": true, "wl": true, "migrate": true,
	"schema": true, "import": true, "lambda": true, "rule": true, "mixin": true, "protocol": true, "check": true,
	"assert": true, "if": true, "elif": true, "else": true, "for": true, "in": true, "and": true, "or": true, "not": true,
	"is": true, "all": true, "any": true, "map": true, "filter": true, "as": true, "type": true, "None": true, "True": true,
	"False": true, "Undefined": true, "output": true, "str": true, "int": true, "float": true, "bool": true, "list": true, "dict": true}

func workloadIdent(name string) string {
	id := strings.ReplaceAll(name, "-", "_")
	if kclReserved[id] {
		id += "_app"
	}
	return id
}

// detectApp reads root and fills an AppSpec, or says why it cannot.
func detectApp(root, project string, claimable bool) (*appDetection, error) {
	d := &appDetection{spec: generator.AppSpec{ProjectName: project, Claimable: claimable}}
	composeName := findComposeFile(root)
	var compose *composeFile
	if composeName != "" {
		c, err := parseComposeFile(filepath.Join(root, composeName))
		if err != nil {
			return nil, err
		}
		if err := refuseCredentialCommands(composeName, c); err != nil {
			return nil, err
		}
		compose = c
		for _, s := range c.services {
			if s.build != nil {
				d.spec.ComposeFile = composeName
				break
			}
		}
		if d.spec.ComposeFile == "" {
			d.skipped = append(d.skipped, fmt.Sprintf("%s builds none of its services, so dev runs the app on the host instead of through it", composeName))
		}
	}
	if d.spec.ComposeFile != "" {
		if err := d.fromCompose(root, compose); err != nil {
			return nil, err
		}
	} else {
		if err := d.fromDockerfiles(root, compose); err != nil {
			return nil, err
		}
	}
	d.finishDatabase()
	d.findMigration(root, compose)
	d.checkDockerignore(root)
	if claimable {
		d.claimableNotes()
	}
	sort.Strings(d.secrets)
	return d, nil
}

// fromCompose makes one workload per compose service the app builds, a
// managed Postgres of its Postgres service, and dev runs the file.
func (d *appDetection) fromCompose(root string, c *composeFile) error {
	var pg *composeService
	for i := range c.services {
		s := &c.services[i]
		if s.build == nil && isPostgresImage(s.image) {
			if pg == nil {
				pg = s
			}
		}
	}
	if pg != nil {
		name := d.spec.ProjectName
		if v := pg.environment["POSTGRES_DB"]; v != "" && !strings.Contains(v, "$") {
			name = v
		}
		d.spec.Database = &generator.AppDatabase{Name: name, ComposeService: pg.name}
		d.spec.DevOnly = append(d.spec.DevOnly, generator.AppDevService{Name: workloadName(pg.name), ComposeService: pg.name, Why: "staging and prod use a managed Postgres instead"})
		d.detected = append(d.detected, fmt.Sprintf("Postgres: compose service %q (%s) → a managed Postgres on staging and prod; dev keeps the compose service", pg.name, pg.image))
	}
	for _, s := range c.services {
		switch {
		case pg != nil && s.name == pg.name:
		case s.build != nil && isMigrationService(s):
			// findMigration reads it.
		case s.build != nil:
			w, err := d.composeWorkload(root, s, pg)
			if err != nil {
				return err
			}
			d.spec.Workloads = append(d.spec.Workloads, w)
		case devToolImages[imageRepo(s.image)]:
			d.spec.DevOnly = append(d.spec.DevOnly, generator.AppDevService{Name: workloadName(s.name), ComposeService: s.name, Why: "a dev tool"})
			d.skipped = append(d.skipped, fmt.Sprintf("compose service %q (%s): a dev tool, so dev only", s.name, s.image))
		case cacheImages[imageRepo(s.image)]:
			port := 6379
			if strings.Contains(imageRepo(s.image), "memcached") {
				port = 11211
			}
			if len(s.ports) > 0 {
				port = s.ports[0]
			}
			name := workloadName(s.name)
			d.spec.Workloads = append(d.spec.Workloads, generator.AppWorkload{
				Name: name, Ident: workloadIdent(name), Kind: "service", Image: normalizeImage(s.image),
				Port: port, PortSource: "the image's port", ComposeService: s.name,
				Notes: []string{fmt.Sprintf("compose service %q: hosting runs the image itself, reached as %s:%d inside the env, with no volume (a cache keeps nothing)", s.name, name, port)},
			})
			d.detected = append(d.detected, fmt.Sprintf("workload %s: the third-party image %s (compose service %q), reached inside the env only", name, s.image, s.name))
			if name != s.name {
				d.assumed = append(d.assumed, fmt.Sprintf("the app reaches %q as %q on hosting: point its URL there if it names %q", s.name, name, s.name))
			}
		default:
			d.spec.DevOnly = append(d.spec.DevOnly, generator.AppDevService{Name: workloadName(s.name), ComposeService: s.name, Why: "declare it for staging and prod yourself"})
			d.skipped = append(d.skipped, fmt.Sprintf("compose service %q (%s): dev only. forge hosts Postgres and caches for you; for this one, deliver its URL in staging and prod (a secret) or declare it in workloads.k", s.name, emptyAs(s.image, "no image")))
		}
	}
	if len(d.spec.Workloads) == 0 {
		return fmt.Errorf("compose builds only migration services; nothing to deploy")
	}
	d.dedupeNames()
	return nil
}

// isMigrationService reports a compose service that only migrates: named
// so, or running a migrate command.
func isMigrationService(s composeService) bool {
	n := strings.ToLower(s.name)
	return strings.Contains(n, "migrat") || (s.commandRaw != "" && regexp.MustCompile(`(?i)\bmigrat|db:migrate|upgrade head`).MatchString(s.commandRaw) && len(s.ports) == 0)
}

func (d *appDetection) composeWorkload(root string, s composeService, pg *composeService) (generator.AppWorkload, error) {
	name := workloadName(s.name)
	w := generator.AppWorkload{Name: name, Ident: workloadIdent(name), Image: name, Build: s.build, ComposeService: s.name, Env: map[string]string{}}
	facts, err := parseDockerfile(filepath.Join(root, filepath.FromSlash(s.build.Dockerfile)), s.build.Target)
	if err != nil {
		return w, fmt.Errorf("compose service %q builds %s: %w", s.name, s.build.Dockerfile, err)
	}
	src := readAppSource(root, s.build.Context)
	cmdRaw := facts.cmdRaw
	if s.commandRaw != "" {
		cmdRaw = s.commandRaw
		// compose runs this instead of the Dockerfile's CMD; every other
		// runtime must too, or a worker sharing the web image serves web.
		w.Command = s.command
		w.CommandSource = fmt.Sprintf("compose service %q's command", s.name)
	}
	// A worker command compose runs in place of a shared image's CMD
	// inherits that image's EXPOSE and ENV PORT, which are the web
	// process's: unless compose publishes a port or the command binds one,
	// it listens on nothing.
	overriddenWorker := s.commandRaw != "" && len(s.ports) == 0 && portInCommand(cmdRaw) == 0 && looksLikeWorker(s.name, cmdRaw)
	switch {
	case overriddenWorker:
	case len(s.ports) > 0:
		w.Port, w.PortSource = s.ports[0], fmt.Sprintf("the container port compose service %q publishes", s.name)
	case len(facts.expose) > 0:
		w.Port, w.PortSource = facts.expose[0], "the Dockerfile's EXPOSE"
	case portInCommand(cmdRaw) > 0:
		w.Port, w.PortSource = portInCommand(cmdRaw), "the port its start command binds"
	case facts.envPort > 0:
		w.Port, w.PortSource = facts.envPort, "the Dockerfile's ENV PORT"
	}
	w.Kind = "service"
	if w.Port == 0 {
		if looksLikeWorker(s.name, cmdRaw) {
			w.Kind = "worker"
		} else {
			w.Port, w.PortSource = src.defaultPort(cmdRaw)
			w.PortSource = "check: " + w.PortSource
			d.assumed = append(d.assumed, fmt.Sprintf("%s listens on %d (%s): nothing declared a port", name, w.Port, strings.TrimPrefix(w.PortSource, "check: ")))
		}
	}
	w.Expose = w.Kind == "service"
	for _, k := range s.envOrder {
		v := s.environment[k]
		switch {
		case k == "DATABASE_URL":
			w.UsesDatabase = true
			w.DatabaseEnv = append(w.DatabaseEnv, k)
		case k == "PORT":
		case pg != nil && isDBHostVar(k) && v == pg.name:
			d.assumed = append(d.assumed, fmt.Sprintf("%s reads its database from %s=%s; hosting delivers a URL, so make the app read DATABASE_URL", name, k, v))
		case pg != nil && (strings.Contains(v, "@"+pg.name+":") || strings.Contains(v, "@"+pg.name+"/")):
			// A database URL under another name (SQLALCHEMY_DATABASE_URI):
			// hosting delivers the managed database's URL under it.
			w.UsesDatabase = true
			w.DatabaseEnv = append(w.DatabaseEnv, k)
			d.assumed = append(d.assumed, fmt.Sprintf("%s reads its database URL from %s; hosting delivers a postgres:// URL there (SQLAlchemy wants postgresql://: adjust in the app if it refuses it)", name, k))
		case strings.Contains(v, "${") || credentialName.MatchString(k) || credentialValue(v):
			w.Secrets = append(w.Secrets, k)
			d.addSecret(k)
		default:
			w.Env[k] = v
		}
	}
	if pg != nil {
		for _, dep := range s.dependsOn {
			if dep == pg.name {
				w.UsesDatabase = true
			}
		}
	}
	desc := fmt.Sprintf("workload %s (%s): compose service %q, builds %s", name, w.Kind, s.name, s.build.Dockerfile)
	if s.build.Context != "" {
		desc += " (context " + s.build.Context + ")"
	}
	if w.Port > 0 {
		desc += fmt.Sprintf(", port %d", w.Port)
	}
	if w.UsesDatabase {
		desc += ", reads DATABASE_URL"
	}
	d.detected = append(d.detected, desc)
	for _, k := range s.withheldArgs {
		d.assumed = append(d.assumed, fmt.Sprintf("%s: build arg %s carries a credential, so it is not copied into workloads.k; pass it as a build secret (RUN --mount=type=secret) and declare it by hand", name, k))
	}
	if pg != nil && w.UsesDatabase && s.environment["DATABASE_URL"] == "" {
		d.assumed = append(d.assumed, fmt.Sprintf("%s reads its database from DATABASE_URL (it depends on %q)", name, pg.name))
	}
	return w, nil
}

func isDBHostVar(k string) bool {
	switch strings.ToUpper(k) {
	case "DB_HOST", "DATABASE_HOST", "POSTGRES_HOST", "PGHOST", "PG_HOST", "DB_HOSTNAME":
		return true
	}
	return false
}

func looksLikeWorker(name, cmd string) bool {
	return regexp.MustCompile(`(?i)worker|celery|sidekiq|rq |bullmq|queue|cron|beat`).MatchString(name + " " + cmd)
}

func (d *appDetection) addSecret(name string) {
	for _, s := range d.secrets {
		if s == name {
			return
		}
	}
	d.secrets = append(d.secrets, name)
}

// fromDockerfiles makes one workload per Dockerfile (the root one, then one
// per top-level directory); dev runs each on the host.
func (d *appDetection) fromDockerfiles(root string, compose *composeFile) error {
	files := findDockerfiles(root)
	if len(files) == 0 {
		return fmt.Errorf("no Dockerfile at the root or in a top-level directory, and no compose service with a build")
	}
	var pgService *composeService
	if compose != nil {
		for i := range compose.services {
			if isPostgresImage(compose.services[i].image) {
				pgService = &compose.services[i]
				break
			}
		}
	}
	for _, f := range files {
		dir := filepath.ToSlash(filepath.Dir(f))
		if dir == "." {
			dir = ""
		}
		name := "api"
		if dir != "" {
			name = workloadName(dir)
		}
		facts, err := parseDockerfile(filepath.Join(root, filepath.FromSlash(f)), "")
		if err != nil {
			return err
		}
		src := readAppSource(root, dir)
		w := generator.AppWorkload{Name: name, Ident: workloadIdent(name), Image: name, Env: map[string]string{},
			Build: &generator.AppBuild{Dockerfile: f, Context: dir}, DevDir: dir}
		switch {
		case len(facts.expose) > 0:
			w.Port, w.PortSource = facts.expose[0], "the Dockerfile's EXPOSE"
		case portInCommand(facts.cmdRaw) > 0:
			w.Port, w.PortSource = portInCommand(facts.cmdRaw), "the port the Dockerfile's CMD binds"
		case facts.envPort > 0:
			w.Port, w.PortSource = facts.envPort, "the Dockerfile's ENV PORT"
		}
		w.Kind = "service"
		if w.Port == 0 {
			if dir != "" && looksLikeWorker(name, facts.cmdRaw) {
				w.Kind = "worker"
			} else {
				p, why := src.defaultPort(facts.cmdRaw)
				w.Port, w.PortSource = p, "check: "+why
				d.assumed = append(d.assumed, fmt.Sprintf("%s listens on %d (%s): its Dockerfile declares no port (EXPOSE)", name, p, why))
			}
		}
		w.Expose = w.Kind == "service"
		w.DevCommand, w.DevCommandSource = src.devCommand(facts.cmd, facts.cmdRaw, w.Port)
		// A dev command copied from the Dockerfile's CMD or the Procfile
		// lands in deploy/kcl/dev/main.k and the summary, as a compose
		// command would (refuseCredentialCommands).
		if credentialArg(w.DevCommand) >= 0 {
			return fmt.Errorf("workload %s: %s, but that command carries a credential, so forge will not copy it into deploy/kcl/dev/main.k or print it: "+
				"have the command read it from an environment variable (for example ${API_TOKEN}), then adopt again", name, strings.TrimPrefix(w.DevCommandSource, "dev "))
		}
		if strings.HasPrefix(w.DevCommandSource, "no dev command") {
			d.assumed = append(d.assumed, fmt.Sprintf("%s has no dev command forge could find: set it in deploy/kcl/dev/main.k", name))
		} else if strings.Contains(w.DevCommandSource, "CMD") {
			d.assumed = append(d.assumed, fmt.Sprintf("dev runs %s's Dockerfile CMD on the host (%s): change it in deploy/kcl/dev/main.k if the app has a dev server", name, strings.Join(w.DevCommand[2:], " ")))
		}
		if pg, why := src.usesPostgres(root); pg {
			w.UsesDatabase = true
			d.assumed = append(d.assumed, fmt.Sprintf("%s uses Postgres (%s) and reads its URL from DATABASE_URL", name, why))
		} else if pgService != nil {
			w.UsesDatabase = true
			d.assumed = append(d.assumed, fmt.Sprintf("%s uses the Postgres of %s (compose service %q) and reads DATABASE_URL", name, findComposeFile(root), pgService.name))
		}
		if cmd := src.installCommand(); cmd != "" {
			if src.dir != "" {
				cmd = fmt.Sprintf("(cd %s && %s)", src.dir, cmd)
			}
			d.installs = append(d.installs, cmd)
		}
		desc := fmt.Sprintf("workload %s (%s): builds %s", name, w.Kind, f)
		if w.Port > 0 {
			desc += fmt.Sprintf(", port %d", w.Port)
		}
		d.detected = append(d.detected, desc)
		d.spec.Workloads = append(d.spec.Workloads, w)
	}
	d.dedupeNames()
	return nil
}

// finishDatabase gives the app a database when a workload uses one, and
// names it so it is no workload's name.
func (d *appDetection) finishDatabase() {
	uses := false
	for _, w := range d.spec.Workloads {
		uses = uses || w.UsesDatabase
	}
	if !uses {
		if d.spec.Database != nil {
			// A Postgres nothing reads: dev keeps it, hosting needs none.
			d.assumed = append(d.assumed, fmt.Sprintf("no workload reads compose service %q's Postgres (no depends_on, no DATABASE_URL), so staging and prod declare no database", d.spec.Database.ComposeService))
			d.spec.Database = nil
		}
		return
	}
	if d.spec.Database == nil {
		d.spec.Database = &generator.AppDatabase{Name: d.spec.ProjectName}
		d.detected = append(d.detected, "Postgres: a managed Postgres on staging and prod; dev runs a forge host-native Postgres")
	}
	name := workloadName(d.spec.Database.Name)
	for _, w := range d.spec.Workloads {
		if w.Name == name || name == "migrate" {
			name += "-db"
		}
	}
	d.spec.Database.Name = name
}

// findMigration finds the migration command of the first workload that uses
// the database: a compose service that migrates, else the app's own tool.
func (d *appDetection) findMigration(root string, compose *composeFile) {
	if d.spec.Database == nil {
		return
	}
	var owner *generator.AppWorkload
	for i := range d.spec.Workloads {
		if d.spec.Workloads[i].UsesDatabase && d.spec.Workloads[i].Build != nil {
			owner = &d.spec.Workloads[i]
			break
		}
	}
	if owner == nil {
		return
	}
	if compose != nil && d.spec.ComposeFile != "" {
		for _, s := range compose.services {
			if s.build != nil && isMigrationService(s) && len(s.command) > 0 {
				if !sameBuild(s.build, owner.Build) {
					d.assumed = append(d.assumed, fmt.Sprintf("compose service %q migrates from its own build; the migrate job runs %s's image instead", s.name, owner.Name))
				}
				d.spec.Migrate = &generator.AppMigration{Workload: owner.Name, Command: s.command, Source: fmt.Sprintf("compose service %q's command", s.name)}
				d.detected = append(d.detected, fmt.Sprintf("migrations: %s (compose service %q) → job migrate, before every rollout", s.commandRaw, s.name))
				return
			}
		}
	}
	src := readAppSource(root, owner.Build.Context)
	cmd, why := src.migrationCommand(root)
	if cmd == nil {
		d.assumed = append(d.assumed, "no migration command found: if the app migrates its schema, add a `migrate` job (forge skill load adopt shows it)")
		return
	}
	d.spec.Migrate = &generator.AppMigration{Workload: owner.Name, Command: cmd, Source: why}
	d.detected = append(d.detected, fmt.Sprintf("migrations: %s (%s) → job migrate from %s's image, before every rollout", strings.Join(cmd, " "), why, owner.Name))
	d.assumed = append(d.assumed, fmt.Sprintf("%s's image carries the migration tool (%s): the job runs it there", owner.Name, cmd[0]))
}

func sameBuild(a, b *generator.AppBuild) bool {
	return a.Dockerfile == b.Dockerfile && a.Context == b.Context && a.Target == b.Target
}

// dedupeNames keeps workload names unique.
func (d *appDetection) dedupeNames() {
	seen := map[string]int{}
	for i := range d.spec.Workloads {
		w := &d.spec.Workloads[i]
		if n := seen[w.Name]; n > 0 {
			w.Name = fmt.Sprintf("%s-%d", w.Name, n+1)
			w.Ident = workloadIdent(w.Name)
			if w.Build != nil {
				w.Image = w.Name
			}
		}
		seen[w.Name]++
	}
}

// checkDockerignore lists what .dockerignore should exclude: forge's own
// files, so a KCL-only edit leaves the image digest alone and the dev
// secrets never enter an image.
func (d *appDetection) checkDockerignore(root string) {
	contexts := map[string]bool{}
	for _, w := range d.spec.Workloads {
		if w.Build != nil {
			contexts[w.Build.Context] = true
		}
	}
	if !contexts[""] {
		return
	}
	data, _ := os.ReadFile(filepath.Join(root, ".dockerignore"))
	have := map[string]bool{}
	for _, l := range strings.Split(string(data), "\n") {
		have[strings.Trim(strings.TrimSpace(l), "/")] = true
	}
	for _, want := range []string{"deploy", ".forge", "secrets"} {
		if !have[want] {
			d.dockerignoreMissing = append(d.dockerignoreMissing, want)
		}
	}
}

// claimableNotes says what an anonymous deploy will refuse or change.
func (d *appDetection) claimableNotes() {
	services := 0
	for _, w := range d.spec.Workloads {
		if w.Kind == "service" {
			services++
		} else {
			d.assumed = append(d.assumed, fmt.Sprintf("--claimable: an unclaimed deploy runs one service; %s (%s) will be refused until it is claimed", w.Name, w.Kind))
		}
	}
	if services > 1 {
		d.assumed = append(d.assumed, fmt.Sprintf("--claimable: an unclaimed deploy runs one service, and prod declares %d: `forge env deploy prod --claimable` refuses it until you rebind all but one", services))
	}
	if len(d.secrets) > 0 {
		d.assumed = append(d.assumed, fmt.Sprintf("--claimable: an unclaimed deploy takes no secret values, so prod delivers none of %s until it is claimed", strings.Join(d.secrets, ", ")))
	}
	d.assumed = append(d.assumed, "--claimable: an unclaimed backend has no outbound internet (DNS and its own Postgres only): a call to a third-party API fails until it is claimed")
}
