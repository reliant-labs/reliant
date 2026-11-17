package release

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const reliantSrc = "github.com/reliant-labs/reliant/db/migrations"

func manifest(source string, files ...string) MigrationManifest {
	return MigrationManifest{Sets: []MigrationSet{NewMigrationSet(source, "", files)}}
}

// The incident, as the ledger saw it. v1.7.52 applied to prod carrying
// ...035148; main then merged ...024221, a lower version, and v1.7.54 carries
// both. 024221 is the one file prod can never run, and nothing else is.
func TestPassedMigrationsNamesTheFileTheSchemaPassed(t *testing.T) {
	v152 := manifest(reliantSrc,
		"20261023235358_access_tokens_obs_write_scope.up.sql",
		"20261024035148_hosted_http_action_tickets.up.sql")
	v154 := manifest(reliantSrc,
		"20261023235358_access_tokens_obs_write_scope.up.sql",
		"20261024024221_chats_worktree_binding_index.up.sql",
		"20261024035148_hosted_http_action_tickets.up.sql",
		"20261024052601_daemon_attachment_build.up.sql")

	got := PassedMigrations(v154, []AppliedManifest{{Release: "v1.7.52", Manifest: v152}})
	want := []PassedMigration{{
		Source: reliantSrc, File: "20261024024221_chats_worktree_binding_index.up.sql", Version: 20261024024221,
		Head: 20261024035148, HeadFile: "20261024035148_hosted_http_action_tickets.up.sql", HeadRelease: "v1.7.52",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PassedMigrations = %+v\nwant %+v", got, want)
	}
}

// What the check must NOT refuse: a file above the head (an ordinary pending
// migration), a file an applied release already carried, and a directory the
// environment never applied from.
func TestPassedMigrationsAdmitsWhatCanApply(t *testing.T) {
	applied := []AppliedManifest{{Release: "v1", Manifest: manifest(reliantSrc, "00001_a.up.sql", "00003_c.up.sql")}}
	target := MigrationManifest{Sets: []MigrationSet{
		NewMigrationSet(reliantSrc, "", []string{"00001_a.up.sql", "00003_c.up.sql", "00004_d.up.sql"}),
		NewMigrationSet("github.com/acme/other/db/migrations", "", []string{"00001_x.up.sql"}),
	}}
	if got := PassedMigrations(target, applied); len(got) != 0 {
		t.Fatalf("PassedMigrations = %+v; want none", got)
	}
	// No history at all: a first deploy judges nothing.
	if got := PassedMigrations(target, nil); len(got) != 0 {
		t.Fatalf("PassedMigrations with no history = %+v; want none", got)
	}
}

// A version is not an identity: a DIFFERENT file at a version the env applied
// is still a file the env never ran — the same-second collision.
func TestPassedMigrationsComparesFilesNotVersions(t *testing.T) {
	applied := []AppliedManifest{{Release: "v1", Manifest: manifest(reliantSrc, "00001_a.up.sql", "00002_b.up.sql")}}
	target := manifest(reliantSrc, "00001_a.up.sql", "00002_b.up.sql", "00002_other.up.sql")
	got := PassedMigrations(target, applied)
	if len(got) != 1 || got[0].File != "00002_other.up.sql" {
		t.Fatalf("PassedMigrations = %+v; want exactly 00002_other.up.sql", got)
	}
}

// The head is the highest version ANY applied release carried, and a file is
// applied when ANY of them carried it: a rollback to an older release leaves
// the schema at the newer one's head.
func TestPassedMigrationsUsesTheWholeAppliedHistory(t *testing.T) {
	applied := []AppliedManifest{
		{Release: "v1", Manifest: manifest(reliantSrc, "00001_a.up.sql")}, // current: rolled back
		{Release: "v2", Manifest: manifest(reliantSrc, "00001_a.up.sql", "00005_e.up.sql")},
	}
	got := PassedMigrations(manifest(reliantSrc, "00001_a.up.sql", "00003_c.up.sql"), applied)
	if len(got) != 1 || got[0].File != "00003_c.up.sql" || got[0].HeadRelease != "v2" || got[0].Head != 5 {
		t.Fatalf("PassedMigrations = %+v; want 00003_c.up.sql passed by v2's head 5", got)
	}
}

func TestNewMigrationSetKeepsOnlyForwardMigrationsInVersionOrder(t *testing.T) {
	s := NewMigrationSet(reliantSrc, "", []string{
		"20261024035148_b.up.sql", "README.md", "00002_x.down.sql", "embed_gen.go",
		"00010_a.up.sql", "notaversion_a.up.sql", "00010_a.up.sql", "00002_y.up.sql",
	})
	want := []string{"00002_y.up.sql", "00010_a.up.sql", "20261024035148_b.up.sql"}
	if !reflect.DeepEqual(s.Files, want) {
		t.Fatalf("Files = %v; want %v", s.Files, want)
	}
}

func TestMigrationManifestValidate(t *testing.T) {
	ok := manifest(reliantSrc, "00001_a.up.sql", "00001_b.up.sql") // a duplicate VERSION is describable
	if err := ok.Validate(); err != nil {
		t.Fatalf("Validate(%+v) = %v; want nil", ok, err)
	}
	for name, m := range map[string]MigrationManifest{
		"no source":      {Sets: []MigrationSet{{Files: []string{"00001_a.up.sql"}}}},
		"twice":          {Sets: []MigrationSet{{Source: "s"}, {Source: "s"}}},
		"short commit":   {Sets: []MigrationSet{{Source: "s", Commit: "abc123"}}},
		"a path":         {Sets: []MigrationSet{{Source: "s", Files: []string{"db/00001_a.up.sql"}}}},
		"a down file":    {Sets: []MigrationSet{{Source: "s", Files: []string{"00001_a.down.sql"}}}},
		"no version":     {Sets: []MigrationSet{{Source: "s", Files: []string{"a_b.up.sql"}}}},
		"duplicate file": {Sets: []MigrationSet{{Source: "s", Files: []string{"00001_a.up.sql", "00001_a.up.sql"}}}},
	} {
		if err := m.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Validate = %v; want ErrInvalid", name, err)
		}
	}
}

