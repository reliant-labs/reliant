package storage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

func testDigest(letter string) string { return "sha256:" + strings.Repeat(letter, 64) }

// singleArch is a manifest body with knowable sizes and no children.
func singleArch(config, layer uint64) string {
	return fmt.Sprintf(`{"schemaVersion":2,"config":{"size":%d},"layers":[{"size":%d}]}`, config, layer)
}

func index(children ...string) string {
	var parts []string
	for _, c := range children {
		parts = append(parts, `{"digest":"`+c+`"}`)
	}
	return `{"schemaVersion":2,"manifests":[` + strings.Join(parts, ",") + `]}`
}

func planDigests(plan []PlanEntry) []string {
	var out []string
	for _, e := range plan {
		out = append(out, e.Digest)
	}
	sort.Strings(out)
	return out
}

func TestRegistryGraphRetainsChildrenOfUntaggedIndexes(t *testing.T) {
	now := time.Now()
	parent, child, recent, second, expired := testDigest("a"), testDigest("b"), testDigest("c"), testDigest("d"), testDigest("e")
	p := DefaultPolicy()
	p.RegistryKeep = 2
	reg := Registry{Repositories: []string{"app"}}
	tags := []Tag{
		{"app", "recent", recent, now},
		{"app", "second", second, now.Add(-time.Hour)},
		{"app", "old-child", child, now.Add(-30 * 24 * time.Hour)},
		{"app", "expired", expired, now.Add(-40 * 24 * time.Hour)},
	}
	revisions := []Revision{
		{Version{"app", parent}, now},
		{Version{"app", child}, now},
		{Version{"app", recent}, now},
		{Version{"app", second}, now},
		{Version{"app", expired}, now.Add(-40 * 24 * time.Hour)},
	}
	for _, pinned := range []bool{false, true} {
		t.Run(fmt.Sprintf("wildcard_pin=%t", pinned), func(t *testing.T) {
			protected := map[string]map[string]bool{}
			if pinned {
				protected["*"] = map[string]bool{parent: true}
			}
			visited := map[string]bool{}
			got, err := registryGraphPlan(tags, revisions, p, reg, protected, now, func(v Version) ([]byte, error) {
				visited[v.Digest] = true
				if v.Digest == parent {
					return []byte(index(child)), nil
				}
				return []byte(singleArch(1, 2)), nil
			})
			if err != nil || len(got) != 1 || got[0].Version != (Version{"app", expired}) {
				t.Fatalf("untagged index child must remain pullable: plan=%v err=%v", planDigests(got), err)
			}
			if !visited[parent] || !visited[child] {
				t.Fatalf("retained graph was not traversed: %v", visited)
			}
		})
	}
}

func TestRegistryGraphDoesNotProtectEligibleIndexes(t *testing.T) {
	now := time.Now()
	parent, child, recent, second := testDigest("a"), testDigest("b"), testDigest("c"), testDigest("d")
	p := DefaultPolicy()
	p.RegistryKeep = 2
	tags := []Tag{
		{"app", "recent", recent, now}, {"app", "second", second, now},
		{"app", "old-index", parent, now.Add(-30 * 24 * time.Hour)},
		{"app", "old-child", child, now.Add(-30 * 24 * time.Hour)},
	}
	old := now.Add(-30 * 24 * time.Hour)
	revisions := []Revision{
		{Version{"app", parent}, old}, {Version{"app", child}, old},
		{Version{"app", recent}, now}, {Version{"app", second}, now},
	}
	got, err := registryGraphPlan(tags, revisions, p, Registry{Repositories: []string{"app"}}, nil, now, func(v Version) ([]byte, error) {
		if v.Digest == recent || v.Digest == second {
			return []byte(singleArch(1, 2)), nil
		}
		return []byte(index()), nil
	})
	if err != nil || len(got) != 2 {
		t.Fatalf("eligible graph must remain reclaimable: %v, %v", planDigests(got), err)
	}
	for _, e := range got {
		if e.Untagged || len(e.Tags) == 0 {
			t.Fatalf("expired tagged version reported as untagged: %+v", e)
		}
	}
}

func TestRegistryGraphFailsClosedOnUnreadableUntaggedRoot(t *testing.T) {
	root := Revision{Version{"app", testDigest("a")}, time.Now()}
	_, err := registryGraphPlan(nil, []Revision{root}, DefaultPolicy(), Registry{Repositories: []string{"app"}}, nil, time.Now(), func(Version) ([]byte, error) {
		return nil, fmt.Errorf("missing retained manifest")
	})
	if err == nil {
		t.Fatal("unreadable untagged root allowed deletion planning")
	}
}

