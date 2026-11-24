package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// ethStyleSig signs like a wallet's personal-sign: r||s||v with v = 27/28.
func ethStyleSig(priv *secp256k1.PrivateKey, hash []byte) string {
	c := ecdsa.SignCompact(priv, hash, false) // [27+recid][r][s]
	return "0x" + hex.EncodeToString(append(c[1:], c[0]))
}

func TestWalletProofs(t *testing.T) {
	msg := walletChallenge("acc_1", "ethereum", "x")
	priv, _ := secp256k1.GeneratePrivateKey()
	acct20 := keccak(priv.PubKey().SerializeUncompressed()[1:])[12:]

	eth := "0x" + hex.EncodeToString(acct20)
	sig := ethStyleSig(priv, prefixHash("\x19Ethereum Signed Message:\n", msg))
	if err := VerifyWalletSignature("ethereum", eth, msg, sig); err != nil {
		t.Fatalf("valid EVM signature rejected: %v", err)
	}
	if VerifyWalletSignature("polygon", "0x0000000000000000000000000000000000000001", msg, sig) == nil {
		t.Fatal("signature accepted for another address")
	}
	if VerifyWalletSignature("ethereum", eth, msg+"x", sig) == nil {
		t.Fatal("signature accepted for another message")
	}

	tron := tronAddress(acct20)
	tsig := ethStyleSig(priv, prefixHash("\x19TRON Signed Message:\n", msg))
	if err := VerifyWalletSignature("tron", tron, msg, tsig); err != nil {
		t.Fatalf("valid Tron signature rejected: %v", err)
	}
	if VerifyWalletSignature("tron", tron, msg, sig) == nil {
		t.Fatal("Ethereum-prefixed signature accepted on Tron")
	}

	pub, sk, _ := ed25519.GenerateKey(rand.Reader)
	sol := base58Encode(pub)
	if err := VerifyWalletSignature("solana", sol, msg, base58Encode(ed25519.Sign(sk, []byte(msg)))); err != nil {
		t.Fatalf("valid Solana signature rejected: %v", err)
	}
	if VerifyWalletSignature("solana", sol, msg, hex.EncodeToString(ed25519.Sign(sk, []byte("other")))) == nil {
		t.Fatal("Solana signature for another message accepted")
	}
}

func TestTronBase58(t *testing.T) {
	const usdt = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t" // the USDT contract on Tron
	b, err := base58Decode(usdt)
	if err != nil || len(b) != 25 || b[0] != 0x41 {
		t.Fatalf("decode: %x %v", b, err)
	}
	h1 := sha256.Sum256(b[:21])
	h2 := sha256.Sum256(h1[:])
	if hex.EncodeToString(h2[:4]) != hex.EncodeToString(b[21:]) {
		t.Fatal("checksum")
	}
	if tronAddress(b[1:21]) != usdt {
		t.Fatal("round trip")
	}
}

func TestPayoutHolds(t *testing.T) {
	st := mustStore(t)
	acct, _, _ := st.CreateAccount("w@x.dev", "owner", 0)
	addr := "0x1111111111111111111111111111111111111111"
	st.SetPayoutAddress(acct.ID, "ethereum", addr)
	if h := st.payoutHold(acct.ID, addr, 50*microUSD); h == "" {
		t.Fatal("unverified wallet should be held")
	}
	st.MarkWalletVerified(acct.ID)
	if h := st.payoutHold(acct.ID, addr, 50*microUSD); h != "" {
		t.Fatalf("verified wallet held: %s", h)
	}
	if h := st.payoutHold(acct.ID, addr, 700*microUSD); h == "" {
		t.Fatal("payout above the identity threshold should be held")
	}
	st.MarkIdentityVerified(acct.ID)
	if h := st.payoutHold(acct.ID, addr, 700*microUSD); h != "" {
		t.Fatalf("identity-checked owner held: %s", h)
	}
	sanctions.replace([]string{"0x1111111111111111111111111111111111111111"})
	defer sanctions.replace(nil)
	if h := st.payoutHold(acct.ID, "0X1111111111111111111111111111111111111111", 50*microUSD); h == "" {
		t.Fatal("sanctioned address not blocked")
	}
}
