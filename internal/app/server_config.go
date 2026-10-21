// Copyright (c) 2025 Reliant Labs

package app

import (
	"fmt"
	"strings"
	"time"

	configv1 "github.com/reliant-labs/reliant/gen/config/v1"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/natsutil"
)

// The server configs (proto/config/v1 ServerProcessConfig, DatabaseConfig and
// each role's command config) in the shapes the packages they configure
// take. The providers read them, and so do `reliant server api|worker`, whose
// hand-written commands load the same configs until they are roots too.

// ControlPlaneEndpoint is the shared ControlPlaneConfig in the shape the
// server packages take, trimmed as every reader of these variables trimmed
// them. The zero value is self-hosted.
func ControlPlaneEndpoint(cfg *configv1.ControlPlaneConfig) controlplane.Endpoint {
	return controlplane.Endpoint{
		URL:                   strings.TrimSpace(cfg.GetUrl()),
		InternalServiceSecret: strings.TrimSpace(cfg.GetInternalServiceSecret()),
	}
}

// DatabaseURLs is the DatabaseConfig's driver, URL and direct (session) URL.
// The URLs are trimmed, as the database layer trimmed them.
func DatabaseURLs(cfg *configv1.DatabaseConfig) (driver, url, directURL string) {
	return cfg.GetDatabaseDriver(), strings.TrimSpace(cfg.GetDatabaseUrl()), strings.TrimSpace(cfg.GetDatabaseDirectUrl())
}

// DatabasePool is the DatabaseConfig pool and wait settings in the shape the
// database layer takes. A size or a wait that cannot be honoured is refused
// rather than quietly replaced by its default.
func DatabasePool(cfg *configv1.DatabaseConfig) (db.Pool, error) {
	if n := cfg.GetDbMaxOpenConns(); n < 1 {
		return db.Pool{}, fmt.Errorf("config: RELIANT_DB_MAX_OPEN_CONNS = %d must be at least 1", n)
	}
	if n := cfg.GetDbMaxIdleConns(); n < 1 {
		return db.Pool{}, fmt.Errorf("config: RELIANT_DB_MAX_IDLE_CONNS = %d must be at least 1", n)
	}
	if s := cfg.GetDbSchemaWaitSeconds(); s < 1 {
		return db.Pool{}, fmt.Errorf("config: RELIANT_DB_SCHEMA_WAIT_SECONDS = %d must be at least 1", s)
	}
	pool := db.Pool{
		MaxOpenConns: int(cfg.GetDbMaxOpenConns()),
		MaxIdleConns: int(cfg.GetDbMaxIdleConns()),
		SchemaWait:   time.Duration(cfg.GetDbSchemaWaitSeconds()) * time.Second,
	}
	if d := cfg.GetDbConnectTimeout(); d != nil {
		if pool.ConnectTimeout = d.AsDuration(); pool.ConnectTimeout <= 0 {
			return db.Pool{}, fmt.Errorf("config: DB_CONNECT_TIMEOUT = %s is not a positive duration", pool.ConnectTimeout)
		}
	}
	return pool, nil
}

// NATSEndpoint is the shared NatsConfig as natsutil connects to it.
func NATSEndpoint(cfg *configv1.NatsConfig) natsutil.Endpoint {
	return natsutil.Endpoint{URL: cfg.GetUrl(), User: cfg.GetUser(), Password: cfg.GetPassword()}
}

// Port is a port setting, named as an operator sets it.
type Port struct {
	Name string
	Port int32
}

// CheckPorts refuses a port setting that is not a TCP port: the loader has
// parsed it as an integer, but 0 or 70000 would fail later, at bind time, as
// something less clear.
func CheckPorts(ports ...Port) error {
	for _, p := range ports {
		if p.Port < 1 || p.Port > 65535 {
			return fmt.Errorf("config: %s = %d is not a TCP port (1-65535)", p.Name, p.Port)
		}
	}
	return nil
}
