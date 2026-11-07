package release

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
)

// ShapeExternalDatabase is one database the env's workloads use but forge does
// not run (forge.ExternalDatabase): Cloud SQL, RDS, a Postgres behind a pooler.
// forge renders nothing for it, so without this entry a reader of the shape
// cannot know it exists. It is a ROUTE: where the Service in front of the
// database is, which database to open, who to sign in as, and which Secret
// holds that role's password.
//
// IT NEVER CARRIES A VALUE. PasswordSecret and PasswordKey are the NAME of a
// Secret in Namespace and the key within it; whoever dials reads the password
// from the cluster, under its own credential. [Shape.Validate] refuses a
// Secret name or key that Kubernetes could not hold, so a password pasted in
// their place is refused rather than recorded.
type ShapeExternalDatabase struct {
	// Name is the database's name in the env, unique across the env's managed
	// and external databases (`forge db query --database` names it).
	Name string `json:"name"`
	// Cluster is the kube context the Service runs on (the database's
	// `runtime`, or the env's primary cluster target), the same key the
	// shape's workloads and objects use. Empty when the env declares no
	// cluster for it.
	Cluster string `json:"cluster,omitempty"`
	// Namespace is the namespace of the Service and of PasswordSecret. Empty
	// when the env declares none.
	Namespace string `json:"namespace,omitempty"`
	// Service is the in-cluster Service in front of the database.
	Service string `json:"service"`
	// Database is the database (a pooler's pool) to open.
	Database string `json:"database"`
	// Username is the role to sign in as; the declaration asks for a
	// read-only one.
	Username string `json:"username"`
	// PasswordSecret is the NAME of the Secret in Namespace that holds the
	// role's password.
	PasswordSecret string `json:"password_secret"`
	// PasswordKey is the key of PasswordSecret holding the password
	// ([DefaultExternalDatabasePasswordKey] when empty).
	PasswordKey string `json:"password_key,omitempty"`
	// SSLMode is the libpq sslmode: [ExternalDatabaseSSLRequire] (the default
	// when empty) or [ExternalDatabaseSSLDisable].
	SSLMode string `json:"sslmode,omitempty"`
	// Reach is how a control plane dials the database:
	// [ExternalDatabaseReachPortForward] (the default when empty) through the
	// kube API under the env's cluster credential, or
	// [ExternalDatabaseReachDirect], `<service>.<namespace>.svc:<port>` with
	// no kube credential, valid only for a control plane running in that
	// cluster.
	Reach string `json:"reach,omitempty"`
	// Port is the Service port, 0 when undeclared: a port-forward then picks
	// the Service's Postgres port (5432) or its only port, and a direct dial
	// uses [DefaultExternalDatabasePort].
	Port int `json:"port,omitempty"`
}

// Defaults of [ShapeExternalDatabase]. KCL (kcl/schema.k ExternalDatabase)
// holds the same values; [Shape.Canonical] spells them out, so a producer that
// writes them and one that leaves them off hash the same.
const (
	DefaultExternalDatabasePasswordKey = "password"
	ExternalDatabaseSSLRequire         = "require"
	ExternalDatabaseSSLDisable         = "disable"
	// ExternalDatabaseReachPortForward: the control plane tunnels to a ready
	// pod behind the Service through the kube API (pods/portforward), under
	// the env's cluster credential. The default.
	ExternalDatabaseReachPortForward = "port_forward"
	// ExternalDatabaseReachDirect: the control plane runs in the database's
	// cluster and dials <service>.<namespace>.svc:<port> itself.
	ExternalDatabaseReachDirect = "direct"
	// DefaultExternalDatabasePort is the port a direct dial uses when the
	// declaration names none.
	DefaultExternalDatabasePort = 5432
)

// DatabaseKind says how a database the shape names is run.
type DatabaseKind string

const (
	// DatabaseManaged is a forge.ManagedDatabase: forge renders it, and the
	// shape records its ManagedDatabase object (and, when it enables live
	// dashboards, a [ShapeDatabase]).
	DatabaseManaged DatabaseKind = "managed"
	// DatabaseExternal is a forge.ExternalDatabase: a route the shape
	// records in [Shape.ExternalDatabases], with nothing rendered for it.
	DatabaseExternal DatabaseKind = "external"
)

// DatabaseKindOf says whether the shape's database name is managed or external,
// or "" when the shape names no such database. A managed database is one with
// a ManagedDatabase object or a [ShapeDatabase] entry; an external one is one
// in [Shape.ExternalDatabases]. Validate refuses a name that is both.
func (s Shape) DatabaseKindOf(name string) DatabaseKind {
	if name == "" {
		return ""
	}
	if _, ok := s.ExternalDatabase(name); ok {
		return DatabaseExternal
	}
	if s.declaresManagedDatabase(name) {
		return DatabaseManaged
	}
	return ""
}

// ExternalDatabase is the external database the shape names, with its
// defaults filled in; ok is false when it names none.
func (s Shape) ExternalDatabase(name string) (ShapeExternalDatabase, bool) {
	for _, x := range s.ExternalDatabases {
		if x.Name == name {
			return x.resolved(), true
		}
	}
	return ShapeExternalDatabase{}, false
}

