// Copyright (c) 2025 Reliant Labs
// Package certs provides self-signed certificate generation for local HTTPS/HTTP2.
package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	certFileName = "reliant.crt"
	keyFileName  = "reliant.key"
)

// CertPaths holds the paths to the certificate and key files
type CertPaths struct {
	CertFile string
	KeyFile  string
}

// EnsureCerts checks for existing certs or generates new ones.
// Certs are stored in the provided directory (typically app data dir).
// Returns paths to cert and key files.
func EnsureCerts(certsDir string) (*CertPaths, error) {
	if err := os.MkdirAll(certsDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create certs directory: %w", err)
	}

	paths := &CertPaths{
		CertFile: filepath.Join(certsDir, certFileName),
		KeyFile:  filepath.Join(certsDir, keyFileName),
	}

	// Check if both files exist and are valid
	if certsExist(paths) {
		logging.Info("Using existing TLS certificates", "dir", certsDir)
		return paths, nil
	}

	// Generate new certs
	logging.Info("Generating new TLS certificates", "dir", certsDir)
	if err := generateCerts(paths); err != nil {
		return nil, fmt.Errorf("failed to generate certificates: %w", err)
	}

	return paths, nil
}

// certsExist checks if valid cert and key files exist
func certsExist(paths *CertPaths) bool {
	certInfo, err := os.Stat(paths.CertFile)
	if err != nil || certInfo.Size() == 0 {
		return false
	}

	keyInfo, err := os.Stat(paths.KeyFile)
	if err != nil || keyInfo.Size() == 0 {
		return false
	}

	// Optionally verify cert is not expired
	certPEM, err := os.ReadFile(paths.CertFile)
	if err != nil {
		return false
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return false
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}

	// Regenerate if cert expires within 7 days
	if time.Until(cert.NotAfter) < 7*24*time.Hour {
		logging.Warn("Certificate expiring soon, will regenerate", "expires", cert.NotAfter)
		return false
	}

	return true
}

// generateCerts creates a new self-signed certificate and private key
func generateCerts(paths *CertPaths) error {
	// Generate ECDSA private key (P-256 curve)
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	// Create certificate template
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("failed to generate serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Reliant Labs"},
			CommonName:   "Reliant Local",
		},
		NotBefore: time.Now().Add(-1 * time.Hour),       // Valid from 1 hour ago (clock skew)
		NotAfter:  time.Now().Add(365 * 24 * time.Hour), // Valid for 1 year

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,

		// SANs for localhost
		IPAddresses: []net.IP{
			net.IPv4(127, 0, 0, 1),
			net.IPv6loopback,
		},
		DNSNames: []string{
			"localhost",
			"127.0.0.1",
		},
	}

	// Create certificate
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	// Write certificate to file
	certFile, err := os.OpenFile(paths.CertFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create cert file: %w", err)
	}
	defer certFile.Close()

	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		return fmt.Errorf("failed to write cert: %w", err)
	}

	// Write private key to file
	keyFile, err := os.OpenFile(paths.KeyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create key file: %w", err)
	}
	defer keyFile.Close()

	keyBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	if err := pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		return fmt.Errorf("failed to write key: %w", err)
	}

	logging.Info("Generated new TLS certificate",
		"cert", paths.CertFile,
		"expires", template.NotAfter.Format("2006-01-02"),
	)

	return nil
}

// GetCertFingerprint returns the SHA256 fingerprint of the certificate.
// This can be used by Electron to trust the specific cert.
func GetCertFingerprint(certPath string) (string, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return "", fmt.Errorf("failed to read cert: %w", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse cert: %w", err)
	}

	// Return the fingerprint in the format Electron expects
	fingerprint := fmt.Sprintf("%X", cert.Raw)
	return fingerprint, nil
}
