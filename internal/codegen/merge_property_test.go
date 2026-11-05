package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/mergeproperty"
	"github.com/reliant-labs/forge/pkg/dbgrants"
)

// The merge invariant (internal/mergeproperty) for codegen's aggregate
// outputs.

// config_gen.k: one branch adds a field to the email block, the other a
// field (and a credential) to the stripe block. Each block's schema, env
// entries and secret refs are their own sections, with the nats block's in
// between untouched.
func TestMergeProperty_ConfigKCL(t *testing.T) {
	field := func(block, name string, sensitive bool) ConfigField {
		return ConfigField{
			Name: name, ProtoType: "string", GoType: "string",
			EnvVar: strings.ToUpper(name), Sensitive: sensitive,
			KCLBlock: block, KCLBlockType: strings.ToUpper(block[:1]) + block[1:] + "Config",
		}
	}
	root := ConfigField{Name: "log_level", ProtoType: "string", GoType: "string", EnvVar: "LOG_LEVEL", DefaultValue: "info"}
	fields := func(extraEmail, extraStripe bool) []ConfigField {
		fs := []ConfigField{root, field("email", "email_from", false), field("email", "resend_key", true)}
		if extraEmail {
			fs = append(fs, field("email", "email_reply_to", false))
		}
		fs = append(fs, field("nats", "nats_url", false), field("nats", "nats_password", true), field("stripe", "stripe_api_base", false))
		if extraStripe {
			fs = append(fs, field("stripe", "stripe_webhook_url", false))
		}
		return append(fs, field("stripe", "stripe_key", true))
	}
	// Neighbours: ours adds a field to the nats block; theirs declares a new
	// kafka block directly before it. Every part of the kafka block (schema,
	// env entries, secret refs) lands right before a nats part ours changed.
	neighbours := func(extraNats, kafka bool) []ConfigField {
		fs := []ConfigField{root, field("email", "email_from", false)}
		if kafka {
			fs = append(fs, field("kafka", "kafka_brokers", false), field("kafka", "kafka_password", true))
		}
		fs = append(fs, field("nats", "nats_url", false))
		if extraNats {
			fs = append(fs, field("nats", "nats_subject", false), field("nats", "nats_token", true))
		}
		return append(fs, field("nats", "nats_password", true))
	}
	for name, variants := range map[string]map[string][]ConfigField{
		"apart": {
			"base": fields(false, false), "ours": fields(true, false),
			"theirs": fields(false, true), "merged": fields(true, true),
		},
		"block born before a changed neighbour": {
			"base": neighbours(false, false), "ours": neighbours(true, false),
			"theirs": neighbours(false, true), "merged": neighbours(true, true),
		},
	} {
		t.Run(name, func(t *testing.T) {
			mergeproperty.AssertPlainMergeIsRender(t, "deploy/kcl/config_gen.k", func(v string) []byte {
				out, err := GenerateConfigKCL(variants[v], "app")
				if err != nil {
					t.Fatal(err)
				}
				return []byte(out)
			})
		})
	}
}

// The grants are one constant script per role and one row file per table
// (pkg/dbgrants.Files), so every shape of independent table changes merges as
// files: tables apart, neighbours, a table born beside a changed one, TWO
// tables born between the same neighbours (orders and profiles after
// accounts: the shape no single file can merge, measured by
// TestE2EMergeProperty), and a table dropped beside one born.
func TestMergeProperty_ReadOnlyGrants(t *testing.T) {
	table := func(name string, cols ...string) dbgrants.Table {
		tb := dbgrants.Table{Schema: "public", Name: name}
		for _, c := range cols {
			tb.Columns = append(tb.Columns, dbgrants.Column{Name: c})
		}
		return tb
	}
	cases := map[string]map[string][]dbgrants.Table{
		"apart": {
			"base":   {table("accounts", "id"), table("orders", "id"), table("users", "id", "email")},
			"ours":   {table("accounts", "id", "balance"), table("orders", "id"), table("users", "id", "email")},
			"theirs": {table("accounts", "id"), table("orders", "id"), table("users", "id", "email", "name"), table("widgets", "id")},
			"merged": {table("accounts", "id", "balance"), table("orders", "id"), table("users", "id", "email", "name"), table("widgets", "id")},
		},
		"neighbours, and a table born beside a changed one": {
			"base":   {table("accounts", "id"), table("orders", "id")},
			"ours":   {table("accounts", "id"), table("orders", "id", "total")},
			"theirs": {table("accounts", "id", "name"), table("events", "id"), table("orders", "id")},
			"merged": {table("accounts", "id", "name"), table("events", "id"), table("orders", "id", "total")},
		},
		"two tables born between the same neighbours": {
			"base":   {table("accounts", "id", "name")},
			"ours":   {table("accounts", "id", "name"), table("profiles", "id", "bio")},
			"theirs": {table("accounts", "id", "name"), table("orders", "id", "sku")},
			"merged": {table("accounts", "id", "name"), table("orders", "id", "sku"), table("profiles", "id", "bio")},
		},
		"a table dropped beside one born": {
			"base":   {table("accounts", "id"), table("orders", "id")},
			"ours":   {table("orders", "id")},
			"theirs": {table("accounts", "id"), table("events", "id"), table("orders", "id")},
			"merged": {table("events", "id"), table("orders", "id")},
		},
	}
	for name, tables := range cases {
		t.Run(name, func(t *testing.T) {
			mergeproperty.AssertPlainMergeIsRenderTree(t, "db/grants", func(dir, v string) {
				// Through forge's own writer, as generate runs it: the stale
				// row of a dropped table is swept by GenerateReadOnlyGrants,
				// here by writing the whole set afresh.
				if err := os.RemoveAll(filepath.Join(dir, "db", "grants")); err != nil {
					t.Fatal(err)
				}
				files, err := GrantsFileSet(tables[v])
				if err != nil {
					t.Fatal(err)
				}
				for rel, b := range files {
					stamped, ok := checksums.Stamp(rel, b)
					if !ok {
						t.Fatalf("%s is not stampable", rel)
					}
					p := filepath.Join(dir, filepath.FromSlash(rel))
					if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, stamped, 0o644); err != nil {
						t.Fatal(err)
					}
				}
			})
		})
	}
}
