package kubedial

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// External is a Postgres forge does not run (a managed cloud database, one
// behind a pooler) that a Service in a namespace fronts: the declaration
// forge.ExternalDatabase. The tunnel is the same as a ManagedDatabase's; what
// differs is everything forge cannot know about a database it did not
// create, so the caller states it: the database to open, the role to sign in
// as and the Secret holding its password.
type External struct {
	Namespace string
	// Service fronts the database; the tunnel reaches its Postgres port
	// (PostgresPort) or, when it has one port, that one (a pooler's 6432).
	Service  string
	Database string
	Username string
	// PasswordSecret is a Secret in Namespace; PasswordKey the key of the
	// password in it ("password" when empty).
	PasswordSecret, PasswordKey string
	// SSLMode is SSLModeRequire (encrypted, not verified: the tunnel ends at
	// a pod, not at a name a certificate carries) or SSLModeDisable. Empty is
	// SSLModeRequire.
	SSLMode string
}

// Target is the Service a connection to x lands behind.
func (x External) Target() Target { return Target{Namespace: x.Namespace, Service: x.Service} }

// ExternalConnConfig reads x's password from its Secret (that one key of that
// one Secret, nothing else) and returns a config that dials x through the
// tunnel, as x.Username. The config holds the password; do not log it.
func (c *Client) ExternalConnConfig(ctx context.Context, x External) (*pgx.ConnConfig, error) {
	if x.Namespace == "" || x.Service == "" || x.Database == "" || x.Username == "" || x.PasswordSecret == "" {
		return nil, errors.New("kubedial: an external database needs a namespace, a service, a database, a username and a password secret")
	}
	key := x.PasswordKey
	if key == "" {
		key = "password"
	}
	sec, err := c.cs.CoreV1().Secrets(x.Namespace).Get(ctx, x.PasswordSecret, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, fmt.Errorf("kubedial: secret %s/%s not found: the external database's password_secret names no Secret in that namespace", x.Namespace, x.PasswordSecret)
	case err != nil:
		return nil, fmt.Errorf("kubedial: read secret %s/%s: %w%s", x.Namespace, x.PasswordSecret, err, externalForbiddenHint(err, x))
	}
	password := string(sec.Data[key])
	if password == "" {
		return nil, fmt.Errorf("kubedial: secret %s/%s has no %q key (the external database's password_key)", x.Namespace, x.PasswordSecret, key)
	}
	t := x.Target()
	dsn := (&url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(x.Username, password),
		Host:     net.JoinHostPort(t.Host(), strconv.Itoa(PostgresPort)),
		Path:     "/" + x.Database,
		RawQuery: "sslmode=disable",
	}).String()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// The error can quote the DSN, and the DSN carries the password.
		return nil, errors.New("kubedial: build connection config: invalid connection parameters")
	}
	switch x.SSLMode {
	case "", SSLModeRequire:
		//nolint:gosec // sslmode=require is encryption without verification: there is no name to verify at the end of a tunnel to a pod.
		cfg.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
	case SSLModeDisable:
		cfg.TLSConfig = nil
	default:
		return nil, fmt.Errorf("kubedial: sslmode %q: an external database takes %s or %s", x.SSLMode, SSLModeRequire, SSLModeDisable)
	}
	// The host is a cluster-internal name with no DNS here; the dial ignores
	// it, and pgx must not try to resolve it first.
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	cfg.DialFunc = c.DialFunc(t)
	return cfg, nil
}

func externalForbiddenHint(err error, x External) string {
	if apierrors.IsForbidden(err) {
		return fmt.Sprintf("\nfix: the identity needs secrets get on %s, pods/portforward get+create and endpointslices get+list in namespace %s", x.PasswordSecret, x.Namespace)
	}
	return ""
}
