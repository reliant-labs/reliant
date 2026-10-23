package pgtest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"go.yaml.in/yaml/v3"
)

// Which postgres the embedded server runs.
//
// A test suite that passes on one postgres major does not prove the SQL on
// another: generated columns, uuidv7(), MERGE … RETURNING, planner and error
// text all move between majors. So the embedded server's major is the
// PROJECT's declaration — the one its production database runs — not
// whatever embedded-postgres happens to default to. Resolution, first match
// wins:
//
//  1. FORGE_TEST_POSTGRES_VERSION (EnvVersion), for one run;
//  2. `database.postgres_version` in the nearest forge.yaml at or above the
//     working directory (a test binary runs in its package's directory, so
//     that is the project's own forge.yaml);
//  3. DefaultVersion.
//
// Either declaration is a major ("16") or an exact release ("16.9.0"). A
// major resolves to the release this forge pins for it (supportedMajors).
//
// Each version gets its OWN shared server and runtime root (instanceRoot): a
// machine running one project on 16 and another on 18 runs two servers, one
// per major, each shared by every process that wants it.
//
// With FORGE_TEST_POSTGRES_URL there is no embedded server to choose. When a
// version is DECLARED (1 or 2 above), the external server's major is checked
// against it, so a CI service container on another major fails the run by
// name instead of passing it on the wrong postgres.

// EnvVersion names the env var that selects the embedded server's postgres
// version for one run, over forge.yaml's database.postgres_version.
const EnvVersion = "FORGE_TEST_POSTGRES_VERSION"

// DefaultVersion is the postgres the embedded server runs when nothing
// declares one: embedded-postgres's own default.
const DefaultVersion = string(embeddedpostgres.V18)

// supportedMajors maps each major forge can boot to the exact release it
// boots. The releases are embedded-postgres's: the binaries it knows how to
// fetch.
var supportedMajors = map[string]embeddedpostgres.PostgresVersion{
	"18": embeddedpostgres.V18,
	"17": embeddedpostgres.V17,
	"16": embeddedpostgres.V16,
	"15": embeddedpostgres.V15,
	"14": embeddedpostgres.V14,
}

var versionRE = regexp.MustCompile(`^(\d+)(\.\d+){0,2}$`)

// ResolveVersion turns a declaration — a major ("16") or an exact release
// ("16.9.0") — into the release the embedded server boots.
func ResolveVersion(declared string) (string, error) {
	v := strings.TrimPrefix(strings.TrimSpace(declared), "v")
	m := versionRE.FindStringSubmatch(v)
	if m == nil {
		return "", fmt.Errorf("pgtest: postgres version %q is not a major (16) or a release (16.9.0)", declared)
	}
	major := m[1]
	pinned, ok := supportedMajors[major]
	if !ok {
		return "", fmt.Errorf("pgtest: postgres %s is not one forge can boot embedded (supported majors: %s)", major, strings.Join(SupportedMajors(), ", "))
	}
	if v == major {
		return string(pinned), nil
	}
	// A release: complete a two-part one ("16.9" -> "16.9.0"), the shape
	// embedded-postgres's binary repository names.
	if strings.Count(v, ".") == 1 {
		v += ".0"
	}
	return v, nil
}

// SupportedMajors lists the postgres majors the embedded server can run,
// newest first.
func SupportedMajors() []string {
	out := make([]string, 0, len(supportedMajors))
	for m := range supportedMajors {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i])
		b, _ := strconv.Atoi(out[j])
		return a > b
	})
	return out
}

// versionChoice is this process's resolved version: the release, whether it
// was declared (rather than defaulted), and where the declaration came from.
type versionChoice struct {
	version  string
	declared bool
	source   string
	err      error
}

var (
	versionOnce sync.Once
	chosen      versionChoice
)

// serverVersion is the version this process's embedded server runs, resolved
// once: every boot, root and reap in one process must agree on it.
func serverVersion() versionChoice {
	versionOnce.Do(func() { chosen = chooseVersion(os.Getenv(EnvVersion), workingDir()) })
	return chosen
}

func workingDir() string {
	wd, _ := os.Getwd()
	return wd
}

// chooseVersion applies the resolution order to an env value and a working
// directory.
func chooseVersion(env, wd string) versionChoice {
	if strings.TrimSpace(env) != "" {
		v, err := ResolveVersion(env)
		return versionChoice{version: v, declared: true, source: EnvVersion, err: err}
	}
	if decl, file := declaredInForgeYAML(wd); decl != "" {
		v, err := ResolveVersion(decl)
		if err != nil {
			err = fmt.Errorf("%s: database.postgres_version: %w", file, err)
		}
		return versionChoice{version: v, declared: true, source: file, err: err}
	}
	return versionChoice{version: DefaultVersion}
}

// declaredInForgeYAML returns database.postgres_version from the nearest
// forge.yaml at or above dir, and that file's path. Only the one key is
// parsed: the rest of forge.yaml is the forge CLI's to validate, and a test
// binary must not fail over a key it does not read.
func declaredInForgeYAML(dir string) (version, file string) {
	for d := dir; d != ""; {
		path := filepath.Join(d, "forge.yaml")
		if data, err := os.ReadFile(path); err == nil { // #nosec G304 -- the project's own forge.yaml
			var f struct {
				Database struct {
					PostgresVersion string `yaml:"postgres_version"`
				} `yaml:"database"`
			}
			if yaml.Unmarshal(data, &f) != nil {
				return "", ""
			}
			return strings.TrimSpace(f.Database.PostgresVersion), path
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return "", ""
}

// instanceRoot is the directory this process's embedded servers, pool state
// and lock live under. The default version keeps the historical
// os.TempDir()/forge-pgtest, so a process on an older forge still shares (and
// never reaps) the same server. Every other version gets a root of its own,
// which an older forge's reaper never scans.
func instanceRoot() string {
	return instanceRootFor(serverVersion().version)
}

func instanceRootFor(version string) string {
	if version == "" || version == DefaultVersion {
		return filepath.Join(os.TempDir(), "forge-pgtest")
	}
	return filepath.Join(os.TempDir(), "forge-pgtest-"+version)
}

// checkExternalVersion fails when a version is declared and the external
// server db is connected to runs another major.
func checkExternalVersion(ctx context.Context, db *sql.DB, choice versionChoice) error {
	if !choice.declared || choice.err != nil {
		return choice.err
	}
	var num int
	if err := db.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::int").Scan(&num); err != nil {
		return fmt.Errorf("pgtest: read the server version of %s: %w", EnvBaseURL, err)
	}
	have := fmt.Sprint(num / 10000)
	want := strings.SplitN(choice.version, ".", 2)[0]
	if have != want {
		return fmt.Errorf("pgtest: %s is postgres %s, but %s declares postgres %s — point it at a postgres %s server, or change the declaration", EnvBaseURL, have, choice.source, want, want)
	}
	return nil
}
