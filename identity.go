package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Identity is this computer's long-lived key pair. Computers recognise each other by the
// SHA-256 fingerprint of the certificate, so there are no passwords or accounts.
type Identity struct {
	Cert tls.Certificate
	FP   string
}

func loadOrCreateIdentity(dir string) (*Identity, error) {
	certFile, keyFile := filepath.Join(dir, "node.crt"), filepath.Join(dir, "node.key")
	_, certErr := os.Stat(certFile)
	_, keyErr := os.Stat(keyFile)
	if certErr == nil || keyErr == nil {
		// An identity exists: use it, or say what is wrong. Making a new one would quietly
		// undo every pairing (helpers and phones know this computer by it).
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err == nil {
			var leaf *x509.Certificate
			if leaf, err = x509.ParseCertificate(cert.Certificate[0]); err == nil {
				return &Identity{Cert: cert, FP: fingerprint(leaf.Raw)}, nil
			}
		}
		return nil, fmt.Errorf("this computer's identity (%s and %s) cannot be read: %w. Restore them from a backup, or delete both to start over (every helper and phone must then be paired again)", certFile, keyFile, err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "potluck node"},
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().AddDate(30, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	// Whole files or none: written aside, then moved into place.
	for _, f := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{{keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600},
		{certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644}} {
		if err := os.WriteFile(f.path+".tmp", f.data, f.mode); err != nil {
			return nil, err
		}
		if err := os.Rename(f.path+".tmp", f.path); err != nil {
			return nil, err
		}
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &Identity{Cert: cert, FP: fingerprint(der)}, nil
}

func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// tlsConfig is used for both directions. Certificates are self-signed, so the usual chain
// check is off; every connection is checked against pinned fingerprints right after the
// handshake instead (see peerFP).
func (id *Identity) tlsConfig() *tls.Config {
	return &tls.Config{
		Certificates:       []tls.Certificate{id.Cert},
		ClientAuth:         tls.RequireAnyClientCert,
		InsecureSkipVerify: true, // pinned-fingerprint check is done by the caller
		MinVersion:         tls.VersionTLS13,
	}
}

func peerFP(c *tls.Conn) (string, error) {
	st := c.ConnectionState()
	if len(st.PeerCertificates) == 0 {
		return "", fmt.Errorf("peer sent no certificate")
	}
	return fingerprint(st.PeerCertificates[0].Raw), nil
}

// pairCode is the short number shown on both screens while two computers pair. It depends on
// both certificates, so someone in the middle would make the two screens disagree.
func pairCode(fpA, fpB string) string {
	fps := []string{fpA, fpB}
	sort.Strings(fps)
	sum := sha256.Sum256([]byte(fps[0] + "|" + fps[1]))
	n := (uint32(sum[0])<<24 | uint32(sum[1])<<16 | uint32(sum[2])<<8 | uint32(sum[3])) % 10000
	return fmt.Sprintf("%04d", n)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
