// Package certutil generates a self-signed TLS certificate/key pair for
// this GoDrop instance. There is no central CA on a LAN, so GoDrop trusts
// peers on a "trust on first use / same network" basis: TLS here buys us
// encryption-in-transit (protection from passive snooping on the LAN)
// rather than strong identity verification. That's an intentional
// trade-off for a local file-sharing tool, not an oversight — call it
// out to users if they ask about the security model.
package certutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"
)

// GenerateSelfSigned creates an in-memory ECDSA self-signed certificate
// valid for the given hostname/peer ID, usable directly as a tls.Certificate.
func GenerateSelfSigned(commonName string) (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("certutil: generate key: %w", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("certutil: generate serial: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName, Organization: []string{"GoDrop"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * 365 * time.Hour), // ~1 year
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{commonName, "localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("certutil: create certificate: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  priv,
	}, nil
}

// InsecureSkipVerifyConfig returns a tls.Config for peers that trusts
// whatever certificate the other side presents. This is appropriate for
// a same-LAN, trust-on-first-use tool where the threat model is "prevent
// casual eavesdropping," not "verify peer identity cryptographically."
// Do not reuse this pattern for anything internet-facing.
func InsecureSkipVerifyConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	}
}