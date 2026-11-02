package cli

// `forge db query`'s port-forward transport: an env whose app database is a
// ManagedDatabase on a cluster forge created (lifecycle local or ephemeral: a
// developer's k3d, a preview env). forge reaches it the way a developer would,
// with their own kube context:
//
//	kube context     the database's declared cluster
//	service          <name>-ro when the Cluster runs standbys, else <name>-rw
//	                 (CloudNativePG's services; a read never loads the primary
//	                 when a replica exists)
//	tunnel           pkg/dbquery/kubedial: a pods/portforward stream through
//	                 the API server per connection, no local port, TLS verified
//	                 against the cluster CA. The control plane's QueryDatabase
//	                 uses the same package, so there is one transport.
//	role             dbgrants.RoleName (forge_ro), the read-only role forge's
//	                 render declares on every ManagedDatabase; its password is
//	                 in dbgrants.SecretName (<name>-forge-ro)
//	database         the ManagedDatabase's database (deploy.DatabaseIdentifiers)
//
// The tunnel lives exactly as long as the connection: the stream is the
// connection, closed when the command returns.
//
// The env's data_access policy applies as on every transport. A REAL
// OnCluster env (lifecycle unset) is refused: reaching a production
// database through someone's laptop kube context is the control plane's job
// (its connected-cluster grant), not a tunnel's.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/dbquery/kubedial"
)

// dbClusterConnTweak, when set, adjusts the connection config before it is
// used. A test seam: a test points the tunnel at a Postgres that has no TLS.
var dbClusterConnTweak func(*pgx.ConnConfig)

// newDBQueryClient opens the kube client for a kube context. A seam: tests
// substitute one over a fake clientset (k3d is not available everywhere this
// runs).
var newDBQueryClient = func(kubeContext string) (*kubedial.Client, error) {
	if kubeContext == "" {
		return nil, errors.New("the database declares no cluster to reach it on")
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kube context %q: %w", kubeContext, err)
	}
	return kubedial.New(cfg)
}

// resolveDBQueryCluster resolves the port-forward target for an OnCluster
// workload's env. The returned target's close stops the tunnel.
func resolveDBQueryCluster(ctx context.Context, o dbQueryOptions, e *KCLEntities, w WorkloadEntity) (dbQueryTarget, error) {
	if x, ok, err := selectExternalDatabase(e, o.database); err != nil {
		return dbQueryTarget{}, err
	} else if ok {
		return resolveDBQueryExternal(ctx, o, e, x, w.Name)
	}
	switch e.Lifecycle {
	case lifecycleLocal, lifecycleEphemeral:
	default:
		// With --addr the route is the user's, so a real env is reachable.
		if o.addr == "" {
			return dbQueryTarget{}, exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("workload %q in %s runs on a cluster %s does not declare scratch (lifecycle local or ephemeral): its database is not reachable from forge yet; bring your own route (VPN, kubectl port-forward) and pass --addr <host:port>", w.Name, o.env, o.env)}
		}
	}
	db, err := selectQueryDatabase(e, o.database)
	if err != nil {
		return dbQueryTarget{}, err
	}
	client, err := newDBQueryClient(db.Cluster)
	if err != nil {
		return dbQueryTarget{}, exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("database %q in %s: %v", db.Name, o.env, err)}
	}
	kdb := kubedial.Database{Namespace: db.Namespace, Name: db.Name, Instances: db.Spec.WithDefaults().Instances}
	creds, err := client.Credentials(ctx, kdb)
	var cfg *pgx.ConnConfig
	if err == nil {
		if o.addr != "" {
			cfg, err = kubedial.ConnConfigForAddr(kdb, creds, o.addr, o.sslmode)
		} else {
			cfg, err = client.ConnConfigFor(kdb, creds)
		}
	}
	if err != nil {
		hint := ""
		if errors.Is(err, kubedial.ErrNoReadOnlyCredentials) {
			hint = fmt.Sprintf(" (redeploy %s)", o.env)
		}
		return dbQueryTarget{}, exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("database %q in %s: read-only credentials %s/%s: %v%s", db.Name, o.env, db.Namespace, dbgrants.SecretName(db.Name), err, hint)}
	}
	if dbClusterConnTweak != nil {
		dbClusterConnTweak(cfg)
	}
	transport := "port-forward"
	if o.addr != "" {
		transport = dbQueryTransportAddr
	}
	return dbQueryTarget{
		connConfig: cfg,
		scope:      dbQueryScope{Env: o.env, Transport: transport, Workload: w.Name, Database: db.Name, Policy: dbQueryPolicy(e)},
	}, nil
}

