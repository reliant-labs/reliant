package release

import "fmt"

// DBQueryPolicy is what an env's declaration says about reading its database
// through `forge db query` (forge.DataAccess.db_query in KCL).
//
// It is how the control plane learns the rule to enforce: the flag on the CLI
// is the client-side interface, and the enforcement reads this from the env's
// RECORDED shape, so a client cannot choose a weaker policy than the env
// declared.
type DBQueryPolicy string

const (
	// DBQueryOpen: anyone with the env may query, with no confirmation. The
	// default for a local or ephemeral env, whose data is scratch.
	DBQueryOpen DBQueryPolicy = "open"
	// DBQueryAudited: only a person holding the db:query grant, naming the env
	// and a reason, and every call is recorded. The default for every other
	// env: reading real data is allowed, and leaves a trail.
	DBQueryAudited DBQueryPolicy = "audited"
	// DBQueryDeny: nobody.
	DBQueryDeny DBQueryPolicy = "deny"
)

// DBQueryPolicies is the closed set, in a stable order.
var DBQueryPolicies = []DBQueryPolicy{DBQueryOpen, DBQueryAudited, DBQueryDeny}

// ShapeDataAccess is the env's declared access to its data (forge.DataAccess).
type ShapeDataAccess struct {
	DBQuery DBQueryPolicy `json:"db_query"`
}

// DefaultDataAccess is what an env with a real database declares nothing
// about gets. It must agree with the KCL default for an env with no lifecycle
// (TestDataAccessDefaultsAgreeWithKCL); a local or ephemeral env defaults to
// open in KCL and records that, which is how a reader can tell it apart.
func DefaultDataAccess() ShapeDataAccess { return ShapeDataAccess{DBQuery: DBQueryAudited} }

// IsDefault reports whether d is nil or the default: the shape then omits it,
// so every shape recorded before the field encodes and hashes as it did.
func (d *ShapeDataAccess) IsDefault() bool {
	return d == nil || *d == DefaultDataAccess()
}

// Resolved returns the data access in force: the default for nil.
func (d *ShapeDataAccess) Resolved() ShapeDataAccess {
	if d == nil {
		return DefaultDataAccess()
	}
	return *d
}

func (d ShapeDataAccess) validate() error {
	for _, p := range DBQueryPolicies {
		if d.DBQuery == p {
			return nil
		}
	}
	return fmt.Errorf("%w: shape data_access.db_query %q (expected %q, %q or %q)", ErrInvalid, d.DBQuery, DBQueryOpen, DBQueryAudited, DBQueryDeny)
}
