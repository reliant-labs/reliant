package dbquery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// InspectRole tells a reader from every kind of role that can do more: one
// that may write a table, one that owns one, one that can SET ROLE to the
// owner without inheriting it, one that can terminate other sessions, and a
// role attribute, one that may run a SECURITY DEFINER function, and one that
// may advance a sequence, one that owns only a sequence it has revoked its
// own grants on, and one that may read server files.
func TestInspectRole(t *testing.T) {
	testkit.Slow(t, "boots a real postgres (pgtest)")
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })

	// Roles are cluster-wide on a shared server: name them per run.
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	sfx := "_" + hex.EncodeToString(b)
	owner, reader, writer, member, signaler, creator := "own"+sfx, "ro"+sfx, "rw"+sfx, "mem"+sfx, "sig"+sfx, "cr"+sfx
	definer, sequencer, seqOwner, fileReader := "def"+sfx, "seq"+sfx, "sqo"+sfx, "frd"+sfx
	exec := func(sql string) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	for _, r := range []string{owner, reader, writer, member, signaler, definer, sequencer, seqOwner, fileReader} {
		exec(fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'pw'`, r))
	}
	exec(fmt.Sprintf(`CREATE ROLE %s LOGIN CREATEDB PASSWORD 'pw'`, creator))
	t.Cleanup(func() {
		for _, r := range []string{member, signaler, creator, definer, sequencer, seqOwner, fileReader, reader, writer, owner} {
			_, _ = admin.Exec(ctx, fmt.Sprintf(`DROP OWNED BY %s`, r))
			_, _ = admin.Exec(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, r))
		}
	})
	exec(fmt.Sprintf(`GRANT CREATE ON SCHEMA public TO %s`, owner))
	exec(fmt.Sprintf(`SET ROLE %s`, owner))
	exec(`CREATE TABLE ledger (id int PRIMARY KEY, amount int)`)
	exec(`CREATE SEQUENCE ledger_ids`)
	exec(`CREATE FUNCTION bump() RETURNS void LANGUAGE sql SECURITY DEFINER AS 'UPDATE ledger SET amount = amount + 1'`)
	exec(`REVOKE EXECUTE ON FUNCTION bump() FROM PUBLIC`)
	exec(`RESET ROLE`)
	exec(fmt.Sprintf(`GRANT CREATE ON SCHEMA public TO %s`, seqOwner))
	exec(fmt.Sprintf(`SET ROLE %s`, seqOwner))
	exec(`CREATE SEQUENCE orphan_ids`)
	exec(fmt.Sprintf(`REVOKE ALL ON SEQUENCE orphan_ids FROM %s`, seqOwner))
	exec(`RESET ROLE`)
	exec(fmt.Sprintf(`REVOKE CREATE ON SCHEMA public FROM %s`, seqOwner))
	exec(fmt.Sprintf(`GRANT pg_read_server_files TO %s`, fileReader))
	exec(fmt.Sprintf(`GRANT SELECT ON ledger TO %s, %s, %s`, reader, writer, signaler))
	exec(fmt.Sprintf(`GRANT UPDATE (amount) ON ledger TO %s`, writer))
	exec(fmt.Sprintf(`ALTER ROLE %s NOINHERIT`, member))
	exec(fmt.Sprintf(`GRANT %s TO %s`, owner, member))
	exec(fmt.Sprintf(`GRANT pg_signal_backend TO %s`, signaler))
	exec(fmt.Sprintf(`GRANT SELECT ON ledger TO %s, %s`, definer, sequencer))
	exec(fmt.Sprintf(`GRANT EXECUTE ON FUNCTION bump() TO %s`, definer))
	exec(fmt.Sprintf(`GRANT USAGE ON SEQUENCE ledger_ids TO %s`, sequencer))

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	inspect := func(role string) Role {
		t.Helper()
		as := *u
		as.User = url.UserPassword(role, "pw")
		c, err := pgx.Connect(ctx, as.String())
		if err != nil {
			t.Fatalf("connect as %s: %v", role, err)
		}
		defer func() { _ = c.Close(ctx) }()
		r, err := InspectRole(ctx, c)
		if err != nil {
			t.Fatalf("InspectRole as %s: %v", role, err)
		}
		if r.Name != role {
			t.Errorf("Name = %q, want %q", r.Name, role)
		}
		return r
	}

	if r := inspect(reader); r.Writable() {
		t.Errorf("a SELECT-only role is a reader, got %v", r.Writes)
	}
	for role, want := range map[string]string{
		writer:     "may write 1 relations",
		owner:      "owns 2 relations",
		member:     "owns 2 relations",
		signaler:   "member of pg_signal_backend",
		creator:    "CREATEDB",
		definer:    "may execute 1 SECURITY DEFINER functions",
		sequencer:  "may advance 1 sequences",
		seqOwner:   "owns 1 relations",
		fileReader: "member of pg_read_server_files",
	} {
		r := inspect(role)
		if !r.Writable() || !strings.Contains(strings.Join(r.Writes, "; "), want) {
			t.Errorf("%s: Writes = %v, want one naming %q", role, r.Writes, want)
		}
	}
}
