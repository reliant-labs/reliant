package cutjournal

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
)

func sampleJournal() Journal {
	return Journal{
		Project: "shop", Env: "prod", Version: "v1.2.3-rc.1", ForgeVersion: "v0.9.0",
		Commit: "0123456789abcdef0123456789abcdef01234567", Tree: "tree1",
		Images: []Image{
			{Repository: "reg.example/p/worker", Tag: "v1.2.3-rc.1", Digest: "sha256:" + strings.Repeat("b", 64)},
			{Repository: "reg.example/p/api", Tag: "v1.2.3-rc.1", Digest: "sha256:" + strings.Repeat("a", 64)},
		},
		State:   []File{{Name: "build-prod.json", Data: []byte(`{"image":"reg.example/p/api"}`)}},
		Outputs: []File{{Name: "observability/metrics-schema.json", Data: []byte(`{}`)}},
	}
}

func TestTag(t *testing.T) {
	for _, tc := range []struct {
		version, want string
		ok            bool
	}{
		{"v1.7.94-rc.1", "v1.7.94-rc.1.forge-cut", true},
		{"v1.0.0", "v1.0.0.forge-cut", true},
		{"", "", false},
		{"v1.0.0+build.5", "", false},         // `+` is not a tag character
		{strings.Repeat("v", 125), "", false}, // over 128 with the suffix
		{".hidden", "", false},                // a tag cannot start with a dot
	} {
		got, err := Tag(tc.version)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("Tag(%q) = %q, %v; want %q ok=%v", tc.version, got, err, tc.want, tc.ok)
		}
	}
}

func TestLocationIsTheFirstSortedDestination(t *testing.T) {
	if got := Location([]string{"reg/p/web", " ", "reg/p/api", "reg/p/worker"}); got != "reg/p/api" {
		t.Fatalf("Location = %q, want reg/p/api", got)
	}
	if got := Location(nil); got != "" {
		t.Fatalf("Location(nil) = %q, want empty", got)
	}
}

func TestEncodeDecodeRoundTripsAndSorts(t *testing.T) {
	raw, err := Encode(sampleJournal())
	if err != nil {
		t.Fatal(err)
	}
	j, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if j.Schema != Schema || j.Images[0].Repository != "reg.example/p/api" {
		t.Fatalf("decoded %+v: want schema set and images sorted by repository", j)
	}
	if !bytes.Equal(j.State[0].Data, []byte(`{"image":"reg.example/p/api"}`)) {
		t.Fatalf("state bytes changed in the round trip: %q", j.State[0].Data)
	}
	// Encoding is canonical: input order does not change the bytes.
	again, _ := Encode(j)
	if !bytes.Equal(raw, again) {
		t.Fatal("re-encoding a decoded journal changed its bytes")
	}
}

// A journal is read back from a registry, so nothing in it may make a resume
// write outside the state directory or the build output directory.
func TestDecodeRefusesPathsOutsideTheirDirectories(t *testing.T) {
	for name, mutate := range map[string]func(*Journal){
		"state traversal":   func(j *Journal) { j.State[0].Name = "../build-prod.json" },
		"state subdir":      func(j *Journal) { j.State[0].Name = "x/build-prod.json" },
		"state not a build": func(j *Journal) { j.State[0].Name = "credentials.json" },
		"output traversal":  func(j *Journal) { j.Outputs[0].Name = "../../etc/passwd" },
		"output absolute":   func(j *Journal) { j.Outputs[0].Name = "/etc/passwd" },
		"output unclean":    func(j *Journal) { j.Outputs[0].Name = "a/../b" },
		"no images":         func(j *Journal) { j.Images = nil },
		"no commit":         func(j *Journal) { j.Commit = "" },
		"bad digest":        func(j *Journal) { j.Images[0].Digest = "latest" },
	} {
		j := sampleJournal()
		mutate(&j)
		if _, err := Encode(j); err == nil {
			t.Errorf("%s: Encode accepted it", name)
		}
	}
	if _, err := Decode([]byte(`{"schema":"forge.dev/cut-journal/v0"}`)); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Errorf("Decode accepted a foreign schema: %v", err)
	}
}

func TestMismatches(t *testing.T) {
	j := sampleJournal()
	same := Expect{Project: "shop", Env: "prod", Version: j.Version, ForgeVersion: "v0.9.0", Commit: j.Commit, Tree: "tree1"}
	if got := j.Mismatches(same); len(got) != 0 {
		t.Fatalf("the cut that wrote it: %v, want none", got)
	}
	for name, tc := range map[string]struct {
		edit func(*Expect, *Journal)
		want string
	}{
		"other commit":  {func(e *Expect, _ *Journal) { e.Commit = "fedcba9876543210" }, "built from commit 0123456789ab and this checkout is at fedcba987654"},
		"no commit":     {func(e *Expect, _ *Journal) { e.Commit = "" }, "this checkout is at (none)"},
		"dirty here":    {func(e *Expect, _ *Journal) { e.Dirty = true }, "this checkout is dirty"},
		"dirty there":   {func(_ *Expect, j *Journal) { j.Dirty = true }, "built from a dirty checkout"},
		"other tree":    {func(e *Expect, _ *Journal) { e.Tree = "tree2" }, "built from tree tree1"},
		"other forge":   {func(e *Expect, _ *Journal) { e.ForgeVersion = "v0.9.1" }, "built by forge v0.9.0 and this is forge v0.9.1"},
		"other env":     {func(e *Expect, _ *Journal) { e.Env = "staging" }, "cut for env prod, not staging"},
		"other version": {func(e *Expect, _ *Journal) { e.Version = "v1.2.3-rc.2" }, "records release v1.2.3-rc.1, not v1.2.3-rc.2"},
		"other project": {func(e *Expect, _ *Journal) { e.Project = "cart" }, "cut by project shop, not cart"},
	} {
		e, jj := same, sampleJournal()
		tc.edit(&e, &jj)
		got := strings.Join(jj.Mismatches(e), "; ")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: mismatches %q, want one containing %q", name, got, tc.want)
		}
	}
}

func TestPushFetchRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	j := sampleJournal()
	digest, err := Push(ctx, store, "v1.2.3-rc.1.forge-cut", j)
	if err != nil {
		t.Fatal(err)
	}
	got, gotDigest, err := Fetch(ctx, store, "v1.2.3-rc.1.forge-cut")
	if err != nil {
		t.Fatal(err)
	}
	if gotDigest != digest || got.Commit != j.Commit || len(got.Images) != 2 || len(got.State) != 1 {
		t.Fatalf("Fetch = %+v @ %s, want the pushed journal @ %s", got, gotDigest, digest)
	}
	// The manifest is a pure function of the journal: re-pushing it is a no-op.
	again, err := Push(ctx, store, "v1.2.3-rc.1.forge-cut", j)
	if err != nil || again != digest {
		t.Fatalf("re-push = %s, %v; want %s", again, err, digest)
	}
}

func TestFetchNotFoundAndForeignArtifacts(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	if _, _, err := Fetch(ctx, store, "v9.forge-cut"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Fetch of an absent tag = %v, want ErrNotFound", err)
	}
	// An image someone tagged with the journal's name is not a journal.
	img := pushImage(t, store, "v9.forge-cut", "not a journal")
	if _, _, err := Fetch(ctx, store, "v9.forge-cut"); err == nil || !strings.Contains(err.Error(), "is not a cut journal") {
		t.Fatalf("Fetch of image %s = %v, want a refusal", img, err)
	}
}

func TestCheckImages(t *testing.T) {
	ctx := context.Background()
	stores := map[string]*memory.Store{"reg.example/p/api": memory.New(), "reg.example/p/worker": memory.New()}
	open := func(repo string) (content.Resolver, error) {
		s, ok := stores[repo]
		if !ok {
			return nil, errors.New("no such registry")
		}
		return s, nil
	}
	j := sampleJournal()
	j.Images[0].Digest = pushImage(t, stores["reg.example/p/worker"], j.Version, "worker")
	j.Images[1].Digest = pushImage(t, stores["reg.example/p/api"], j.Version, "api")
	if got := CheckImages(ctx, j, open); len(got) != 0 {
		t.Fatalf("every tag names its recorded digest: got %v", got)
	}

	moved := j
	moved.Images = append([]Image(nil), j.Images...)
	moved.Images[1].Digest = "sha256:" + strings.Repeat("c", 64)
	got := strings.Join(CheckImages(ctx, moved, open), "; ")
	if !strings.Contains(got, "reg.example/p/api:v1.2.3-rc.1 names "+j.Images[1].Digest) {
		t.Fatalf("a moved tag: %q", got)
	}

	gone := j
	gone.Images = []Image{{Repository: "reg.example/p/worker", Tag: "v0-missing", Digest: j.Images[0].Digest}, {Repository: "reg.example/p/nowhere", Tag: "x", Digest: j.Images[0].Digest}}
	got = strings.Join(CheckImages(ctx, gone, open), "; ")
	if !strings.Contains(got, "v0-missing no longer exists") || !strings.Contains(got, "reg.example/p/nowhere:x could not be read") {
		t.Fatalf("a missing tag and an unreadable repository: %q", got)
	}
}

// pushImage pushes a minimal image manifest tagged tag and returns its digest.
func pushImage(t *testing.T, store *memory.Store, tag, body string) string {
	t.Helper()
	ctx := context.Background()
	layer := []byte(body)
	layerDesc := content.NewDescriptorFromBytes("application/vnd.oci.image.layer.v1.tar", layer)
	raw := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"` +
		ocispec.DescriptorEmptyJSON.Digest.String() + `","size":2},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"` +
		layerDesc.Digest.String() + `","size":` + strconv.Itoa(len(layer)) + `}]}`)
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, raw)
	for _, b := range []struct {
		d ocispec.Descriptor
		v []byte
	}{{layerDesc, layer}, {ocispec.DescriptorEmptyJSON, ocispec.DescriptorEmptyJSON.Data}, {desc, raw}} {
		if err := store.Push(ctx, b.d, bytes.NewReader(b.v)); err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatal(err)
		}
	}
	if err := store.Tag(ctx, desc, tag); err != nil {
		t.Fatal(err)
	}
	return desc.Digest.String()
}
