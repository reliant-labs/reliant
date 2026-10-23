package kubedial

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/deploy"
)

// ErrNoReadOnlyCredentials means the read-only role's Secret is missing or
// holds no password: the database predates the forge_ro role, or CNPG has not
// reconciled it yet.
var ErrNoReadOnlyCredentials = errors.New("no read-only credentials")

// Credentials are what a connection as the read-only role needs, read through
// the same API server the tunnel runs over.
type Credentials struct {
	// Username is dbgrants.RoleName unless the Secret says otherwise.
	Username string
	Password string
	// CAPEM is the cluster CA certificate (ca.crt of <db>-ca).
	CAPEM []byte
}

// Credentials reads the read-only role's basic-auth Secret
// (dbgrants.SecretName) and the cluster CA (CASecretName, key ca.crt) of db.
// Only those two Secrets are read, and only those two keys are kept.
func (c *Client) Credentials(ctx context.Context, db Database) (*Credentials, error) {
	secretName := dbgrants.SecretName(db.Name)
	sec, err := c.cs.CoreV1().Secrets(db.Namespace).Get(ctx, secretName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, fmt.Errorf("kubedial: secret %s/%s: %w (does the database predate forge's read-only role? redeploy)", db.Namespace, secretName, ErrNoReadOnlyCredentials)
	case err != nil:
		return nil, fmt.Errorf("kubedial: read secret %s/%s: %w%s", db.Namespace, secretName, err, forbiddenHint(err, db.Namespace))
	}
	creds := &Credentials{Username: string(sec.Data["username"]), Password: string(sec.Data["password"])}
	if creds.Username == "" {
		creds.Username = dbgrants.RoleName
	}
	if creds.Password == "" {
		return nil, fmt.Errorf("kubedial: secret %s/%s has no password: %w", db.Namespace, secretName, ErrNoReadOnlyCredentials)
	}

	caName := CASecretName(db.Name)
	ca, err := c.cs.CoreV1().Secrets(db.Namespace).Get(ctx, caName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, fmt.Errorf("kubedial: secret %s/%s not found: CNPG has not issued the cluster CA yet, or the Cluster uses a CA Secret of its own", db.Namespace, caName)
	case err != nil:
		return nil, fmt.Errorf("kubedial: read secret %s/%s: %w%s", db.Namespace, caName, err, forbiddenHint(err, db.Namespace))
	}
	creds.CAPEM = ca.Data["ca.crt"]
	if len(creds.CAPEM) == 0 {
		return nil, fmt.Errorf("kubedial: secret %s/%s has no ca.crt", db.Namespace, caName)
	}
	return creds, nil
}

// ConnConfig is everything needed to run a query against db: it reads the
// credentials and returns a pgx config that dials through the tunnel and
// verifies the server certificate against the cluster CA
// (sslmode=verify-full semantics, ServerName <service>.<namespace>.svc).
//
// Pass it to pgx.ConnectConfig, or to a pgxpool.Config's ConnConfig: every
// connection takes its own stream and re-resolves the pod. The config holds
// the password; do not log it.
func (c *Client) ConnConfig(ctx context.Context, db Database) (*pgx.ConnConfig, error) {
	creds, err := c.Credentials(ctx, db)
	if err != nil {
		return nil, err
	}
	return c.ConnConfigFor(db, creds)
}

// ConnConfigFor is [Client.ConnConfig] with credentials the caller already
// holds, for a pool that caches them.
func (c *Client) ConnConfigFor(db Database, creds *Credentials) (*pgx.ConnConfig, error) {
	t := db.Target()
	database, _ := deploy.DatabaseIdentifiers(db.Name)
	dsn := (&url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(creds.Username, creds.Password),
		Host:     net.JoinHostPort(t.Host(), strconv.Itoa(PostgresPort)),
		Path:     "/" + database,
		RawQuery: "sslmode=disable",
	}).String()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// The error can quote the DSN, and the DSN carries the password.
		return nil, errors.New("kubedial: build connection config: invalid connection parameters")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(creds.CAPEM) {
		return nil, fmt.Errorf("kubedial: the cluster CA of %s/%s is not a PEM certificate", db.Namespace, db.Name)
	}
	cfg.TLSConfig = &tls.Config{RootCAs: pool, ServerName: t.Host(), MinVersion: tls.VersionTLS12}
	// The host is a cluster-internal name with no DNS here; the dial ignores
	// it, and pgx must not try to resolve it first.
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	cfg.DialFunc = c.DialFunc(t)
	return cfg, nil
}