// Untagged manifests are the ~25 GB of orphans a re-pushed tag leaves behind.
// They are reclaimable exactly when nothing retained reaches them and they are
// older than the retention window; before this they were all treated as roots
// and never selected.
func TestRegistryGraphReclaimsUnreachableUntaggedManifests(t *testing.T) {
	now := time.Now()
	p := DefaultPolicy()
	p.RegistryKeep = 2
	p.RegistryDays = 14
	old, recent := now.Add(-30*24*time.Hour), now.Add(-2*time.Hour)

	orphan, fresh := testDigest("1"), testDigest("2")
	liveTag, liveIdx, liveChild := testDigest("3"), testDigest("4"), testDigest("5")
	deadIdx, sharedChild, ownChild := testDigest("6"), testDigest("7"), testDigest("8")
	pinned := testDigest("9")

	for _, tc := range []struct {
		name      string
		tags      []Tag
		revisions []Revision
		protected map[string]map[string]bool
		bodies    map[string]string
		want      []string
		untagged  []string
		wantErr   bool
	}{
		{
			name:      "old untagged single-arch is reclaimed",
			tags:      []Tag{{"app", "dev", liveTag, now}},
			revisions: []Revision{{Version{"app", liveTag}, now}, {Version{"app", orphan}, old}},
			bodies:    map[string]string{liveTag: singleArch(100, 900), orphan: singleArch(1000, 4000)},
			want:      []string{orphan},
			untagged:  []string{orphan},
		},
		{
			name:      "recent untagged is kept",
			tags:      []Tag{{"app", "dev", liveTag, now}},
			revisions: []Revision{{Version{"app", liveTag}, now}, {Version{"app", fresh}, recent}},
			bodies:    map[string]string{liveTag: singleArch(1, 2), fresh: singleArch(1, 2)},
			want:      nil,
		},
		{
			name:      "old untagged child of a retained index is kept",
			tags:      []Tag{{"app", "dev", liveIdx, now}},
			revisions: []Revision{{Version{"app", liveIdx}, now}, {Version{"app", liveChild}, old}},
			bodies:    map[string]string{liveIdx: index(liveChild), liveChild: singleArch(1, 2)},
			want:      nil,
		},
		{
			name: "old untagged index is reclaimed while children shared with a retained tag are kept",
			tags: []Tag{{"app", "dev", sharedChild, now}},
			revisions: []Revision{
				{Version{"app", sharedChild}, old}, {Version{"app", deadIdx}, old}, {Version{"app", ownChild}, old},
			},
			bodies: map[string]string{
				deadIdx: index(sharedChild, ownChild), sharedChild: singleArch(1, 2), ownChild: singleArch(10, 20),
			},
			want:     []string{deadIdx, ownChild},
			untagged: []string{deadIdx, ownChild},
		},
		{
			name:      "old untagged manifest protected by a bare digest is kept",
			tags:      []Tag{{"app", "dev", liveTag, now}},
			revisions: []Revision{{Version{"app", liveTag}, now}, {Version{"app", pinned}, old}},
			protected: map[string]map[string]bool{"*": {pinned: true}},
			bodies:    map[string]string{liveTag: singleArch(1, 2), pinned: singleArch(1, 2)},
			want:      nil,
		},
		{
			name:      "fetch error aborts the whole plan",
			tags:      []Tag{{"app", "dev", liveTag, now}},
			revisions: []Revision{{Version{"app", liveTag}, now}, {Version{"app", orphan}, old}},
			bodies:    map[string]string{liveTag: singleArch(1, 2)},
			wantErr:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := registryGraphPlan(tc.tags, tc.revisions, p, Registry{Repositories: []string{"app"}}, tc.protected, now, func(v Version) ([]byte, error) {
				body, ok := tc.bodies[v.Digest]
				if !ok {
					return nil, fmt.Errorf("manifest %s unreadable", v.Digest)
				}
				return []byte(body), nil
			})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("unreadable manifest did not abort the plan: %v", planDigests(got))
				}
				if got != nil {
					t.Fatalf("aborted plan still returned entries: %v", planDigests(got))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := append([]string{}, tc.want...)
			sort.Strings(want)
			if gotDigests := planDigests(got); !equalStrings(gotDigests, want) {
				t.Fatalf("plan = %v, want %v", gotDigests, want)
			}
			for _, e := range got {
				if contains(tc.untagged, e.Digest) != e.Untagged {
					t.Fatalf("entry %+v mislabelled: untagged set is %v", e, tc.untagged)
				}
			}
		})
	}
}