func (s Shape) declaresManagedDatabase(name string) bool {
	for _, o := range s.Objects {
		if o.Kind == "ManagedDatabase" && o.Name == name {
			return true
		}
	}
	for _, d := range s.Databases {
		if d.Name == name {
			return true
		}
	}
	return false
}

// resolved returns x with every unset default filled.
func (x ShapeExternalDatabase) resolved() ShapeExternalDatabase {
	if x.PasswordKey == "" {
		x.PasswordKey = DefaultExternalDatabasePasswordKey
	}
	if x.SSLMode == "" {
		x.SSLMode = ExternalDatabaseSSLRequire
	}
	if x.Reach == "" {
		x.Reach = ExternalDatabaseReachPortForward
	}
	return x
}

// DirectAddress is the host:port a [ExternalDatabaseReachDirect] dial opens,
// `<service>.<namespace>.svc:<port>`; "" when x is not reached directly or
// names no namespace.
func (x ShapeExternalDatabase) DirectAddress() string {
	if x.resolved().Reach != ExternalDatabaseReachDirect || x.Namespace == "" {
		return ""
	}
	port := x.Port
	if port == 0 {
		port = DefaultExternalDatabasePort
	}
	return fmt.Sprintf("%s.%s.svc:%d", x.Service, x.Namespace, port)
}

var (
	// externalDatabaseLabelRE is an RFC-1123 label: the database's own name
	// (as KCL checks it), a Service and a namespace.
	externalDatabaseLabelRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	// externalDatabaseSecretRE is an RFC-1123 subdomain: a Secret's name.
	externalDatabaseSecretRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
	// externalDatabaseKeyRE is a Secret data key.
	externalDatabaseKeyRE = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)
)

func (x ShapeExternalDatabase) validate() error {
	label := func(field, v string) error {
		if len(v) > 63 || !externalDatabaseLabelRE.MatchString(v) {
			return fmt.Errorf("%w: shape external database %q: %s %q is not an RFC-1123 label of at most 63 characters", ErrInvalid, x.Name, field, v)
		}
		return nil
	}
	if err := label("name", x.Name); err != nil {
		return err
	}
	if err := label("service", x.Service); err != nil {
		return err
	}
	if x.Namespace != "" {
		if err := label("namespace", x.Namespace); err != nil {
			return err
		}
	}
	if x.Database == "" || x.Username == "" {
		return fmt.Errorf("%w: shape external database %q needs a database and a username", ErrInvalid, x.Name)
	}
	// A Secret's name and key are names Kubernetes can hold, never a value:
	// this is what refuses a password pasted where its Secret's name belongs.
	if len(x.PasswordSecret) > 253 || !externalDatabaseSecretRE.MatchString(x.PasswordSecret) {
		return fmt.Errorf("%w: shape external database %q: password_secret must name a Secret (an RFC-1123 subdomain), never hold a value", ErrInvalid, x.Name)
	}
	if x.PasswordKey != "" && (len(x.PasswordKey) > 253 || !externalDatabaseKeyRE.MatchString(x.PasswordKey)) {
		return fmt.Errorf("%w: shape external database %q: password_key must name a key of its Secret, never hold a value", ErrInvalid, x.Name)
	}
	switch x.SSLMode {
	case "", ExternalDatabaseSSLRequire, ExternalDatabaseSSLDisable:
	default:
		return fmt.Errorf("%w: shape external database %q: sslmode %q (expected %q or %q)", ErrInvalid, x.Name, x.SSLMode, ExternalDatabaseSSLRequire, ExternalDatabaseSSLDisable)
	}
	switch x.Reach {
	case "", ExternalDatabaseReachPortForward:
	case ExternalDatabaseReachDirect:
		if x.Namespace == "" {
			return fmt.Errorf("%w: shape external database %q: reach %q dials <service>.<namespace>.svc and needs a namespace", ErrInvalid, x.Name, x.Reach)
		}
	default:
		return fmt.Errorf("%w: shape external database %q: reach %q (expected %q or %q)", ErrInvalid, x.Name, x.Reach, ExternalDatabaseReachPortForward, ExternalDatabaseReachDirect)
	}
	if x.Port < 0 || x.Port > 65535 {
		return fmt.Errorf("%w: shape external database %q: port %d (expected 1-65535, or 0 for undeclared)", ErrInvalid, x.Name, x.Port)
	}
	return nil
}

func (s Shape) validateExternalDatabases() error {
	seen := map[string]bool{}
	for _, x := range s.ExternalDatabases {
		if err := x.validate(); err != nil {
			return err
		}
		if seen[x.Name] {
			return fmt.Errorf("%w: shape names external database %q twice", ErrInvalid, x.Name)
		}
		seen[x.Name] = true
		if s.declaresManagedDatabase(x.Name) {
			return fmt.Errorf("%w: shape names database %q as both managed and external", ErrInvalid, x.Name)
		}
	}
	return nil
}

// canonicalExternalDatabases spells every default out and sorts by name, so a
// producer that writes the defaults and one that leaves them off hash the
// same. Nil when there are none, so a shape that declares none encodes, and
// hashes, exactly as it did before the field.
func canonicalExternalDatabases(in []ShapeExternalDatabase) []ShapeExternalDatabase {
	if len(in) == 0 {
		return nil
	}
	out := make([]ShapeExternalDatabase, len(in))
	for i, x := range in {
		out[i] = x.resolved()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return slices.Clip(out)
}
