package cli

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"runtime"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// trackedEvent is one Event trackEvent recorded.
type trackedEvent struct {
	name  string
	props map[string]string
}

// has reports whether e is want's event with at least want's props.
func (e trackedEvent) has(want trackedEvent) bool {
	if e.name != want.name {
		return false
	}
	for k, v := range want.props {
		if e.props[k] != v {
			return false
		}
	}
	return true
}

func (e trackedEvent) String() string { return fmt.Sprintf("%s%v", e.name, e.props) }

// recordTrackedEvents replaces trackEvent for the test, returning what it
// records. Not for parallel tests: trackEvent is process-wide.
func recordTrackedEvents(t *testing.T) *[]trackedEvent {
	t.Helper()
	var (
		mu  sync.Mutex
		got []trackedEvent
	)
	prev := trackEvent
	trackEvent = func(_ context.Context, name string, attrs ...slog.Attr) {
		e := trackedEvent{name: name, props: map[string]string{}}
		for _, a := range attrs {
			e.props[a.Key] = a.Value.String()
		}
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	}
	t.Cleanup(func() { trackEvent = prev })
	return &got
}

func TestClaimableShapeOf(t *testing.T) {
	t.Parallel()
	hosted := RuntimeEntity{Type: RuntimeHosted}
	workload := func(build string) WorkloadEntity {
		return WorkloadEntity{Name: "api", Runtime: hosted, Build: BuildConfigEntity{Type: build}}
	}
	site := func(typ string) FrontendEntity {
		return FrontendEntity{Name: "web", Type: typ, Runtime: FrontendRuntime{Type: FrontendRuntimeHosted}}
	}
	identity := &release.ShapeIdentity{Name: "users", Runtime: release.IdentityRuntimeHosted}
	cases := []struct {
		name string
		e    *KCLEntities
		want cloud.DeployShape
	}{
		{"nothing rendered", nil, cloud.DeployShape{Template: "other", Auth: "none"}},
		{"nothing hosted", &KCLEntities{}, cloud.DeployShape{Template: "other", Auth: "none"}},
		{"vite site", &KCLEntities{Frontends: []FrontendEntity{site("vite")}}, cloud.DeployShape{Template: "vite", Auth: "none"}},
		{"vite-spa site", &KCLEntities{Frontends: []FrontendEntity{site("vite-spa")}}, cloud.DeployShape{Template: "vite", Auth: "none"}},
		{"next.js export", &KCLEntities{Frontends: []FrontendEntity{site("nextjs")}}, cloud.DeployShape{Template: "nextjs-static", Auth: "none"}},
		{"expo app", &KCLEntities{Frontends: []FrontendEntity{site("react-native")}}, cloud.DeployShape{Template: "other", Auth: "none"}},
		{"a next.js and a vite site", &KCLEntities{Frontends: []FrontendEntity{site("nextjs"), site("vite")}}, cloud.DeployShape{Template: "other", Auth: "none"}},
		{"a site not hosted", &KCLEntities{Frontends: []FrontendEntity{{Name: "web", Type: "vite", Runtime: FrontendRuntime{Type: "bucket"}}}},
			cloud.DeployShape{Template: "other", Auth: "none"}},
		{"forge server with managed identity and a frontend", &KCLEntities{
			Workloads: []WorkloadEntity{workload("go")}, Frontends: []FrontendEntity{site("nextjs")}, Identity: identity,
		}, cloud.DeployShape{Template: "forge-native", Auth: "managed"}},
		{"forge server and a docker job", &KCLEntities{Workloads: []WorkloadEntity{workload("docker"), workload("go")}},
			cloud.DeployShape{Template: "forge-native", Auth: "none"}},
		{"adopted dockerfile app with a vite site", &KCLEntities{
			Workloads: []WorkloadEntity{workload("docker")}, Frontends: []FrontendEntity{site("vite")},
		}, cloud.DeployShape{Template: "dockerfile-app", Auth: "none"}},
		{"prebuilt image", &KCLEntities{Workloads: []WorkloadEntity{workload("")}}, cloud.DeployShape{Template: "dockerfile-app", Auth: "none"}},
		{"shell build", &KCLEntities{Workloads: []WorkloadEntity{workload("shell")}, Frontends: []FrontendEntity{site("vite")}},
			cloud.DeployShape{Template: "other", Auth: "none"}},
		{"a go workload not hosted", &KCLEntities{
			Workloads: []WorkloadEntity{{Name: "api", Runtime: RuntimeEntity{Type: RuntimeCluster}, Build: BuildConfigEntity{Type: "go"}}},
			Frontends: []FrontendEntity{site("vite")},
		}, cloud.DeployShape{Template: "vite", Auth: "none"}},
		{"several identities", &KCLEntities{Frontends: []FrontendEntity{site("vite")}, Identities: []release.ShapeIdentity{*identity, *identity}},
			cloud.DeployShape{Template: "vite", Auth: "managed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claimableShapeOf(tc.e); got != tc.want {
				t.Fatalf("claimableShapeOf = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestClaimableRefusedReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		err   error
		stage string
		want  string
	}{
		{"signed in", &claimableRefusal{reason: reasonClaimableSignedIn}, "", "claimable_signed_in"},
		{"ineligible", &claimableRefusal{reason: reasonClaimableIneligible}, "", "claimable_ineligible"},
		{"unavailable", &claimableRefusal{reason: cloud.ReasonClaimableUnavailable}, "", "unavailable"},
		{"wrapped refusal", fmt.Errorf("deploy: %w", &claimableRefusal{reason: reasonClaimableSignedIn}), "", "claimable_signed_in"},
		{"flags", &claimableRefusal{}, refusedInvalidFlags, "invalid_flags"},
		{"migrations unreadable", fmt.Errorf("read db/migrations: denied"), refusedUnmergedMigrations, "unmerged_migrations"},
		{"build", fmt.Errorf("docker build failed"), refusedBuild, "build"},
		{"anything else", fmt.Errorf("env publishes nothing hosted"), "", "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claimableRefusedReason(tc.err, tc.stage); got != tc.want {
				t.Fatalf("claimableRefusedReason = %q, want %q", got, tc.want)
			}
		})
	}
}

// The props are fixed vocabularies only: a marker's value never reaches them,
// and an unrendered env's shape is "unknown".
func TestClaimableRefusedAttrs(t *testing.T) {
	t.Parallel()
	env := map[string]string{"CODEX_SESSION_ID": "/home/alice/.codex/sess-42"}
	got := map[string]string{}
	for _, a := range claimableRefusedAttrs("other", cloud.DeployShape{}, func(k string) string { return env[k] }) {
		got[a.Key] = a.Value.String()
	}
	want := map[string]string{
		"reason": "other", "version": buildinfo.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"harness": "codex", "template": "unknown", "auth": "unknown",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("attrs = %v, want %v", got, want)
	}
	for k, v := range got {
		if !regexp.MustCompile(`^[a-z0-9_.+-]+$`).MatchString(v) {
			t.Errorf("prop %s = %q: not a fixed-vocabulary value", k, v)
		}
	}
}

// A flag combination --claimable refuses is counted once, before anything is
// rendered or asked of a control plane.
func TestDispatchClaimableDeploy_TracksALocalRefusal(t *testing.T) {
	tracked := recordTrackedEvents(t)
	err := dispatchClaimableDeploy(context.Background(), "prod", deployCmdFlags{dryRun: true})
	if err == nil {
		t.Fatal("--claimable --dry-run must be refused")
	}
	want := trackedEvent{name: claimableRefusedEvent, props: map[string]string{"reason": "invalid_flags", "template": "unknown", "auth": "unknown"}}
	if len(*tracked) != 1 || !(*tracked)[0].has(want) {
		t.Fatalf("tracked %v, want one %v", *tracked, want)
	}
}

// --plan-only is a preview, not a refused deploy: it tracks nothing.
func TestDispatchClaimableDeploy_PreviewTracksNothing(t *testing.T) {
	tracked := recordTrackedEvents(t)
	f := deployCmdFlags{dryRun: true}
	f.promote.planOnly = true
	if err := dispatchClaimableDeploy(context.Background(), "prod", f); err == nil {
		t.Fatal("--dry-run with --plan-only must still be refused")
	}
	if len(*tracked) != 0 {
		t.Fatalf("a preview tracked %v", *tracked)
	}
}
