package schemadef

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// MigrationSet is the ordered *.up.sql files of one migrations directory,
// read ONCE: the exact bytes a shadow applies, and the digest that names
// them.
//
// Reading the bytes once is what makes the digest trustworthy as a cache
// key. A caller that hashed the directory and then applied it by reading the
// files again could hash one version of a migration and apply another (an
// editor or a scaffold writing between the two reads), and would then file a
// schema under a name that does not describe it. A set applies the bytes it
// hashed, so the two cannot disagree.
type MigrationSet struct {
	files  []migrationFile
	digest string
}

type migrationFile struct {
	name string
	sql  string
}

// migrationSetDigestVersion is folded into every digest. Bump it if what a
// digest describes ever changes — another file kind, a different order — so
// a digest computed under the old meaning can never name a schema built
// under the new one.
const migrationSetDigestVersion = "forge-schemadef-migration-set/v1"

// ReadMigrationSet reads every *.up.sql under migDir, in lexical order — the
// order a shadow applies them in. A missing directory is an empty set, not an
// error: a project with no migrations has no schema yet.
func ReadMigrationSet(migDir string) (*MigrationSet, error) {
	entries, err := os.ReadDir(migDir)
	if err != nil {
		if os.IsNotExist(err) {
			return newMigrationSet(nil), nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	files := make([]migrationFile, 0, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(migDir, name))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		files = append(files, migrationFile{name: name, sql: string(raw)})
	}
	return newMigrationSet(files), nil
}

func newMigrationSet(files []migrationFile) *MigrationSet {
	h := sha256.New()
	h.Write([]byte(migrationSetDigestVersion))
	h.Write([]byte{0})
	for _, f := range files {
		// Length-prefixed, so no choice of names and contents can make two
		// different sets hash the same stream.
		h.Write([]byte(f.name))
		h.Write([]byte{0})
		h.Write([]byte(strconv.Itoa(len(f.sql))))
		h.Write([]byte{0})
		h.Write([]byte(f.sql))
	}
	return &MigrationSet{files: files, digest: hex.EncodeToString(h.Sum(nil))}
}

// Len is the number of migration files in the set.
func (m *MigrationSet) Len() int { return len(m.files) }

// Digest names the set's content: every file's name and bytes, in apply
// order. Two sets with the same digest apply the same statements in the same
// order, wherever their directories are.
func (m *MigrationSet) Digest() string { return m.digest }

// apply replays the set, in order, onto db.
func (m *MigrationSet) apply(db *sql.DB) error {
	for _, f := range m.files {
		if err := applyMigration(db, f.sql); err != nil {
			return fmt.Errorf("apply migration %s to shadow schema: %w", f.name, err)
		}
	}
	return nil
}

// Template is a migration set applied ONCE to a scratch database that is
// afterwards only ever copied, never handed out.
//
// Applying a project's migrations is the expensive half of reading its
// schema: every statement of every file, in order, on a real postgres, then a
// catalog walk over every table. One `forge generate` used to pay it once per
// consumer — the entity projection behind the ORM, the CRUD wiring, the
// frontend pages, nav and mocks, the test factories — for the same unchanged
// directory each time. A Template pays it once. Tables is the introspected
// model; Open hands out a private live copy (CREATE DATABASE … TEMPLATE) to a
// caller that must execute SQL against the schema, for the cost of a file
// copy instead of a replay.
//
// The template database itself is never handed out. A caller that wrote to
// it — even a rolled-back INSERT advances a sequence — would change what the
// next copy starts from, and the copies are only interchangeable with a
// fresh replay because nobody touches the original.
//
// The caller owns Close, which drops the template database. Copies already
// opened are independent databases and survive it.
type Template struct {
	digest  string
	tables  []Table
	views   []Table
	baseURL string
	name    string
	cleanup func()
}

// ApplyTemplateAt applies the set to a fresh scratch database on the server
// at baseURL (pkg/pgtest's default ephemeral server when empty), introspects
// it, and releases every connection to it so it can be copied.
//
// The model Tables returns is exactly ApplyAndIntrospectAt's for the same
// files: the same replay, the same statement policy, the same catalog walk.
//
// An empty set is an error rather than an empty template: a project with no
// migrations needs no postgres at all, and callers check Len first so that
// one never boots one.
func (m *MigrationSet) ApplyTemplateAt(baseURL string) (*Template, error) {
	if m.Len() == 0 {
		return nil, errors.New("schemadef: no migrations to apply to a template")
	}
	db, cleanup, err := openShadow(baseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres shadow db: %w", err)
	}
	fail := func(err error) (*Template, error) {
		cleanup()
		return nil, err
	}
	var name string
	if err := db.QueryRow("SELECT current_database()").Scan(&name); err != nil {
		return fail(fmt.Errorf("name the shadow template database: %w", err))
	}
	if err := m.apply(db); err != nil {
		return fail(err)
	}
	tables, err := introspect(db)
	if err != nil {
		return fail(err)
	}
	views, err := IntrospectViews(db)
	if err != nil {
		return fail(err)
	}
	// Postgres copies a template only while no other session is connected
	// to it; this pool is the only one that ever was.
	if err := db.Close(); err != nil {
		return fail(fmt.Errorf("release the shadow template database: %w", err))
	}
	return &Template{digest: m.digest, tables: tables, views: views, baseURL: baseURL, name: name, cleanup: cleanup}, nil
}

// Views returns the applied schema's views (IntrospectViews), a deep copy
// per call like Tables.
func (t *Template) Views() []Table { return cloneTables(t.views) }

// Digest is the digest of the migration set the template was built from.
func (t *Template) Digest() string { return t.digest }

// Tables returns the applied schema. Each call returns its own deep copy, so
// one consumer sorting or trimming its tables cannot change what the next one
// reads.
func (t *Template) Tables() []Table { return cloneTables(t.tables) }

// Open returns a private live copy of the template: a new scratch database
// holding the applied schema, which the caller may write to freely and must
// Close. It is indistinguishable from a database the migrations were just
// replayed into, without the replay.
func (t *Template) Open() (*Shadow, error) {
	var (
		db      *sql.DB
		cleanup func()
		err     error
	)
	if t.baseURL != "" {
		db, cleanup, err = pgtest.NewCloneAtURL(t.baseURL, t.name)
	} else {
		db, cleanup, err = pgtest.NewClone(t.name)
	}
	if err != nil {
		return nil, fmt.Errorf("copy shadow template %s: %w", t.name, err)
	}
	return &Shadow{db: db, cleanup: cleanup}, nil
}

// Close drops the template database. Safe to call more than once and on a
// nil *Template.
func (t *Template) Close() {
	if t == nil || t.cleanup == nil {
		return
	}
	t.cleanup()
	t.cleanup = nil
}

// cloneTables deep-copies a schema model: no slice in the copy shares a
// backing array with the original. TestCloneTables_SharesNothing walks the
// types by reflection, so a slice field added to the model without a line
// here fails it.
func cloneTables(in []Table) []Table {
	if in == nil {
		return nil
	}
	out := make([]Table, len(in))
	for i, t := range in {
		t.Columns = slices.Clone(t.Columns)
		t.Triggers = slices.Clone(t.Triggers)
		t.PKCols = slices.Clone(t.PKCols)
		t.Indexes = slices.Clone(t.Indexes)
		for j := range t.Indexes {
			t.Indexes[j].Columns = slices.Clone(t.Indexes[j].Columns)
			t.Indexes[j].Keys = slices.Clone(t.Indexes[j].Keys)
		}
		t.ForeignKeys = slices.Clone(t.ForeignKeys)
		t.Checks = slices.Clone(t.Checks)
		for j := range t.Checks {
			t.Checks[j].Columns = slices.Clone(t.Checks[j].Columns)
		}
		out[i] = t
	}
	return out
}
