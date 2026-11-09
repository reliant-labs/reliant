package kubedial

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/reliant-labs/forge/pkg/release"
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
	// SSLMode is SSLModeRequire (the default, encryption without verification),
	// SSLModeDisable, or SSLModeVerifyFull (authenticate the Service DNS name).
	SSLMode string
	// Exactly one namespace-local CA source and an explicit key are required
	// for verify-full. Other modes refuse these fields. No inline PEM or paths.
	CASecret, CAConfigMap, CAKey string
	// TLSServerName defaults to <service>.<namespace>.svc for verify-full.
	TLSServerName string
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
	// Share the recorded route validator so direct and CLI readers cannot
	// disagree on which trust combinations are meaningful. No API reads yet.
	route := release.ShapeExternalDatabase{
		Name: "external", Namespace: x.Namespace, Service: x.Service,
		Database: x.Database, Username: x.Username, PasswordSecret: x.PasswordSecret,
		PasswordKey: x.PasswordKey, SSLMode: x.SSLMode, CASecret: x.CASecret,
		CAConfigMap: x.CAConfigMap, CAKey: x.CAKey, TLSServerName: x.TLSServerName,
	}
	if x.SSLMode == SSLModeVerifyFull || x.CASecret != "" || x.CAConfigMap != "" || x.CAKey != "" || x.TLSServerName != "" {
		if err := route.Validate(); err != nil {
			return nil, fmt.Errorf("kubedial: %w", err)
		}
	} else if x.SSLMode != "" && x.SSLMode != SSLModeRequire && x.SSLMode != SSLModeDisable {
		return nil, errors.New("kubedial: sslmode must be require, disable or verify-full")
	}
	tlsConfig, err := c.externalTLSConfig(ctx, x)
	if err != nil {
		return nil, err
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
	cfg.TLSConfig = tlsConfig
	// The host is a cluster-internal name with no DNS here; the dial ignores
	// it, and pgx must not try to resolve it first.
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	cfg.DialFunc = c.DialFunc(t)
	return cfg, nil
}

// externalTLSConfig runs after reference validation and before the password
// read. It fetches current CA bytes on every invocation: rotation takes effect
// in new configs; callers retaining a config must rebuild it after rotation.
func (c *Client) externalTLSConfig(ctx context.Context, x External) (*tls.Config, error) {
	switch x.SSLMode {
	case "", SSLModeRequire:
		//nolint:gosec // Backwards-compatible libpq require: encryption without authentication.
		return &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, nil
	case SSLModeDisable:
		return nil, nil
	}
	var ca []byte
	if x.CASecret != "" {
		sec, err := c.cs.CoreV1().Secrets(x.Namespace).Get(ctx, x.CASecret, metav1.GetOptions{})
		if err != nil {
			// API errors can contain arbitrary response bodies. Do not echo them.
			return nil, errors.New("kubedial: cannot read external database CA Secret")
		}
		ca = sec.Data[x.CAKey]
	} else {
		cm, err := c.cs.CoreV1().ConfigMaps(x.Namespace).Get(ctx, x.CAConfigMap, metav1.GetOptions{})
		if err != nil {
			return nil, errors.New("kubedial: cannot read external database CA ConfigMap")
		}
		text, textOK := cm.Data[x.CAKey]
		binary, binaryOK := cm.BinaryData[x.CAKey]
		if textOK && binaryOK {
			return nil, errors.New("kubedial: external database CA key occurs in both ConfigMap data and binaryData")
		}
		ca = []byte(text)
		if binaryOK {
			ca = binary
		}
	}
	// Strict certificate-only PEM prevents partial acceptance of a bundle with
	// malformed blocks or accidental private keys. Never log trust bytes.
	pool := x509.NewCertPool()
	count := 0
	for rest := bytes.TrimSpace(ca); len(rest) > 0; {
		if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, errors.New("kubedial: external database CA bundle is not certificate PEM")
		}
		block, tail := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || bytes.Count(rest[:len(rest)-len(tail)], []byte("-----BEGIN ")) != 1 {
			return nil, errors.New("kubedial: external database CA bundle is not certificate PEM")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, errors.New("kubedial: external database CA bundle contains an invalid certificate")
		}
		pool.AddCert(cert)
		count++
		rest = bytes.TrimSpace(tail)
	}
	if count == 0 {
		return nil, errors.New("kubedial: external database CA bundle is empty or missing its selected key")
	}
	name := x.TLSServerName
	if name == "" {
		name = x.Target().Host()
	}
	return &tls.Config{RootCAs: pool, ServerName: name, MinVersion: tls.VersionTLS12}, nil
}

func externalForbiddenHint(err error, x External) string {
	if apierrors.IsForbidden(err) {
		return fmt.Sprintf("\nfix: the identity needs secrets get on %s, pods/portforward get+create and endpointslices get+list in namespace %s", x.PasswordSecret, x.Namespace)
	}
	return ""
}
