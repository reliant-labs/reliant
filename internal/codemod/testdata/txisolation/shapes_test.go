package shapes

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/lib/pq"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestShapes prints, for every shape, the level the server reports inside
// the transaction and how many times the shape ran a body that always fails
// 40001. The same file, run before and after the codemod, must print the
// same lines.
func TestShapes(t *testing.T) {
	sqlDB, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	c, err := orm.NewClientWithDB(sqlDB, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := PlainSQL(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for name, run := range Shapes(c) {
		attempts := 0
		var iso, ro, def string
		_ = run(ctx, func(ctx context.Context, q orm.Context) error {
			attempts++
			if attempts == 1 {
				for _, s := range []struct {
					name string
					into *string
				}{{"transaction_isolation", &iso}, {"transaction_read_only", &ro}, {"transaction_deferrable", &def}} {
					if err := q.QueryRow(ctx, "SHOW "+s.name).Scan(s.into); err != nil {
						t.Errorf("%s: SHOW %s: %v", name, s.name, err)
					}
				}
			}
			return &pq.Error{Code: "40001", Message: "could not serialize access"}
		})
		lines = append(lines, fmt.Sprintf("SHAPE %s isolation=%q read_only=%s deferrable=%s attempts=%d", name, iso, ro, def, attempts))
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Println(l)
	}
}
