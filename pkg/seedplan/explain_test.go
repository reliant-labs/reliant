package seedplan

import (
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

func explainPlan(t *testing.T) *Plan {
	t.Helper()
	tables := []schemadef.Table{
		{
			Name: "accounts", PKCols: []string{"id"},
			Columns: []schemadef.Column{idCol(), textCol("email"), textCol("region"), textCol("nick")},
			Indexes: []schemadef.Index{
				{Name: "accounts_email_key", Columns: []string{"email"}, Unique: true},
				{Name: "accounts_region_nick_key", Columns: []string{"region", "nick"}, Unique: true},
				{Name: "accounts_lower_nick", Columns: []string{"nick"}, Unique: true, Expression: true,
					Keys: []schemadef.IndexKey{{Expr: "lower(nick)", Column: "nick", Fold: "lower"}}},
				{Name: "accounts_plain_idx", Columns: []string{"region"}},
			},
		},
		{
			Name: "orders", PKCols: []string{"id"},
			Columns:     []schemadef.Column{idCol(), {Name: "account_id", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true}, textCol("code")},
			ForeignKeys: []schemadef.ForeignKey{{Name: "orders_account_id_fkey", Column: "account_id", RefTable: "accounts", RefColumn: "id"}},
			Checks:      []schemadef.CheckConstraint{check("orders_code_check", `CHECK ((code <> ''))`, "code")},
		},
		{
			Name: "lines", PKCols: []string{"id"},
			Columns: []schemadef.Column{idCol(), textCol("region"), textCol("nick")},
			ForeignKeys: []schemadef.ForeignKey{{
				Name: "lines_account_fkey", Column: "region", RefTable: "accounts", RefColumn: "region",
				KeyColumns: []string{"region", "nick"}, RefKeyColumns: []string{"region", "nick"},
			}},
		},
	}
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 3, Salt: 1})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func tablePlanOf(t *testing.T, p *Plan, name string) tablePlan {
	t.Helper()
	for _, tp := range p.tables {
		if tp.table.Name == name {
			return tp
		}
	}
	t.Fatalf("no table %q in the plan", name)
	return tablePlan{}
}

func TestExplainInsertFailure_NamesTheConstraintItsColumnsAndThePlannedValues(t *testing.T) {
	p := explainPlan(t)
	for _, tc := range []struct {
		name  string
		table string
		msg   string
		want  []string
	}{
		{"a check", "orders", `ERROR: new row for relation "orders" violates check constraint "orders_code_check" (SQLSTATE 23514)`,
			[]string{"seed orders:", "constraint: orders_code_check (check)", "definition: CHECK ((code <> ''))", "columns:    code", "row 0: code = ", "row 2:"}},
		{"a single-column foreign key", "orders", `insert or update on table "orders" violates foreign key constraint "orders_account_id_fkey"`,
			[]string{"constraint: orders_account_id_fkey (foreign key)", "definition: FOREIGN KEY (account_id) REFERENCES accounts (id)", "columns:    account_id"}},
		{"a composite foreign key prints the whole key", "lines", `insert or update on table "lines" violates foreign key constraint "lines_account_fkey"`,
			[]string{"FOREIGN KEY (region, nick) REFERENCES accounts (region, nick)", "columns:    nick, region"}},
		{"a unique index", "accounts", `duplicate key value violates unique constraint "accounts_email_key"`,
			[]string{"constraint: accounts_email_key (unique)", "definition: UNIQUE (email)", "columns:    email"}},
		{"a composite unique index", "accounts", `duplicate key value violates unique constraint "accounts_region_nick_key"`,
			[]string{"definition: UNIQUE (region, nick)", "columns:    nick, region"}},
		{"an expression unique index is not rendered as its bare columns", "accounts", `duplicate key value violates unique constraint "accounts_lower_nick"`,
			[]string{"definition: UNIQUE (expression)"}},
		{"a not-null violation names the column from the message", "orders", `null value in column "code" of relation "orders" violates not-null constraint`,
			[]string{"constraint: not-null", "columns:    code", "row 0: code = "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := p.explainInsertFailure(tablePlanOf(t, p, tc.table), errors.New(tc.msg))
			if err == nil {
				t.Fatal("want an error")
			}
			for _, w := range append(tc.want, "forge generated both this schema and this data, and they disagree", tc.msg) {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("explanation lacks %q:\n%s", w, err)
				}
			}
		})
	}
}

func TestExplainInsertFailure_DegradesToThePlainErrorWhenItCannotDecode(t *testing.T) {
	p := explainPlan(t)
	err := p.explainInsertFailure(tablePlanOf(t, p, "orders"), errors.New("connection reset by peer"))
	if err == nil || err.Error() != "seed orders: connection reset by peer" {
		t.Errorf("err = %v", err)
	}

	// A constraint nobody introspected is named but not explained further.
	err = p.explainInsertFailure(tablePlanOf(t, p, "orders"), errors.New(`violates check constraint "mystery"`))
	if err == nil || !strings.Contains(err.Error(), "constraint: mystery (check)") || strings.Contains(err.Error(), "columns:") {
		t.Errorf("an unknown constraint: %v", err)
	}

	// An unnamed violation without a column to read has nothing to list.
	err = p.explainInsertFailure(tablePlanOf(t, p, "orders"), errors.New("violates not-null constraint"))
	if err == nil || !strings.Contains(err.Error(), "constraint: not-null") || strings.Contains(err.Error(), "columns:") {
		t.Errorf("an unnamed not-null: %v", err)
	}
}

func TestConstraintDefinitionAndColumns_AnswerOnlyForNamedConstraintsOfPlannedTables(t *testing.T) {
	p := explainPlan(t)
	if p.constraintDef("ghost", "x") != "" || p.constraintDef("orders", "") != "" || p.constraintDef("orders", "nope") != "" {
		t.Error("an unknown table, an empty name or an unknown constraint has no definition")
	}
	if p.constraintDef("accounts", "accounts_plain_idx") != "" {
		t.Error("a non-unique index is not a constraint")
	}
	if p.constraintColumns("ghost", "x") != nil || p.constraintColumns("orders", "") != nil || p.constraintColumns("orders", "nope") != nil {
		t.Error("an unknown table, an empty name or an unknown constraint spans no columns")
	}
	if got := p.constraintColumns("accounts", "accounts_plain_idx"); len(got) != 1 || got[0] != "region" {
		t.Errorf("any index names its columns: %v", got)
	}
	cols := p.constraintColumns("accounts", "accounts_region_nick_key")
	cols[0] = "mutated"
	if again := p.constraintColumns("accounts", "accounts_region_nick_key"); again[0] != "region" {
		t.Error("the returned columns are a copy")
	}
}

func TestPlannedCell_ExplainsWhatTheSeederDidNotWrite(t *testing.T) {
	p := explainPlan(t)
	tp := tablePlanOf(t, p, "orders")
	if got := p.plannedCell(tp, "code", 0); !strings.HasPrefix(got, `"`) {
		t.Errorf("a seeded scalar is quoted: %s", got)
	}
	if got := p.plannedCell(tp, "no_such_column", 0); got != "(not planned)" {
		t.Errorf("a column the plan does not carry: %s", got)
	}
	for i := range tp.cols {
		if tp.cols[i].col.Name == "code" {
			tp.cols[i].omit = true
		}
	}
	if got := p.plannedCell(tp, "code", 0); got != "(not written: the database default)" {
		t.Errorf("an omitted column: %s", got)
	}
}
