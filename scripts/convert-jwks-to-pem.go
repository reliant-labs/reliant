// Copyright (c) 2025 Reliant Labs
//go:build ignore

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"os"
)

type JWKS struct {
	Keys []JWK `json:"keys"`
}

type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Kid string `json:"kid"`
}

func main() {
	// Read JWKS JSON from stdin or first argument
	var jwksJSON []byte
	var err error

	if len(os.Args) > 1 {
		jwksJSON = []byte(os.Args[1])
	} else {
		jwksJSON, err = io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
			os.Exit(1)
		}
	}

	// Parse JWKS
	var jwks JWKS
	if err := json.Unmarshal(jwksJSON, &jwks); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing JWKS: %v\n", err)
		os.Exit(1)
	}

	if len(jwks.Keys) == 0 {
		fmt.Fprintf(os.Stderr, "No keys found in JWKS\n")
		os.Exit(1)
	}

	// Use first key
	key := jwks.Keys[0]

	if key.Kty != "EC" {
		fmt.Fprintf(os.Stderr, "Unsupported key type: %s (expected EC)\n", key.Kty)
		os.Exit(1)
	}

	// Determine curve
	var curve elliptic.Curve
	switch key.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		fmt.Fprintf(os.Stderr, "Unsupported curve: %s\n", key.Crv)
		os.Exit(1)
	}

	// Decode X coordinate
	xBytes, err := base64.RawURLEncoding.DecodeString(key.X)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding X coordinate: %v\n", err)
		os.Exit(1)
	}

	// Decode Y coordinate
	yBytes, err := base64.RawURLEncoding.DecodeString(key.Y)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding Y coordinate: %v\n", err)
		os.Exit(1)
	}

	// Create ECDSA public key
	ecdsaPub := &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}

	// Marshal to DER format
	derBytes, err := x509.MarshalPKIXPublicKey(ecdsaPub)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling public key: %v\n", err)
		os.Exit(1)
	}

	// Create PEM block
	pemBlock := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: derBytes,
	}

	// Encode to PEM
	if err := pem.Encode(os.Stdout, pemBlock); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding PEM: %v\n", err)
		os.Exit(1)
	}
}