// selectQueryDatabase picks the cluster ManagedDatabase to query: the one
// named, or the env's only one.
func selectQueryDatabase(e *KCLEntities, name string) (DatabaseEntity, error) {
	var all []DatabaseEntity
	for _, d := range e.Databases {
		if d.Runtime == RuntimeCluster {
			all = append(all, d)
		}
	}
	if name != "" {
		for _, d := range all {
			if d.Name == name {
				return d, nil
			}
		}
		return DatabaseEntity{}, exitCodeError{code: exitWrong, msg: fmt.Sprintf("no cluster database named %q (the env declares %s)", name, databaseNames(all, e.ExternalDatabases))}
	}
	switch len(all) {
	case 1:
		return all[0], nil
	case 0:
		return DatabaseEntity{}, exitCodeError{code: exitUndetermined, msg: "the env declares no ManagedDatabase on a cluster to query"}
	default:
		return DatabaseEntity{}, exitCodeError{code: exitWrong, msg: fmt.Sprintf("the env declares %d cluster databases (%s): pick one with --database", len(all), databaseNames(all, nil))}
	}
}

// databaseNames lists the databases --database can name.
func databaseNames(ds []DatabaseEntity, xs []ExternalDatabaseEntity) string {
	var names []string
	for _, d := range ds {
		names = append(names, d.Name)
	}
	for _, x := range xs {
		names = append(names, x.Name+" (external)")
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// selectExternalDatabase picks the forge.ExternalDatabase a query reads: the
// one --database names, or, when it names none, the env's only database of
// either kind when that one is external. ok is false when the query is not
// for an external database.
func selectExternalDatabase(e *KCLEntities, name string) (ExternalDatabaseEntity, bool, error) {
	if name != "" {
		for _, x := range e.ExternalDatabases {
			if x.Name == name {
				return x, true, nil
			}
		}
		return ExternalDatabaseEntity{}, false, nil
	}
	managed := 0
	for _, d := range e.Databases {
		if d.Runtime == RuntimeCluster {
			managed++
		}
	}
	switch {
	case len(e.ExternalDatabases) == 0:
		return ExternalDatabaseEntity{}, false, nil
	case len(e.ExternalDatabases) == 1 && managed == 0:
		return e.ExternalDatabases[0], true, nil
	}
	clusterDBs := make([]DatabaseEntity, 0, managed)
	for _, d := range e.Databases {
		if d.Runtime == RuntimeCluster {
			clusterDBs = append(clusterDBs, d)
		}
	}
	return ExternalDatabaseEntity{}, false, exitCodeError{code: exitWrong, msg: fmt.Sprintf("the env declares %d databases (%s): pick one with --database", managed+len(e.ExternalDatabases), databaseNames(clusterDBs, e.ExternalDatabases))}
}

// resolveDBQueryExternal is the port-forward transport for a
// forge.ExternalDatabase: a tunnel to a ready pod behind its Service, signed
// in as its declared role. Unlike a ManagedDatabase it is reachable on a real
// env: no control plane runs it, and declaring it is the env's opt-in. The
// env's data_access policy still decides who may ask (checked by the caller).
func resolveDBQueryExternal(ctx context.Context, o dbQueryOptions, e *KCLEntities, x ExternalDatabaseEntity, workload string) (dbQueryTarget, error) {
	if o.addr != "" {
		return dbQueryTarget{}, exitCodeError{code: exitWrong, msg: fmt.Sprintf("--addr does not apply to external database %q: forge reaches it through its service %s/%s; drop --addr", x.Name, x.Namespace, x.Service)}
	}
	if x.Cluster == "" || x.Namespace == "" {
		return dbQueryTarget{}, exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("external database %q in %s has no cluster or namespace: bind `runtime = forge.OnCluster {target = ...}` on it", x.Name, o.env)}
	}
	// Judge the policy before reading the password: a refused query never
	// touches the credential. runDBQuery judges it again, as for every
	// transport.
	policy := dbQueryPolicy(e)
	if err := checkDBQueryPolicy(o, policy); err != nil {
		return dbQueryTarget{}, err
	}
	client, err := newDBQueryClient(x.Cluster)
	if err != nil {
		return dbQueryTarget{}, exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("external database %q in %s: %v", x.Name, o.env, err)}
	}
	cfg, err := client.ExternalConnConfig(ctx, kubedial.External{
		Namespace: x.Namespace, Service: x.Service, Database: x.Database, Username: x.Username,
		PasswordSecret: x.PasswordSecret, PasswordKey: x.PasswordKey, SSLMode: x.SSLMode,
	})
	if err != nil {
		return dbQueryTarget{}, exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("external database %q in %s: %v", x.Name, o.env, err)}
	}
	if dbClusterConnTweak != nil {
		dbClusterConnTweak(cfg)
	}
	return dbQueryTarget{
		connConfig: cfg,
		scope:      dbQueryScope{Env: o.env, Transport: "port-forward", Workload: workload, Database: x.Name, Policy: policy},
	}, nil
}
