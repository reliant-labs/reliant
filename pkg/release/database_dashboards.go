package release

import (
	"fmt"
	"slices"
	"sort"
)

// ShapeDatabase is what the env's shape records about one managed database,
// beyond the workload entry every database already has. It exists only for a
// database that DECLARES something the control plane enforces; a database that
// declares nothing has no entry, so a shape recorded before the field encodes
// and hashes exactly as it did.
type ShapeDatabase struct {
	Name string `json:"name"`
	// Dashboards is the database's live-dashboard declaration
	// (ManagedDatabase.dashboards in KCL). Nil means disabled.
	Dashboards *ShapeDatabaseDashboards `json:"dashboards,omitempty"`
}

// ShapeDatabaseDashboards is the limits a dashboard panel query runs under on
// one database, and whether panels may query it at all.
//
// THE CONTROL PLANE ENFORCES FROM THIS RECORDED SHAPE, not from a client's
// request, so a client cannot choose a looser cap or a longer timeout than the
// env declared. FAIL CLOSED: a database with no entry, an entry with no
// dashboards, and a dashboards record whose enabled is false (or absent from
// the JSON) all read as disabled ([Shape.DashboardsFor]).
type ShapeDatabaseDashboards struct {
	Enabled        bool `json:"enabled"`
	RowCap         int  `json:"row_cap,omitempty"`
	TimeoutMs      int  `json:"timeout_ms,omitempty"`
	MaxConcurrency int  `json:"max_concurrency,omitempty"`
}

// Bounds of the limits. KCL (kcl/schema.k DatabaseDashboards) and the
// ManagedDatabase spec (pkg/deploy/v1alpha1) hold the same numbers;
// TestDatabaseDashboardsDefaultsAgreeWithKCL keeps them from drifting.
const (
	DefaultDashboardRowCap         = 1000
	MaxDashboardRowCap             = 10000
	DefaultDashboardTimeoutMs      = 5000
	MinDashboardTimeoutMs          = 100
	MaxDashboardTimeoutMs          = 10000
	DefaultDashboardMaxConcurrency = 2
	MaxDashboardMaxConcurrency     = 4
)

// DefaultDatabaseDashboards is what `forge.DatabaseDashboards {enabled = True}`
// declares: the limits a database that opted in and said nothing else gets.
// DISABLED is not this value: a disabled database records nothing.
func DefaultDatabaseDashboards() ShapeDatabaseDashboards {
	return ShapeDatabaseDashboards{
		Enabled:        true,
		RowCap:         DefaultDashboardRowCap,
		TimeoutMs:      DefaultDashboardTimeoutMs,
		MaxConcurrency: DefaultDashboardMaxConcurrency,
	}
}

// Resolved returns d with every unset limit filled. A nil or disabled d
// resolves to the zero value: disabled, no limits.
func (d *ShapeDatabaseDashboards) Resolved() ShapeDatabaseDashboards {
	if d == nil || !d.Enabled {
		return ShapeDatabaseDashboards{}
	}
	r := *d
	def := DefaultDatabaseDashboards()
	if r.RowCap == 0 {
		r.RowCap = def.RowCap
	}
	if r.TimeoutMs == 0 {
		r.TimeoutMs = def.TimeoutMs
	}
	if r.MaxConcurrency == 0 {
		r.MaxConcurrency = def.MaxConcurrency
	}
	return r
}

// DashboardsFor is the dashboards declaration in force for the named database:
// the zero value (disabled) when the shape names no such database or it
// declares none. This is the one question a consumer asks of the field, and
// the one that cannot be answered open by accident.
func (s Shape) DashboardsFor(database string) ShapeDatabaseDashboards {
	for _, d := range s.Databases {
		if d.Name == database {
			return d.Dashboards.Resolved()
		}
	}
	return ShapeDatabaseDashboards{}
}

func (d ShapeDatabaseDashboards) validate(database string) error {
	r := d.Resolved()
	if !d.Enabled {
		return nil
	}
	switch {
	case r.RowCap < 1 || r.RowCap > MaxDashboardRowCap:
		return fmt.Errorf("%w: shape database %q dashboards.row_cap %d (expected 1-%d)", ErrInvalid, database, d.RowCap, MaxDashboardRowCap)
	case r.TimeoutMs < MinDashboardTimeoutMs || r.TimeoutMs > MaxDashboardTimeoutMs:
		return fmt.Errorf("%w: shape database %q dashboards.timeout_ms %d (expected %d-%d)", ErrInvalid, database, d.TimeoutMs, MinDashboardTimeoutMs, MaxDashboardTimeoutMs)
	case r.MaxConcurrency < 1 || r.MaxConcurrency > MaxDashboardMaxConcurrency:
		return fmt.Errorf("%w: shape database %q dashboards.max_concurrency %d (expected 1-%d)", ErrInvalid, database, d.MaxConcurrency, MaxDashboardMaxConcurrency)
	}
	return nil
}

func validateShapeDatabases(dbs []ShapeDatabase) error {
	seen := map[string]bool{}
	for _, d := range dbs {
		if d.Name == "" {
			return fmt.Errorf("%w: shape database needs a name", ErrInvalid)
		}
		if seen[d.Name] {
			return fmt.Errorf("%w: shape names database %q twice", ErrInvalid, d.Name)
		}
		seen[d.Name] = true
		if d.Dashboards != nil {
			if err := d.Dashboards.validate(d.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

// canonicalShapeDatabases drops what is the default (a database whose
// dashboards are disabled records nothing) and spells every enabled limit out,
// so a producer that writes the defaults and one that leaves them off hash the
// same. Sorted by name.
func canonicalShapeDatabases(in []ShapeDatabase) []ShapeDatabase {
	var out []ShapeDatabase
	for _, d := range in {
		if d.Dashboards == nil || !d.Dashboards.Enabled {
			continue
		}
		r := d.Dashboards.Resolved()
		out = append(out, ShapeDatabase{Name: d.Name, Dashboards: &r})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return slices.Clip(out)
}
