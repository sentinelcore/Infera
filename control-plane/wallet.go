package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

// Payout safety (G14, ARCHITECTURE §8.1):
//   1. Wallet ownership proof: the owner signs a one-time message with the payout wallet
//      (EVM chains and Tron: personal-sign; Solana: signMessage). TON, or owners who can't
//      sign, are verified by an admin after a small test transfer.
//   2. Identity check above a lifetime payout threshold (INFERA_KYC_THRESHOLD_USD, default
//      600): payouts wait until an admin marks the owner's identity as checked.
//   3. Sanctions screening: OFAC-listed addresses are refused when added and blocked from
//      every payout batch. Lists refresh daily (INFERA_SANCTIONS_URLS) and can be extended
//      with INFERA_BLOCKED_ADDRESSES.

func (s *Store) MigrateWallet() {
	big := "INTEGER"
	if s.pg {
		big = "BIGINT"
	}
	for _, c := range []string{
		"payout_challenge TEXT NOT NULL DEFAULT ''",
		"payout_verified_at " + big + " NOT NULL DEFAULT 0",
		"identity_verified_at " + big + " NOT NULL DEFAULT 0",
	} {
		if s.pg {
			s.db.Exec(`ALTER TABLE accounts ADD COLUMN IF NOT EXISTS ` + c)
		} else {
			s.db.Exec(`ALTER TABLE accounts ADD COLUMN ` + c)
		}
	}
}

// ---------- sanctions ----------

var defaultSanctionsURLs = []string{
	"https://raw.githubusercontent.com/0xB10C/ofac-sanctioned-digital-currency-addresses/lists/sanctioned_addresses_ETH.txt",
	"https://raw.githubusercontent.com/0xB10C/ofac-sanctioned-digital-currency-addresses/lists/sanctioned_addresses_TRX.txt",
	"https://raw.githubusercontent.com/0xB10C/ofac-sanctioned-digital-currency-addresses/lists/sanctioned_addresses_USDT.txt",
	"https://raw.githubusercontent.com/0xB10C/ofac-sanctioned-digital-currency-addresses/lists/sanctioned_addresses_USDC.txt",
	"https://raw.githubusercontent.com/0xB10C/ofac-sanctioned-digital-currency-addresses/lists/sanctioned_addresses_SOL.txt",
}

type sanctionsList struct {
	mu      sync.RWMutex
	set     map[string]bool
	updated time.Time
}

var sanctions = &sanctionsList{set: map[string]bool{}}

func normAddr(a string) string {
	a = strings.TrimSpace(a)
	if strings.HasPrefix(a, "0x") || strings.HasPrefix(a, "0X") {
		return strings.ToLower(a) // EVM addresses are case-insensitive
	}
	return a
}

func (l *sanctionsList) Blocked(addr string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.set[normAddr(addr)]
}

func (l *sanctionsList) replace(addrs []string) {
	m := map[string]bool{}
	for _, a := range addrs {
		if a = normAddr(a); a != "" && !strings.HasPrefix(a, "#") {
			m[a] = true
		}
	}
	l.mu.Lock()
	l.set, l.updated = m, time.Now()
	l.mu.Unlock()
}

// RunSanctions loads the lists now and every 24 hours.
func RunSanctions() {
	urls := defaultSanctionsURLs
	if v, ok := os.LookupEnv("INFERA_SANCTIONS_URLS"); ok {
		urls = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' })
	}
	extra := strings.FieldsFunc(os.Getenv("INFERA_BLOCKED_ADDRESSES"), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' })
	for {
		all := append([]string{}, extra...)
		ok := 0
		for _, u := range urls {
			if lines, err := fetchLines(u); err == nil {
				all = append(all, lines...)
				ok++
			} else {
				log.Printf("sanctions: %s: %v", u, err)
			}
		}
		sanctions.replace(all)
		log.Printf("sanctions: %d blocked addresses from %d of %d lists", len(all), ok, len(urls))
		if ok < len(urls) {
			mon.Incident("sanctions", fmt.Sprintf("sanctions list refresh: only %d of %d lists loaded", ok, len(urls)))
		}
		time.Sleep(24 * time.Hour)
	}
}

func fetchLines(u string) ([]string, error) {
	c := &http.Client{Timeout: 30 * time.Second}
	r, err := c.Get(u)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", r.StatusCode)
	}
	var out []string
	sc := bufio.NewScanner(r.Body)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out, sc.Err()
}