// A protected digest roots the graph only where the registry actually holds
// that manifest. A bare digest names content, not a location, and much of what
// the protected-set scan collects is not a manifest in THIS registry at all: a
// container started by image ID (`docker run sha256:<config>` — which is
// exactly how RegistryGC starts its own retention helper), a kubelet imageID
// from another registry, or a release pin for a sibling repository. Rooting
// such a digest in every repository and fetching it 404s by construction, and
// one 404 aborted the entire plan. TestRegistryMaintenanceEndToEnd failed in CI
// on precisely that: GET /v2/app/manifests/<registry:2's image ID>.
func TestRegistryGraphRootsProtectedDigestsOnlyWhereTheManifestExists(t *testing.T) {
	now := time.Now()
	old := now.Add(-30 * 24 * time.Hour)
	p := DefaultPolicy()
	p.RegistryKeep = 1
	p.RegistryDays = 14

	live, appOrphan := testDigest("a"), testDigest("b")
	sibPinned, sibOrphan := testDigest("c"), testDigest("d")
	imageID := testDigest("e") // a config digest: never a manifest anywhere
	appPinned := testDigest("f")

	reg := Registry{Repositories: []string{"app", "sib"}}
	tags := []Tag{{"app", "dev", live, now}}
	revisions := []Revision{
		{Version{"app", live}, now},
		{Version{"app", appOrphan}, old},
		{Version{"sib", sibPinned}, old},
		{Version{"sib", sibOrphan}, old},
	}
	bodies := map[Version]string{
		{"app", live}:      singleArch(1, 2),
		{"app", appOrphan}: singleArch(1, 2),
		{"sib", sibPinned}: singleArch(1, 2),
		{"sib", sibOrphan}: singleArch(1, 2),
	}
	protected := map[string]map[string]bool{
		// A bare release pin held only by sib, and an image ID held by nobody.
		"*": {sibPinned: true, imageID: true},
		// A repository-qualified digest for content the registry no longer has
		// (a pod still referencing an image that was already reclaimed).
		"app": {appPinned: true},
	}
	var fetched []Version
	got, err := registryGraphPlan(tags, revisions, p, reg, protected, now, func(v Version) ([]byte, error) {
		fetched = append(fetched, v)
		body, ok := bodies[v]
		if !ok {
			return nil, fmt.Errorf("registry GET /v2/%s/manifests/%s: HTTP 404", v.Repository, v.Digest)
		}
		return []byte(body), nil
	})
	if err != nil {
		t.Fatalf("a protected digest that is no manifest here aborted the plan: %v", err)
	}
	if want := []string{appOrphan, sibOrphan}; !equalStrings(planDigests(got), want) {
		t.Fatalf("plan = %v, want only the two orphans %v (the sib pin must stay protected)", planDigests(got), want)
	}
	for _, v := range fetched {
		if _, ok := bodies[v]; !ok {
			t.Errorf("fetched %s/%s, which the inventory says this registry does not hold", v.Repository, v.Digest)
		}
	}
}

// The same defect end to end through Runner.registryPlan: the protected set is
// assembled from `docker inspect` of every container, and a container whose
// Config.Image is a bare image ID — RegistryGC's own `-forge-retention` helper,
// started from the registry container's image ID — must not abort the plan.
// Only the docker CLI and the registry HTTP API are faked; the inventory
// parsing, protected-set assembly and graph walk are the production code.
func TestRegistryPlanSurvivesAContainerStartedByImageID(t *testing.T) {
	now := time.Now()
	live, orphan := testDigest("a"), testDigest("b")
	imageID := testDigest("e")
	const prefix = "/var/lib/registry/docker/registry/v2/repositories/app/_manifests/"
	manifests := map[string]string{live: singleArch(1, 2), orphan: singleArch(3, 4)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		digest := strings.TrimPrefix(req.URL.Path, "/v2/app/manifests/")
		body, ok := manifests[digest]
		if req.Method != http.MethodGet || !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	r := Runner{Policy: DefaultPolicy(), Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "docker" {
			return nil, fmt.Errorf("unexpected command %s %v", name, args)
		}
		joined := strings.Join(args, " ")
		switch {
		case joined == "ps -aq":
			return []byte("helper\n"), nil
		case joined == "inspect helper":
			return []byte(`[{"Config":{"Image":"` + imageID + `"},"Image":"` + imageID + `"}]`), nil
		case joined == "image inspect "+imageID:
			return []byte(`[{"RepoDigests":[]}]`), nil
		case args[0] == "exec" && strings.Contains(joined, "_manifests/tags/"):
			return []byte(fmt.Sprintf("%d\t%stags/dev/current/link\t%s\n", now.Unix(), prefix, live)), nil
		case args[0] == "exec" && strings.Contains(joined, "_manifests/revisions/"):
			old := now.Add(-30 * 24 * time.Hour).Unix()
			return []byte(fmt.Sprintf("%d\t%srevisions/sha256/%s/link\t%s\n%d\t%srevisions/sha256/%s/link\t%s\n",
				now.Unix(), prefix, strings.TrimPrefix(live, "sha256:"), live,
				old, prefix, strings.TrimPrefix(orphan, "sha256:"), orphan)), nil
		}
		return nil, fmt.Errorf("unexpected docker %v", args)
	}}
	plan, err := r.registryPlan(context.Background(), Registry{Container: "reg", Repositories: []string{"app"}}, "reg", srv.URL)
	if err != nil {
		t.Fatalf("registry plan aborted on a container started by image ID: %v", err)
	}
	if got := planDigests(plan); !equalStrings(got, []string{orphan}) {
		t.Fatalf("plan = %v, want [%s]", got, orphan)
	}
}

