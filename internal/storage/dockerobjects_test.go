package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/openfiles"
)

// fakeObjectDaemon is a Docker daemon's image, volume and container state behind
// the Runner.Command seam. It applies no guards of its own unless daemonGuards
// is set, so a test that forge must refuse something proves FORGE refused it:
// the fake would have removed it.
type fakeObjectDaemon struct {
	images     []fakeImage
	volumes    []fakeVolume
	containers []fakeObjectContainer

	// down fails every call, as with no daemon running.
	down bool
	// failPS fails only the container listing.
	failPS bool
	// danglingIgnoresMounts makes `volume ls --filter dangling=true` list
	// every volume: a container mounted one after the listing.
	danglingIgnoresMounts bool
	// daemonGuards refuses to remove an object a container uses, as the real
	// daemon does without --force.
	daemonGuards bool
	// tagBeforeRemove tags an image the moment forge re-reads its tags.
	tagBeforeRemove map[string]string
	// removeErr is returned by rmi / volume rm for that object.
	removeErr map[string]error

	ran            []string
	removedImages  []string
	removedVolumes []string
}

type fakeImage struct {
	id      string
	tags    []string
	digests []string
	created time.Time
	size    uint64
}

type fakeVolume struct {
	name    string
	driver  string // "" means local
	labels  map[string]string
	options map[string]string
	created time.Time
}

type fakeObjectContainer struct {
	imageID string
	volumes []string
}

// anonLabels is what the daemon puts on every volume it creates without a name.
var anonLabels = map[string]string{anonymousVolumeLabel: ""}

// volumeHash is a 64-hex volume name, as the daemon generates for an anonymous one.
func volumeHash(seed byte) string { return strings.Repeat(fmt.Sprintf("%02x", seed), 32) }

func (f *fakeObjectDaemon) imageUsed(id string) bool {
	return slices.ContainsFunc(f.containers, func(c fakeObjectContainer) bool { return c.imageID == id })
}

func (f *fakeObjectDaemon) volumeUsed(name string) bool {
	return slices.ContainsFunc(f.containers, func(c fakeObjectContainer) bool { return slices.Contains(c.volumes, name) })
}

func (f *fakeObjectDaemon) image(id string) *fakeImage {
	for i := range f.images {
		if f.images[i].id == id {
			return &f.images[i]
		}
	}
	return nil
}