// ---------- wallet ownership proof ----------

func walletChallenge(accountID, network, address string) string {
	return fmt.Sprintf("Infera payout wallet\nAccount: %s\nNetwork: %s\nAddress: %s\nNonce: %s", accountID, network, address, randomHex(8))
}

var errBadSig = errors.New("the signature doesn't match this wallet; sign the exact message shown, with the payout wallet")

// VerifyWalletSignature checks that sig over msg was made by address on network.
func VerifyWalletSignature(network, address, msg, sig string) error {
	switch network {
	case "ethereum", "arbitrum", "bnb", "polygon":
		pub, err := recoverSecp(prefixHash("\x19Ethereum Signed Message:\n", msg), sig)
		if err != nil {
			return err
		}
		if !strings.EqualFold("0x"+hex.EncodeToString(pub), address) {
			return errBadSig
		}
		return nil
	case "tron":
		pub, err := recoverSecp(prefixHash("\x19TRON Signed Message:\n", msg), sig)
		if err != nil {
			return err
		}
		if tronAddress(pub) != address {
			return errBadSig
		}
		return nil
	case "solana":
		pk, err := base58Decode(address)
		if err != nil || len(pk) != ed25519.PublicKeySize {
			return errors.New("not a valid Solana address")
		}
		raw := decodeSig(sig)
		if len(raw) != ed25519.SignatureSize || !ed25519.Verify(pk, []byte(msg), raw) {
			return errBadSig
		}
		return nil
	}
	return errors.New("signed-message proof isn't available for this network yet; an admin will verify it with a small test transfer")
}

func keccak(b ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, x := range b {
		h.Write(x)
	}
	return h.Sum(nil)
}

func prefixHash(prefix, msg string) []byte {
	return keccak([]byte(fmt.Sprintf("%s%d%s", prefix, len(msg), msg)))
}

// decodeSig accepts hex (with or without 0x), base58 or base64.
func decodeSig(s string) []byte {
	s = strings.TrimSpace(s)
	if b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")); err == nil {
		return b
	}
	if b, err := base58Decode(s); err == nil && len(b) == 64 {
		return b
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b
	}
	return nil
}

// recoverSecp returns the 20-byte account (keccak of the public key) that signed hash.
// sig is r||s||v (65 bytes), v in {0,1,27,28}.
func recoverSecp(hash []byte, sig string) ([]byte, error) {
	b := decodeSig(sig)
	if len(b) != 65 {
		return nil, errors.New("the signature should be 65 bytes (0x… from your wallet)")
	}
	v := b[64]
	if v >= 27 {
		v -= 27
	}
	if v > 1 {
		return nil, errBadSig
	}
	compact := append([]byte{27 + v}, b[:64]...)
	pub, _, err := ecdsa.RecoverCompact(compact, hash)
	if err != nil {
		return nil, errBadSig
	}
	return keccak(pub.SerializeUncompressed()[1:])[12:], nil
}

func tronAddress(acct20 []byte) string {
	payload := append([]byte{0x41}, acct20...)
	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])
	return base58Encode(append(payload, h2[:4]...))
}

