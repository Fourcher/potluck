package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The token Apple requires: ES256 over header.claims, signature r||s, verifiable with the key.
func TestAPNsToken(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	path := filepath.Join(t.TempDir(), "AuthKey_TEST.p8")
	_ = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	tok, err := apnsToken(PushConfig{KeyPath: path, KeyID: "KEY123", TeamID: "TEAM456"})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", tok)
	}
	var hdr, claims map[string]any
	b, _ := base64.RawURLEncoding.DecodeString(parts[0])
	_ = json.Unmarshal(b, &hdr)
	b, _ = base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(b, &claims)
	if hdr["alg"] != "ES256" || hdr["kid"] != "KEY123" || claims["iss"] != "TEAM456" {
		t.Fatalf("header %v claims %v", hdr, claims)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(&key.PublicKey, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("signature does not verify")
	}
	if _, err := apnsToken(PushConfig{KeyPath: filepath.Join(t.TempDir(), "missing.p8"), KeyID: "OTHER"}); err == nil {
		t.Fatal("a missing key file was accepted")
	}
}