func (f *fakeObjectDaemon) command(_ context.Context, name string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	f.ran = append(f.ran, joined)
	if name != "docker" {
		return nil, fmt.Errorf("unexpected %s %s", name, joined)
	}
	if f.down {
		return nil, errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?")
	}
	var out strings.Builder
	switch {
	case joined == "context inspect":
		return []byte(`[{"Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}]`), nil
	case joined == "image ls --filter dangling=true --no-trunc --quiet":
		for _, img := range f.images {
			if len(img.tags) == 0 {
				fmt.Fprintln(&out, img.id)
			}
		}
	case joined == "ps -aq":
		if f.failPS {
			return nil, errors.New("Error response from daemon: ps failed")
		}
		for i := range f.containers {
			fmt.Fprintf(&out, "c%d\n", i)
		}
	case strings.HasPrefix(joined, "inspect --format {{.Image}} {{.Config.Image}} "):
		for _, c := range f.containers {
			fmt.Fprintf(&out, "%s %s\n", c.imageID, "some/ref:1")
		}
	case strings.HasPrefix(joined, "inspect --format {{range .Mounts}}"):
		for _, c := range f.containers {
			fmt.Fprintln(&out, strings.Join(c.volumes, " "))
		}
	case strings.HasPrefix(joined, "image inspect --format {{json .RepoTags}} "):
		id := strings.TrimPrefix(joined, "image inspect --format {{json .RepoTags}} ")
		img := f.image(id)
		if img == nil {
			return nil, fmt.Errorf("Error: No such image: %s", id)
		}
		if tag, ok := f.tagBeforeRemove[id]; ok {
			img.tags = append(img.tags, tag)
		}
		b, _ := json.Marshal(append([]string{}, img.tags...))
		return b, nil
	case strings.HasPrefix(joined, `image inspect --format {"Id"`):
		for _, id := range args[4:] {
			img := f.image(id)
			if img == nil {
				return nil, fmt.Errorf("Error: No such image: %s", id)
			}
			b, _ := json.Marshal(map[string]any{
				"Id": img.id, "Created": img.created.UTC().Format(time.RFC3339Nano),
				"RepoTags": append([]string{}, img.tags...), "RepoDigests": append([]string{}, img.digests...), "Size": img.size,
			})
			fmt.Fprintln(&out, string(b))
		}
	case strings.HasPrefix(joined, "rmi "):
		id := strings.TrimPrefix(joined, "rmi ")
		if err := f.removeErr[id]; err != nil {
			return nil, err
		}
		if f.daemonGuards && f.imageUsed(id) {
			return nil, fmt.Errorf("Error response from daemon: conflict: unable to delete %s (must be forced) - image is being used by stopped container c0", id)
		}
		f.removedImages = append(f.removedImages, id)
		f.images = slices.DeleteFunc(f.images, func(img fakeImage) bool { return img.id == id })
	case joined == "volume ls --quiet --filter dangling=true":
		for _, v := range f.volumes {
			if f.danglingIgnoresMounts || !f.volumeUsed(v.name) {
				fmt.Fprintln(&out, v.name)
			}
		}
	case strings.HasPrefix(joined, "volume inspect "):
		var rows []map[string]any
		for _, name := range args[2:] {
			for _, v := range f.volumes {
				if v.name != name {
					continue
				}
				driver := v.driver
				if driver == "" {
					driver = "local"
				}
				rows = append(rows, map[string]any{
					"Name": v.name, "Driver": driver, "Scope": "local", "Labels": v.labels, "Options": v.options,
					"CreatedAt":  v.created.UTC().Format(time.RFC3339),
					"Mountpoint": "/var/lib/docker/volumes/" + v.name + "/_data",
				})
			}
		}
		b, _ := json.Marshal(rows)
		return b, nil
	case strings.HasPrefix(joined, "volume rm "):
		name := strings.TrimPrefix(joined, "volume rm ")
		if err := f.removeErr[name]; err != nil {
			return nil, err
		}
		if f.daemonGuards && f.volumeUsed(name) {
			return nil, fmt.Errorf("Error response from daemon: remove %s: volume is in use - [c0]", name)
		}
		f.removedVolumes = append(f.removedVolumes, name)
		f.volumes = slices.DeleteFunc(f.volumes, func(v fakeVolume) bool { return v.name == name })
	default:
		return nil, fmt.Errorf("unexpected docker %s", joined)
	}
	return []byte(out.String()), nil
}

// ranRemoval reports whether forge asked the daemon to remove object.
func (f *fakeObjectDaemon) ranRemoval(object string) bool {
	return slices.ContainsFunc(f.ran, func(c string) bool {
		return c == "rmi "+object || c == "volume rm "+object
	})
}

func objectRunner(t *testing.T, f *fakeObjectDaemon, out *strings.Builder) Runner {
	t.Helper()
	return Runner{Policy: DefaultPolicy(), Out: out, Command: f.command, PolicyPath: filepath.Join(t.TempDir(), "storage.json")}
}

func ago(d time.Duration) time.Time { return time.Now().Add(-d) }

const day = 24 * time.Hour

// ---- dangling images ----

// Positive control for every refusal below: an untagged image, built days
// ago, that no container uses, is removed — and printed.
func TestDanglingImagesRemovesAnUnusedOldUntaggedImage(t *testing.T) {
	f := &fakeObjectDaemon{images: []fakeImage{
		{id: "sha256:old", digests: []string{"localhost:5051/app@sha256:aa"}, created: ago(3 * day), size: 88 << 20},
	}}
	var out strings.Builder
	if err := objectRunner(t, f, &out).DanglingImages(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.removedImages, []string{"sha256:old"}) {
		t.Fatalf("removed %v, want the unused old dangling image\n%s", f.removedImages, out.String())
	}
	if !strings.Contains(out.String(), "removed dangling image sha256:old (localhost:5051/app, 88.0 MiB") {
		t.Fatalf("each removal must be printed:\n%s", out.String())
	}
}

