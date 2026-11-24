package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// Sealed jobs (ARCHITECTURE.md §3.3): the device's sandbox sends a fresh X25519 public key
// when it accepts a job. The gateway derives two keys with HKDF-SHA256 over the shared secret:
// "down" encrypts the job to the sandbox, "up" encrypts streamed output back to the gateway.
// The agent and the relay only ever see ciphertext.

const sealInfo = "infera sealed job v1"

type Seal struct {
	EphemeralPub []byte
	down, up     []byte
}

func NewSeal(jobID string, sandboxPubB64 string) (*Seal, error) {
	peer, err := base64.StdEncoding.DecodeString(sandboxPubB64)
	if err != nil || len(peer) != 32 {
		return nil, errors.New("bad sandbox public key")
	}
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		return nil, err
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	shared, err := curve25519.X25519(priv, peer)
	if err != nil {
		return nil, err
	}
	keys := make([]byte, 64)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, []byte(jobID), []byte(sealInfo)), keys); err != nil {
		return nil, err
	}
	return &Seal{EphemeralPub: pub, down: keys[:32], up: keys[32:]}, nil
}

func (s *Seal) SealDown(plain []byte) (nonce, ct []byte, err error) {
	aead, err := chacha20poly1305.New(s.down)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, aead.NonceSize())
	rand.Read(nonce)
	return nonce, aead.Seal(nil, nonce, plain, nil), nil
}

func (s *Seal) OpenUp(nonce, ct []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(s.up)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, ct, nil)
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func unb64(s string) []byte {
	b, _ := base64.StdEncoding.DecodeString(s)
	return b
}

// VerifyReceipt checks the device's Ed25519 signature over the receipt string.
func VerifyReceipt(pubB64, receipt, sigB64 string) bool {
	pub := unb64(pubB64)
	sig := unb64(sigB64)
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, []byte(receipt), sig)
}