// The preview must say which bytes it is about to reclaim, and must not claim
// the total is exact: layers are shared between manifests.
func TestRegistryPlanPreviewDistinguishesTaggedAndUntaggedWithSizes(t *testing.T) {
	now := time.Now()
	live, previous, expiredTag, orphan := testDigest("a"), testDigest("d"), testDigest("b"), testDigest("c")
	bodies := map[string]string{
		live:       singleArch(1<<20, 2<<20),
		previous:   singleArch(1<<20, 2<<20),
		expiredTag: singleArch(1<<20, 3<<20),
		orphan:     singleArch(1<<20, 5<<20),
	}
	old := now.Add(-60 * 24 * time.Hour)
	// RegistryKeep=2 keeps the two newest digests, so a third tag is what makes
	// the expired one fall out of the window as well as out of the TTL.
	p := DefaultPolicy()
	p.RegistryKeep = 2
	plan, err := registryGraphPlan(
		[]Tag{{"app", "dev", live, now}, {"app", "prev", previous, now.Add(-time.Hour)}, {"app", "gone", expiredTag, old}},
		[]Revision{{Version{"app", live}, now}, {Version{"app", previous}, now.Add(-time.Hour)}, {Version{"app", expiredTag}, old}, {Version{"app", orphan}, old}},
		p, Registry{Repositories: []string{"app"}}, nil, now,
		func(v Version) ([]byte, error) { return []byte(bodies[v.Digest]), nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	var untagged, tagged uint64
	for _, e := range plan {
		if e.Untagged {
			untagged += e.Bytes
			continue
		}
		tagged += e.Bytes
	}
	r := Runner{Policy: p, Out: &out}
	for _, e := range plan {
		if e.Untagged {
			r.print("expire unreachable untagged manifest %s%s\n", e.Digest, sizeSuffix(e.Bytes, e.Sized))
			continue
		}
		r.print("expire tagged version %s (%s)%s\n", e.Digest, strings.Join(e.Tags, ", "), sizeSuffix(e.Bytes, e.Sized))
	}
	if tagged != 4<<20 || untagged != 6<<20 {
		t.Fatalf("size estimate: tagged=%d untagged=%d", tagged, untagged)
	}
	text := out.String()
	if !strings.Contains(text, "tagged version "+expiredTag+" (gone) (4.0 MiB)") {
		t.Fatalf("tagged line missing: %s", text)
	}
	if !strings.Contains(text, "unreachable untagged manifest "+orphan+" (6.0 MiB)") {
		t.Fatalf("untagged line missing: %s", text)
	}
	if formatBytes(tagged+untagged) != "10.0 MiB" {
		t.Fatal(formatBytes(tagged + untagged))
	}
}

func TestRegistryRevisionInventoryValidatesLinks(t *testing.T) {
	digest := testDigest("a")
	path := "/var/lib/registry/docker/registry/v2/repositories/team/app/_manifests/revisions/sha256/" + strings.TrimPrefix(digest, "sha256:") + "/link"
	modified := time.Unix(1700000000, 0)
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			link := digest
			if corrupt {
				link = testDigest("b")
			}
			r := Runner{Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "docker" || args[0] != "exec" || !strings.Contains(args[len(args)-1], "_manifests/revisions/sha256/") {
					t.Fatalf("unexpected inventory command: %s %v", name, args)
				}
				return []byte(fmt.Sprintf("%d\t%s\t%s\n", modified.Unix(), path, link)), nil
			}}
			got, err := r.revisions(context.Background(), "registry")
			if corrupt {
				if err == nil {
					t.Fatal("mismatched revision link accepted")
				}
				return
			}
			if err != nil || len(got) != 1 || got[0].Version != (Version{"team/app", digest}) {
				t.Fatalf("revision inventory: %v, %v", got, err)
			}
			if !got[0].Modified.Equal(modified) {
				t.Fatalf("revision mtime not carried through: %v", got[0].Modified)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