func TestMigrationManifestEqualIgnoresOrderAndCommit(t *testing.T) {
	a := MigrationManifest{Sets: []MigrationSet{
		{Source: "x", Commit: strings.Repeat("a", 40), Files: []string{"00001_a.up.sql", "00002_b.up.sql"}},
		{Source: "y", Files: []string{"00001_q.up.sql"}},
	}}
	b := MigrationManifest{Sets: []MigrationSet{
		{Source: "y", Files: []string{"00001_q.up.sql"}},
		{Source: "x", Files: []string{"00002_b.up.sql", "00001_a.up.sql"}},
	}}
	if !a.Equal(b) {
		t.Fatal("manifests listing the same files under the same sources compare unequal")
	}
	b.Sets[0].Files = append(b.Sets[0].Files, "00002_r.up.sql")
	if a.Equal(b) {
		t.Fatal("manifests with different files compare equal")
	}
}

// Evidence, not identity: a re-cut that records a manifest the first cut did
// not is the same release, so it is an idempotent retry and not a conflict —
// and the backfill does not turn a historical release into a different one.
func TestSameContentIgnoresMigrations(t *testing.T) {
	r := Release{
		Version:   "v1.0.0",
		CreatedAt: time.Unix(0, 0).UTC(),
		Artifacts: map[string]Artifact{"api": {
			Kind: KindOCI, Mode: ModeShared,
			Digests: map[string]string{SharedVariant: "sha256:" + strings.Repeat("a", 64)},
		}},
	}
	with := r
	m := manifest(reliantSrc, "00001_a.up.sql")
	with.Migrations = &m
	if err := with.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !r.SameContent(with) {
		t.Fatal("a release and the same release with a migration manifest are not SameContent")
	}
	bad := r
	bad.Migrations = &MigrationManifest{Sets: []MigrationSet{{Source: ""}}}
	if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate with a malformed manifest = %v; want ErrInvalid", err)
	}
}