// A preview prints every target and removes nothing.
func TestDanglingImagesPreviewPrintsEachTargetAndRemovesNothing(t *testing.T) {
	f := &fakeObjectDaemon{images: []fakeImage{
		{id: "sha256:a", created: ago(3 * day)},
		{id: "sha256:b", created: ago(2 * day)},
	}}
	var out strings.Builder
	if err := objectRunner(t, f, &out).DanglingImages(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(f.ran, func(c string) bool { return strings.HasPrefix(c, "rmi ") }) {
		t.Fatalf("a preview removed something: %v", f.ran)
	}
	for _, id := range []string{"sha256:a", "sha256:b"} {
		if !strings.Contains(out.String(), "remove dangling image "+id) {
			t.Fatalf("the preview does not list %s:\n%s", id, out.String())
		}
	}
}

// An image a container — here a STOPPED one — was created from is never
// removed, even by a daemon that would not have refused.
func TestDanglingImagesNeverRemovesAnImageAContainerUses(t *testing.T) {
	f := &fakeObjectDaemon{
		images:     []fakeImage{{id: "sha256:used", created: ago(30 * day)}, {id: "sha256:free", created: ago(30 * day)}},
		containers: []fakeObjectContainer{{imageID: "sha256:used"}},
	}
	var out strings.Builder
	if err := objectRunner(t, f, &out).DanglingImages(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if f.ranRemoval("sha256:used") {
		t.Fatalf("removed an image a stopped container uses\n%s", out.String())
	}
	if !slices.Equal(f.removedImages, []string{"sha256:free"}) {
		t.Fatalf("removed %v, want only sha256:free", f.removedImages)
	}
	if !strings.Contains(out.String(), "retained 1 in use by a container") {
		t.Fatalf("the in-use image must be counted as retained:\n%s", out.String())
	}
}

// When the container list cannot be read, which images are in use is unknown,
// so nothing is removed.
func TestDanglingImagesRetainEverythingWhenContainersCannotBeListed(t *testing.T) {
	f := &fakeObjectDaemon{
		images:     []fakeImage{{id: "sha256:used", created: ago(30 * day)}},
		containers: []fakeObjectContainer{{imageID: "sha256:used"}},
		failPS:     true,
	}
	err := objectRunner(t, f, &strings.Builder{}).DanglingImages(context.Background(), true)
	if err == nil || len(f.removedImages) != 0 {
		t.Fatalf("an unknown in-use set must retain everything and fail the layer: removed %v, err %v", f.removedImages, err)
	}
}

// No daemon: the layer fails, nothing is removed.
func TestDanglingImagesRetainEverythingWhenDockerIsUnavailable(t *testing.T) {
	f := &fakeObjectDaemon{images: []fakeImage{{id: "sha256:old", created: ago(30 * day)}}, down: true}
	err := objectRunner(t, f, &strings.Builder{}).DanglingImages(context.Background(), true)
	if err == nil || len(f.removedImages) != 0 {
		t.Fatalf("docker unavailable: removed %v, err %v", f.removedImages, err)
	}
}

// A dangling image built in the last hour may be one somebody is about to tag
// or run by ID.
func TestDanglingImagesRetainsAnImageBuiltInTheLastHour(t *testing.T) {
	f := &fakeObjectDaemon{images: []fakeImage{{id: "sha256:fresh", created: ago(10 * time.Minute)}}}
	var out strings.Builder
	if err := objectRunner(t, f, &out).DanglingImages(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if f.ranRemoval("sha256:fresh") {
		t.Fatalf("removed a dangling image built 10 minutes ago\n%s", out.String())
	}
	if !strings.Contains(out.String(), "1 built in the last 1h0m0s") {
		t.Fatalf("the young image must be counted as retained:\n%s", out.String())
	}
}

// A tagged image is never a target, and an image tagged between the plan and
// its removal is caught by the re-read: `docker rmi <id>` of an image with one
// tag would untag AND delete it.
func TestDanglingImagesNeverRemovesATaggedImage(t *testing.T) {
	f := &fakeObjectDaemon{
		images: []fakeImage{
			{id: "sha256:tagged", tags: []string{"app:dev"}, created: ago(30 * day)},
			{id: "sha256:raced", created: ago(30 * day)},
		},
		tagBeforeRemove: map[string]string{"sha256:raced": "app:keep"},
	}
	var out strings.Builder
	if err := objectRunner(t, f, &out).DanglingImages(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if f.ranRemoval("sha256:tagged") || f.ranRemoval("sha256:raced") {
		t.Fatalf("removed a tagged image: %v\n%s", f.removedImages, out.String())
	}
	if !strings.Contains(out.String(), "kept dangling image sha256:raced: tagged app:keep since the plan") {
		t.Fatalf("the raced image must be reported as kept:\n%s", out.String())
	}
}

// The daemon refusing (a container started using the image after the plan) is
// the last guard working: the image is kept, the layer does not fail, and the
// rest still go.
func TestDanglingImagesDaemonRefusalIsRetainedNotFailed(t *testing.T) {
	f := &fakeObjectDaemon{
		images: []fakeImage{{id: "sha256:a", created: ago(30 * day)}, {id: "sha256:b", created: ago(20 * day)}},
		removeErr: map[string]error{"sha256:a": errors.New(
			"Error response from daemon: conflict: unable to delete sha256:a (must be forced) - image is being used by stopped container 1")},
	}
	var out strings.Builder
	if err := objectRunner(t, f, &out).DanglingImages(context.Background(), true); err != nil {
		t.Fatalf("a daemon refusal must not fail the layer: %v", err)
	}
	if !slices.Equal(f.removedImages, []string{"sha256:b"}) || !strings.Contains(out.String(), "kept dangling image sha256:a") {
		t.Fatalf("removed %v\n%s", f.removedImages, out.String())
	}
}

// One pass asks the daemon for at most max_docker_removals_per_pass objects,
// oldest first, and says it was cut off; the next pass continues.
func TestDanglingImagesHonourThePassCap(t *testing.T) {
	f := &fakeObjectDaemon{}
	for i := range 5 {
		f.images = append(f.images, fakeImage{id: fmt.Sprintf("sha256:%d", i), created: ago(time.Duration(10-i) * day)})
	}
	r := objectRunner(t, f, &strings.Builder{})
	r.Policy.MaxDockerRemovalsPerPass = 2
	err := r.DanglingImages(context.Background(), true)
	var cut *CutOffError
	if !errors.As(err, &cut) || !errors.Is(err, errDockerCap) {
		t.Fatalf("a capped layer must be cut off: %v", err)
	}
	if !slices.Equal(f.removedImages, []string{"sha256:0", "sha256:1"}) {
		t.Fatalf("removed %v, want the two oldest", f.removedImages)
	}
	if err := objectRunner(t, f, &strings.Builder{}).DanglingImages(context.Background(), true); err != nil || len(f.images) != 0 {
		t.Fatalf("the next passes must finish the job: %v, left %v", err, f.images)
	}
}

// A daemon reporting EMFILE backs the pass off at once.
func TestDanglingImagesBackOffOnDaemonDistress(t *testing.T) {
	f := &fakeObjectDaemon{
		images: []fakeImage{{id: "sha256:a", created: ago(30 * day)}, {id: "sha256:b", created: ago(20 * day)}},
		removeErr: map[string]error{"sha256:a": errors.New(
			"Error response from daemon: unlinkat /var/lib/docker/overlay2/x: too many open files")},
	}
	r := objectRunner(t, f, &strings.Builder{})
	err := r.DanglingImages(context.Background(), true)
	var backoff *BackedOffError
	if !errors.As(err, &backoff) {
		t.Fatalf("daemon EMFILE must back off: %v", err)
	}
	if f.ranRemoval("sha256:b") {
		t.Fatal("kept removing after the daemon reported distress")
	}
}

// ---- anonymous volumes ----

// seedVolumeRecord writes an observation record saying each volume has been
// seen unused since the given time.
func seedVolumeRecord(t *testing.T, r Runner, since map[string]time.Time) {
	t.Helper()
	if err := writeStateFile(r.PolicyPath, AnonymousVolumeRecordPath(r.PolicyPath), volumeRecord{UnusedSince: since}); err != nil {
		t.Fatal(err)
	}
}

func explicitVolumeRunner(t *testing.T, f *fakeObjectDaemon, out *strings.Builder) Runner {
	r := objectRunner(t, f, out)
	r.Explicit = true
	return r
}

// Positive control for every refusal below: an anonymous, plain local volume,
// created and seen unused more than the window ago, is removed by an explicit
// apply — and printed.
func TestAnonymousVolumesExplicitApplyRemovesAnOldUnusedAnonymousVolume(t *testing.T) {
	anon := volumeHash(1)
	f := &fakeObjectDaemon{volumes: []fakeVolume{{name: anon, labels: anonLabels, created: ago(60 * day)}}}
	var out strings.Builder
	r := explicitVolumeRunner(t, f, &out)
	seedVolumeRecord(t, r, map[string]time.Time{anon: ago(20 * day)})
	if err := r.AnonymousVolumes(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.removedVolumes, []string{anon}) || !strings.Contains(out.String(), "removed anonymous volume "+anon) {
		t.Fatalf("removed %v\n%s", f.removedVolumes, out.String())
	}
}

// Named volumes are user data. Neither signal alone makes a volume anonymous:
// a 64-hex name without the daemon's label, or the label on a name a person
// chose, is retained; only both together are.
func TestAnonymousVolumesNeverRemovesANamedVolume(t *testing.T) {
	anon, unlabelledHex := volumeHash(1), volumeHash(2)
	f := &fakeObjectDaemon{volumes: []fakeVolume{
		{name: "control-plane_pgdata", labels: map[string]string{"com.docker.compose.volume": "pgdata"}, created: ago(90 * day)},
		{name: unlabelledHex, created: ago(90 * day)},                    // pre-23 daemon, or a person named it
		{name: "my-scratch", labels: anonLabels, created: ago(90 * day)}, // label forged on a chosen name
		{name: anon, labels: anonLabels, created: ago(90 * day)},         // the one real anonymous volume
	}}
	var out strings.Builder
	r := explicitVolumeRunner(t, f, &out)
	seedVolumeRecord(t, r, map[string]time.Time{"control-plane_pgdata": ago(60 * day), unlabelledHex: ago(60 * day), "my-scratch": ago(60 * day), anon: ago(60 * day)})
	if err := r.AnonymousVolumes(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	for _, named := range []string{"control-plane_pgdata", unlabelledHex, "my-scratch"} {
		if f.ranRemoval(named) {
			t.Fatalf("removed named volume %s\n%s", named, out.String())
		}
	}
	if !slices.Equal(f.removedVolumes, []string{anon}) {
		t.Fatalf("removed %v, want only the anonymous volume", f.removedVolumes)
	}
	if !strings.Contains(out.String(), "3 named (never removed)") {
		t.Fatalf("named volumes must be counted as retained:\n%s", out.String())
	}
}

// A volume a container mounts is never removed, even when the daemon's
// dangling filter listed it (a container mounted it after the listing) and the
// daemon would not have refused.
func TestAnonymousVolumesNeverRemovesAVolumeAContainerUses(t *testing.T) {
	used, free := volumeHash(1), volumeHash(2)
	f := &fakeObjectDaemon{
		volumes:               []fakeVolume{{name: used, labels: anonLabels, created: ago(60 * day)}, {name: free, labels: anonLabels, created: ago(60 * day)}},
		containers:            []fakeObjectContainer{{volumes: []string{used}}},
		danglingIgnoresMounts: true,
	}
	var out strings.Builder
	r := explicitVolumeRunner(t, f, &out)
	seedVolumeRecord(t, r, map[string]time.Time{used: ago(30 * day), free: ago(30 * day)})
	if err := r.AnonymousVolumes(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if f.ranRemoval(used) {
		t.Fatalf("removed a volume a stopped container mounts\n%s", out.String())
	}
	if !slices.Equal(f.removedVolumes, []string{free}) {
		t.Fatalf("removed %v, want only the free volume", f.removedVolumes)
	}
	// Seen in use, it leaves the record: its unused clock restarts.
	if _, ok := r.readVolumeRecord()[used]; ok {
		t.Fatal("a volume seen in use must leave the observation record")
	}
}

// When the container mounts cannot be read, which volumes are in use is
// unknown, so nothing is removed.
func TestAnonymousVolumesRetainEverythingWhenContainersCannotBeListed(t *testing.T) {
	anon := volumeHash(1)
	f := &fakeObjectDaemon{
		volumes:               []fakeVolume{{name: anon, labels: anonLabels, created: ago(60 * day)}},
		containers:            []fakeObjectContainer{{volumes: []string{anon}}},
		danglingIgnoresMounts: true,
		failPS:                true,
	}
	r := explicitVolumeRunner(t, f, &strings.Builder{})
	seedVolumeRecord(t, r, map[string]time.Time{anon: ago(30 * day)})
	if err := r.AnonymousVolumes(context.Background(), true); err == nil || len(f.removedVolumes) != 0 {
		t.Fatalf("an unknown in-use set must retain everything and fail the layer: removed %v, err %v", f.removedVolumes, err)
	}
}

// Unused for less than the window — by forge's own observation, or by the
// volume's age — is retained. A first sighting starts the clock now.
func TestAnonymousVolumesRetainsAVolumeUnusedForLessThanTheWindow(t *testing.T) {
	recent, firstSeen, youngVolume := volumeHash(1), volumeHash(2), volumeHash(3)
	f := &fakeObjectDaemon{volumes: []fakeVolume{
		{name: recent, labels: anonLabels, created: ago(60 * day)},     // seen unused only 2 days ago
		{name: firstSeen, labels: anonLabels, created: ago(60 * day)},  // never seen before
		{name: youngVolume, labels: anonLabels, created: ago(2 * day)}, // record claims longer than it has existed
	}}
	var out strings.Builder
	r := explicitVolumeRunner(t, f, &out)
	seedVolumeRecord(t, r, map[string]time.Time{recent: ago(2 * day), youngVolume: ago(30 * day)})
	before := time.Now()
	if err := r.AnonymousVolumes(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if len(f.removedVolumes) != 0 {
		t.Fatalf("removed %v, all unused for less than 14 days\n%s", f.removedVolumes, out.String())
	}
	if !strings.Contains(out.String(), "retained 3 unused for less") {
		t.Fatalf("young volumes must be counted as retained:\n%s", out.String())
	}
	if since := r.readVolumeRecord()[firstSeen]; since.Before(before) {
		t.Fatalf("a first sighting must start the clock now, recorded %v", since)
	}
}

// No daemon: the layer fails, nothing is removed, the record is untouched.
func TestAnonymousVolumesRetainEverythingWhenDockerIsUnavailable(t *testing.T) {
	anon := volumeHash(1)
	f := &fakeObjectDaemon{volumes: []fakeVolume{{name: anon, labels: anonLabels, created: ago(60 * day)}}, down: true}
	r := explicitVolumeRunner(t, f, &strings.Builder{})
	seeded := map[string]time.Time{anon: ago(30 * day)}
	seedVolumeRecord(t, r, seeded)
	if err := r.AnonymousVolumes(context.Background(), true); err == nil || len(f.removedVolumes) != 0 {
		t.Fatalf("docker unavailable: removed %v, err %v", f.removedVolumes, err)
	}
	if got := r.readVolumeRecord(); !got[anon].Equal(seeded[anon]) {
		t.Fatalf("an unreadable daemon must not rewrite the record: %v", got)
	}
}

// A volume with driver options mounts something outside Docker's data root,
// and a plugin driver's may be a cloud disk: anonymous or not, never removed.
func TestAnonymousVolumesRetainsDriverOptionsAndPluginDrivers(t *testing.T) {
	bind, plugin := volumeHash(1), volumeHash(2)
	f := &fakeObjectDaemon{volumes: []fakeVolume{
		{name: bind, labels: anonLabels, options: map[string]string{"type": "none", "o": "bind", "device": "/home/workspace"}, created: ago(60 * day)},
		{name: plugin, driver: "rexray/ebs", labels: anonLabels, created: ago(60 * day)},
	}}
	var out strings.Builder
	r := explicitVolumeRunner(t, f, &out)
	seedVolumeRecord(t, r, map[string]time.Time{bind: ago(30 * day), plugin: ago(30 * day)})
	if err := r.AnonymousVolumes(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if len(f.removedVolumes) != 0 || !strings.Contains(out.String(), "2 with a non-local driver or driver options") {
		t.Fatalf("removed %v\n%s", f.removedVolumes, out.String())
	}
}

// Only an explicit pass that applies removes a volume. An explicit preview
// prints each target; an automatic apply records and retains.
func TestAnonymousVolumesAreRemovedOnlyByAnExplicitApply(t *testing.T) {
	anon := volumeHash(1)
	setup := func(explicit bool) (*fakeObjectDaemon, Runner, *strings.Builder) {
		f := &fakeObjectDaemon{volumes: []fakeVolume{{name: anon, labels: anonLabels, created: ago(60 * day)}}}
		out := &strings.Builder{}
		r := objectRunner(t, f, out)
		r.Explicit = explicit
		seedVolumeRecord(t, r, map[string]time.Time{anon: ago(30 * day)})
		return f, r, out
	}

	f, r, out := setup(true)
	if err := r.AnonymousVolumes(context.Background(), false); err != nil || f.ranRemoval(anon) {
		t.Fatalf("explicit preview removed (%v): %v", err, f.ran)
	}
	if !strings.Contains(out.String(), "remove anonymous volume "+anon) {
		t.Fatalf("the explicit preview must print each target:\n%s", out.String())
	}

	f, r, out = setup(false)
	if err := r.AnonymousVolumes(context.Background(), true); err != nil || f.ranRemoval(anon) {
		t.Fatalf("an automatic apply removed a volume (%v)\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "only an explicit `forge storage gc --apply` removes volumes") {
		t.Fatalf("the automatic pass must say why it retained the volume:\n%s", out.String())
	}

	f, r, _ = setup(true)
	if err := r.AnonymousVolumes(context.Background(), true); err != nil || !f.ranRemoval(anon) {
		t.Fatalf("explicit apply did not remove (%v): %v", err, f.ran)
	}
}

// "off" turns the layer off: no docker call at all.
func TestAnonymousVolumesOffMakesNoDockerCall(t *testing.T) {
	f := &fakeObjectDaemon{}
	r := explicitVolumeRunner(t, f, &strings.Builder{})
	r.Policy.AnonymousVolumeUnused = "off"
	if err := r.AnonymousVolumes(context.Background(), true); err != nil || len(f.ran) != 0 {
		t.Fatalf("off: err %v, ran %v", err, f.ran)
	}
}

func TestAnonymousVolumeUnusedPolicyValidation(t *testing.T) {
	for value, ok := range map[string]bool{"336h": true, "24h": true, "off": true, "23h": false, "": false, "soon": false} {
		p := DefaultPolicy()
		p.AnonymousVolumeUnused = value
		if err := p.Validate(); (err == nil) != ok {
			t.Errorf("anonymous_volume_unused %q: Validate() = %v, want ok=%v", value, err, ok)
		}
	}
	p := DefaultPolicy()
	p.MaxDockerRemovalsPerPass = -1
	if p.Validate() == nil {
		t.Error("a negative max_docker_removals_per_pass must be rejected")
	}
}

// ---- the passes ----

// quietHostRunner is a Runner whose host layers have nothing to do, so a pass
// exercises only the Docker layers.
func quietHostRunner(t *testing.T, f *fakeObjectDaemon, out *strings.Builder) Runner {
	t.Helper()
	r := objectRunner(t, f, out)
	r.Policy.Builders = nil
	r.TempRoot, r.SourceCacheRoot = t.TempDir(), t.TempDir()
	r.GoCacheRoot, r.GolangciCacheRoot = t.TempDir(), t.TempDir()
	r.OpenPaths = func(context.Context) (openfiles.Snapshot, error) {
		return openfiles.FromPaths([]string{"/nonexistent/forge-storage-test/unrelated"}), nil
	}
	return r
}

// The automatic pass removes dangling images and never an anonymous volume,
// even from a runner marked explicit: it only starts the volume's clock.
func TestNonDisruptiveGCRemovesDanglingImagesButNeverAVolume(t *testing.T) {
	anon, fresh := volumeHash(1), volumeHash(2)
	f := &fakeObjectDaemon{
		images:  []fakeImage{{id: "sha256:old", created: ago(3 * day)}},
		volumes: []fakeVolume{{name: anon, labels: anonLabels, created: ago(60 * day)}, {name: fresh, labels: anonLabels, created: ago(60 * day)}},
	}
	var out strings.Builder
	r := quietHostRunner(t, f, &out)
	r.Explicit = true
	seedVolumeRecord(t, r, map[string]time.Time{anon: ago(30 * day)})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_ = r.NonDisruptiveGC(ctx, true)
	if !slices.Equal(f.removedImages, []string{"sha256:old"}) {
		t.Fatalf("the automatic pass did not remove the dangling image: %v\n%s", f.removedImages, out.String())
	}
	if len(f.removedVolumes) != 0 {
		t.Fatalf("the automatic pass removed volume(s) %v\n%s", f.removedVolumes, out.String())
	}
	if _, ok := r.readVolumeRecord()[fresh]; !ok {
		t.Fatalf("the automatic pass must start the clock of a newly unused volume\n%s", out.String())
	}
}

// The full pass removes anonymous volumes only when it is explicit: the
// installed daily job (`gc --apply --scheduled`) and `storage daemon` run it
// with Explicit false.
func TestGCRemovesAnonymousVolumesOnlyWhenExplicit(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		anon := volumeHash(1)
		f := &fakeObjectDaemon{volumes: []fakeVolume{{name: anon, labels: anonLabels, created: ago(60 * day)}}}
		var out strings.Builder
		r := quietHostRunner(t, f, &out)
		r.Explicit = explicit
		seedVolumeRecord(t, r, map[string]time.Time{anon: ago(30 * day)})
		_ = r.GC(context.Background(), true)
		if f.ranRemoval(anon) != explicit {
			t.Fatalf("explicit=%v: removed=%v\n%s", explicit, f.ranRemoval(anon), out.String())
		}
	}
}

// A daemon in distress while removing a dangling image ends the Docker layers
// of the full pass: the volume layer does not even list, and no builder is
// pruned.
func TestGCStopsAskingTheDaemonAfterItBacksOff(t *testing.T) {
	anon := volumeHash(1)
	f := &fakeObjectDaemon{
		images:  []fakeImage{{id: "sha256:a", created: ago(30 * day)}},
		volumes: []fakeVolume{{name: anon, labels: anonLabels, created: ago(60 * day)}},
		removeErr: map[string]error{"sha256:a": errors.New(
			"Error response from daemon: unlinkat /var/lib/docker/overlay2/x: too many open files")},
	}
	var out strings.Builder
	r := quietHostRunner(t, f, &out)
	r.Explicit = true
	r.Policy.Builders = []string{"default"}
	seedVolumeRecord(t, r, map[string]time.Time{anon: ago(30 * day)})
	err := r.GC(context.Background(), true)
	if res := NewGCResult(time.Now(), err); !slices.Contains(res.BackedOffLayers, "dangling images") {
		t.Fatalf("want the dangling-image layer recorded as backed off: %+v\n%s", res, out.String())
	}
	for _, c := range f.ran {
		if strings.HasPrefix(c, "volume ") || strings.HasPrefix(c, "buildx ") {
			t.Fatalf("asked the daemon %q after it backed off\n%s", c, out.String())
		}
	}
}

func TestDockerDistressRecognisesDaemonErrnoText(t *testing.T) {
	for msg, distress := range map[string]bool{
		"Error response from daemon: open /var/lib/docker/x: too many open files":   true,
		"Error response from daemon: too many open files in system":                 true,
		"Error response from daemon: read /var/lib/docker/x: input/output error":    true,
		"Error response from daemon: stat x: transport endpoint is not connected":   true,
		"Error response from daemon: conflict: unable to delete x (must be forced)": false,
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock":        false,
	} {
		if got := fsDistress(dockerDistress(errors.New(msg))); got != distress {
			t.Errorf("%q: distress=%v, want %v", msg, got, distress)
		}
	}
	if _, err := os.Stat("/nonexistent-forge-test"); fsDistress(dockerDistress(err)) {
		t.Error("ENOENT is not distress")
	}
}
