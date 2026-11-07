// Fires forgeconv-deps-are-interfaces. The package carries NO role
// marker of any kind — it is an ordinary internal package, which is the
// point: the rule applies wherever a `type Deps struct` exists, so a
// concrete dep cannot hide behind the absence of a label.
//
// checkout's Deps carries two concrete shapes: a same-package struct
// pointer and a cross-package one (`*db.PostgresRepository`), which is
// the shape that showed up nine times in one real project while the
// rule was still opt-in and firing on nothing.
package checkout

import "context"

type Service interface {
	Run(ctx context.Context) error
}
