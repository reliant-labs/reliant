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

// StoragePolicy is what an env's declaration says about reading its object
// storage from outside the application (forge.DataAccess.storage in KCL): the
// control plane's storage browser lists, previews and downloads under it. The
// same three values as [DBQueryPolicy], with the same meanings, under the
// storage:read grant instead of db:query.
//
// UNDECLARED, IT FOLLOWS db_query ([ShapeDataAccess.Resolved]), so an env that
// denied SQL is not silently opened to file browsing by a field it never
// heard of.
type StoragePolicy string

const (
	// StorageOpen: anyone with the env may browse its buckets.
	StorageOpen StoragePolicy = "open"
	// StorageAudited: only a person holding the storage:read grant, naming
	// the env and a reason, and every call is recorded.
	StorageAudited StoragePolicy = "audited"
	// StorageDeny: nobody.
	StorageDeny StoragePolicy = "deny"
)

// StoragePolicies is the closed set, in a stable order.
var StoragePolicies = []StoragePolicy{StorageOpen, StorageAudited, StorageDeny}

// ShapeDataAccess is the env's declared access to its data (forge.DataAccess).
type ShapeDataAccess struct {
	DBQuery DBQueryPolicy `json:"db_query"`
	// Storage is the storage-browser policy when the env declares one that
	// differs from DBQuery; empty means "the same as DBQuery". Read it
	// through [ShapeDataAccess.Resolved], never directly. Omitted when empty,
	// so every shape recorded before the field encodes and hashes as it did;
	// a control plane that predates it refuses the whole shape, so it is a
	// bundle-document feature ([BundleFeatureStorage]).
	Storage StoragePolicy `json:"storage,omitempty"`
}

// DefaultDataAccess is what an env with a real database declares nothing
// about gets, resolved: audited for both. It must agree with the KCL default
// for an env with no lifecycle (TestDataAccessDefaultsAgreeWithKCL); a local
// or ephemeral env defaults to open in KCL and records that, which is how a
// reader can tell it apart.
func DefaultDataAccess() ShapeDataAccess {
	return ShapeDataAccess{DBQuery: DBQueryAudited, Storage: StorageAudited}
}

// IsDefault reports whether d is nil or resolves to the default: the shape
// then omits it, so every shape recorded before the field encodes and hashes
// as it did.
func (d *ShapeDataAccess) IsDefault() bool {
	return d == nil || d.Resolved() == DefaultDataAccess()
}

// Resolved returns the data access in force: the default for nil, and an
// undeclared Storage filled in from DBQuery.
func (d *ShapeDataAccess) Resolved() ShapeDataAccess {
	if d == nil {
		return DefaultDataAccess()
	}
	out := *d
	if out.Storage == "" {
		out.Storage = StoragePolicy(out.DBQuery)
	}
	return out
}

// canonical spells d the one way that hashes: Storage omitted when it says
// what DBQuery already does, so declaring the fallback explicitly records the
// same bytes as leaving it out.
func (d ShapeDataAccess) canonical() ShapeDataAccess {
	if d.Storage == StoragePolicy(d.DBQuery) {
		d.Storage = ""
	}
	return d
}

func (d ShapeDataAccess) validate() error {
	known := false
	for _, p := range DBQueryPolicies {
		if d.DBQuery == p {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("%w: shape data_access.db_query %q (expected %q, %q or %q)", ErrInvalid, d.DBQuery, DBQueryOpen, DBQueryAudited, DBQueryDeny)
	}
	if d.Storage == "" {
		return nil
	}
	for _, p := range StoragePolicies {
		if d.Storage == p {
			return nil
		}
	}
	return fmt.Errorf("%w: shape data_access.storage %q (expected %q, %q, %q or empty for db_query's)", ErrInvalid, d.Storage, StorageOpen, StorageAudited, StorageDeny)
}