const b58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func base58Encode(b []byte) string {
	x := new(big.Int).SetBytes(b)
	var out []byte
	mod, base := new(big.Int), big.NewInt(58)
	for x.Sign() > 0 {
		x.DivMod(x, base, mod)
		out = append(out, b58[mod.Int64()])
	}
	for _, c := range b {
		if c != 0 {
			break
		}
		out = append(out, '1')
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func base58Decode(s string) ([]byte, error) {
	x := new(big.Int)
	for _, c := range s {
		i := strings.IndexRune(b58, c)
		if i < 0 {
			return nil, errors.New("not base58")
		}
		x.Mul(x, big.NewInt(58)).Add(x, big.NewInt(int64(i)))
	}
	b := x.Bytes()
	for _, c := range s {
		if c != '1' {
			break
		}
		b = append([]byte{0}, b...)
	}
	return b, nil
}

// ---------- store ----------

type WalletStatus struct {
	Challenge          string `json:"challenge,omitempty"`
	VerifiedAt         int64  `json:"verified_at"`
	IdentityVerifiedAt int64  `json:"identity_verified_at"`
}

func (s *Store) Wallet(accountID string) WalletStatus {
	var w WalletStatus
	s.queryRow(`SELECT payout_challenge,payout_verified_at,identity_verified_at FROM accounts WHERE id=?`, accountID).Scan(&w.Challenge, &w.VerifiedAt, &w.IdentityVerifiedAt)
	return w
}

func (s *Store) SetWalletChallenge(accountID, challenge string) {
	s.exec(`UPDATE accounts SET payout_challenge=?, payout_verified_at=0 WHERE id=?`, challenge, accountID)
}

func (s *Store) MarkWalletVerified(accountID string) {
	s.exec(`UPDATE accounts SET payout_verified_at=?, payout_challenge='' WHERE id=?`, now(), accountID)
}

func (s *Store) MarkIdentityVerified(accountID string) {
	s.exec(`UPDATE accounts SET identity_verified_at=? WHERE id=?`, now(), accountID)
}

func (s *Store) TotalPaid(accountID string) int64 {
	var p int64
	s.queryRow(`SELECT CAST(COALESCE(SUM(-amount),0) AS BIGINT) FROM ledger WHERE account_id=? AND kind='payout'`, accountID).Scan(&p)
	return p
}

// ---------- API ----------

// POST /api/owner/payout-address/verify {signature}
func (a *API) VerifyWallet(w http.ResponseWriter, r *http.Request) {
	acct := a.auth(w, r, "owner")
	if acct == nil {
		return
	}
	var in struct{ Signature string }
	if readJSON(r, &in) != nil || strings.TrimSpace(in.Signature) == "" {
		apiError(w, 400, "signature is required")
		return
	}
	ws := a.st.Wallet(acct.ID)
	if acct.PayoutAddress == "" || ws.Challenge == "" {
		apiError(w, 400, "add a payout wallet first")
		return
	}
	if err := VerifyWalletSignature(acct.PayoutNetwork, acct.PayoutAddress, ws.Challenge, in.Signature); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	a.st.MarkWalletVerified(acct.ID)
	writeJSON(w, map[string]any{"ok": true, "note": "Wallet verified."})
}

// POST /api/admin/verify {account_id, wallet: bool, identity: bool}
// For wallets that can't sign (test transfer confirmed) and for identity checks.
func (a *API) AdminVerify(w http.ResponseWriter, r *http.Request) {
	if !a.admin(w, r) {
		return
	}
	var in struct {
		AccountID string `json:"account_id"`
		Wallet    bool   `json:"wallet"`
		Identity  bool   `json:"identity"`
	}
	if readJSON(r, &in) != nil || in.AccountID == "" || (!in.Wallet && !in.Identity) {
		apiError(w, 400, "account_id and wallet and/or identity are required")
		return
	}
	if in.Wallet {
		a.st.MarkWalletVerified(in.AccountID)
	}
	if in.Identity {
		a.st.MarkIdentityVerified(in.AccountID)
	}
	writeJSON(w, map[string]any{"ok": true})
}

func kycThreshold() int64 {
	return int64(envFloat("INFERA_KYC_THRESHOLD_USD", 600) * microUSD)
}
