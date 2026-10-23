package migrationlint

import "testing"

func TestAddColumnNotNullDistinguishesCheckExpressions(t *testing.T) {
	cases := []struct {
		name, sql string
		unsafe    bool
	}{
		{"table check", `ALTER TABLE app.bundles ADD COLUMN snapshot jsonb, ADD COLUMN digest text, ADD CONSTRAINT valid CHECK ((snapshot IS NULL AND digest IS NULL) OR (snapshot IS NOT NULL AND digest IS NOT NULL));`, false},
		{"column check multiline", "ALTER TABLE bundles\n ADD COLUMN snapshot jsonb\n CHECK (snapshot IS NOT\n NULL);", false},
		{"quoted check tokens", `ALTER TABLE "NOT NULL" ADD COLUMN "NOT NULL" text CHECK ("NOT NULL" IS NOT NULL AND "NOT NULL" <> 'NOT NULL');`, false},
		{"check nested commas", `ALTER TABLE bundles ADD COLUMN snapshot jsonb CHECK (coalesce(snapshot, '{}') IS NOT NULL), ADD CONSTRAINT valid CHECK (snapshot IS NOT NULL);`, false},
		{"check string", `ALTER TABLE bundles ADD COLUMN note text CHECK (note <> 'NOT NULL, DEFAULT x');`, false},
		{"dollar string", `ALTER TABLE bundles ADD COLUMN note text CHECK (note <> $text$NOT NULL, DEFAULT x$text$);`, false},
		{"actual multiline", "ALTER TABLE bundles\n ADD COLUMN digest text\n NOT\n NULL\n CHECK (digest IS NOT NULL);", true},
		{"actual quoted column", `ALTER TABLE "bundles" ADD COLUMN "digest" text NOT NULL CHECK ("digest" IS NOT NULL);`, true},
		{"if not exists", `ALTER TABLE bundles ADD COLUMN IF NOT EXISTS "digest" text NOT NULL;`, true},
		{"default other action", `ALTER TABLE bundles ADD COLUMN safe text DEFAULT 'x', ADD COLUMN unsafe text NOT NULL;`, true},
		{"default in check string", `ALTER TABLE bundles ADD COLUMN unsafe text NOT NULL CHECK (unsafe <> 'DEFAULT value');`, true},
		{"default quoted identifier", `ALTER TABLE bundles ADD COLUMN "DEFAULT" text NOT NULL;`, true},
		{"actual safe default", `ALTER TABLE bundles ADD COLUMN safe text NOT NULL DEFAULT 'NOT NULL';`, false},
		{"generated safe", `ALTER TABLE bundles ADD COLUMN value text GENERATED ALWAYS AS (coalesce(a, b)) STORED NOT NULL;`, false},
		{"generated other action", `ALTER TABLE bundles ADD COLUMN value text GENERATED ALWAYS AS (a) STORED, ADD COLUMN unsafe text NOT NULL;`, true},
		{"identity not exempt", `ALTER TABLE bundles ADD COLUMN id bigint GENERATED ALWAYS AS IDENTITY NOT NULL;`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := lintMigrationFile("0001_add.up.sql", tc.sql, DefaultConfig())
			found := false
			for _, f := range findings {
				if f.Rule == "unsafe-add-not-null-column" {
					found = true
				}
			}
			if found != tc.unsafe {
				t.Fatalf("unsafe-add-not-null-column = %v want %v: %+v", found, tc.unsafe, findings)
			}
		})
	}
}
