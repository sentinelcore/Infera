package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

type API struct {
	st           *Store
	hub          *Hub
	cfg          *Config
	inviteCode   string
	adminToken   string
	starterCents int64
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

// POST /api/signup {email, kind: owner|customer, invite}
func (a *API) Signup(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Kind, Invite string }
	if readJSON(r, &in) != nil || !strings.Contains(in.Email, "@") {
		apiError(w, 400, "enter a valid email")
		return
	}
	if a.inviteCode != "" && in.Invite != a.inviteCode {
		apiError(w, 403, "beta invite code is wrong; ask the Infera team for one")
		return
	}
	credits := int64(0)
	if in.Kind == "customer" {
		credits = a.starterCents * microUSD / 100
	}
	acct, key, err := a.st.CreateAccount(in.Email, in.Kind, credits)
	if err != nil {
		apiError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"account_id": acct.ID, "kind": acct.Kind, "api_key": key,
		"note": "Save this key now. It is shown only once."})
}

func (a *API) auth(w http.ResponseWriter, r *http.Request, kind string) *Account {
	acct, err := a.st.AccountByKey(bearer(r))
	if err != nil || (kind != "" && acct.Kind != kind) {
		apiError(w, 401, "sign in with your "+kind+" key")
		return nil
	}
	return acct
}

// POST /api/pair-code (owner) -> 6-digit code valid 15 minutes
func (a *API) PairCode(w http.ResponseWriter, r *http.Request) {
	acct := a.auth(w, r, "owner")
	if acct == nil {
		return
	}
	code, err := a.st.CreatePairCode(acct.ID)
	if err != nil {
		apiError(w, 500, "could not create a code")
		return
	}
	writeJSON(w, map[string]any{"code": code, "expires_in_s": 900})
}

// POST /api/devices/pair {code, name, pubkey, platform} (called by the agent)
func (a *API) PairDevice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code, Name, Pubkey, Platform string
		HwID                         string `json:"hw_id"`
	}
	if readJSON(r, &in) != nil || in.Code == "" || in.Pubkey == "" {
		apiError(w, 400, "code and pubkey are required")
		return
	}
	if in.Name == "" {
		in.Name = "My device"
	}
	hw := ""
	if in.HwID != "" {
		hw = hashSecret("hw|" + in.HwID) // store only a hash of the machine ID
	}
	id, token, err := a.st.ConsumePairCode(strings.TrimSpace(in.Code), in.Name, in.Pubkey, in.Platform, hw)
	if err != nil {
		code := 400
		if err == ErrPairedElsewhere {
			code = 409
		}
		apiError(w, code, err.Error())
		return
	}
	writeJSON(w, map[string]any{"device_id": id, "token": token})
}

type deviceView struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Platform       string   `json:"platform"`
	Class          string   `json:"class"`
	PrivacyLevel   string   `json:"privacy_level"`
	Dependability  float64  `json:"dependability"`
	Band           Band     `json:"band"`
	Streak         int      `json:"streak"`
	SuspendedUntil int64    `json:"suspended_until"`
	Online         bool     `json:"online"`
	Status         string   `json:"status"`
	Mode           string   `json:"mode"`
	Models         []string `json:"models"`
	LastSeen       int64    `json:"last_seen"`
	Bench          any      `json:"bench"`
	PublicTag      string   `json:"public_tag"`
}

func (a *API) viewDevice(d *DeviceRow) deviceView {
	v := deviceView{ID: d.ID, Name: d.Name, Platform: d.Platform, Class: d.Class, PrivacyLevel: d.PrivacyLevel,
		Dependability: d.Dependability, Band: BandFor(d.Dependability), Streak: d.Streak, SuspendedUntil: d.SuspendedUntil,
		LastSeen: d.LastSeen, Status: "offline", PublicTag: publicTag(d.ID)}
	if d.Models != "" {
		v.Models = strings.Split(d.Models, ",")
	}
	var bench any
	json.Unmarshal([]byte(d.Bench), &bench)
	v.Bench = bench
	if n := a.hub.Node(d.ID); n != nil {
		n.mu.Lock()
		v.Online, v.Status, v.Mode = n.Online, n.Status, n.Mode
		if !n.Online {
			v.Status = "offline"
		}
		n.mu.Unlock()
	}
	return v
}

// GET /api/owner (owner) -> devices, balance, ledger, penalties
func (a *API) Owner(w http.ResponseWriter, r *http.Request) {
	acct := a.auth(w, r, "owner")
	if acct == nil {
		return
	}
	devs, _ := a.st.DevicesByOwner(acct.ID)
	var views []deviceView
	for _, d := range devs {
		views = append(views, a.viewDevice(d))
	}
	writeJSON(w, map[string]any{
		"account":   map[string]any{"id": acct.ID, "email": acct.Email, "payout_network": acct.PayoutNetwork, "payout_address": acct.PayoutAddress, "payout_address_set_at": acct.PayoutAddressSetAt},
		"devices":   views,
		"balance":   a.st.OwnerBalance(acct.ID),
		"today":     a.st.OwnerJobStats(acct.ID),
		"ledger":    a.st.RecentLedger(acct.ID, 50),
		"penalties": a.st.RecentScoreEvents(acct.ID, 50),
	})
}

// GET /api/device/me (device token) -> used by the agent's local page
func (a *API) DeviceMe(w http.ResponseWriter, r *http.Request) {
	dev, err := a.st.DeviceAuth(r.URL.Query().Get("device_id"), bearer(r))
	if err != nil {
		apiError(w, 401, "unknown device")
		return
	}
	writeJSON(w, map[string]any{
		"device":  a.viewDevice(dev),
		"balance": a.st.OwnerBalance(dev.OwnerID),
		"today":   a.st.OwnerJobStats(dev.OwnerID),
	})
}

var addrPatterns = map[string]*regexp.Regexp{
	"tron":     regexp.MustCompile(`^T[1-9A-HJ-NP-Za-km-z]{33}$`),
	"ethereum": regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`),
	"arbitrum": regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`),
	"bnb":      regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`),
	"polygon":  regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`),
	"solana":   regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,44}$`),
	"ton":      regexp.MustCompile(`^[A-Za-z0-9_-]{48}$`),
}

// POST /api/owner/payout-address {network, address}
func (a *API) PayoutAddress(w http.ResponseWriter, r *http.Request) {
	acct := a.auth(w, r, "owner")
	if acct == nil {
		return
	}
	var in struct{ Network, Address string }
	readJSON(r, &in)
	in.Network = strings.ToLower(strings.TrimSpace(in.Network))
	in.Address = strings.TrimSpace(in.Address)
	re, ok := addrPatterns[in.Network]
	if !ok {
		apiError(w, 400, "network must be tron, ethereum, arbitrum, bnb, polygon, solana or ton")
		return
	}
	if !re.MatchString(in.Address) {
		apiError(w, 400, "that doesn't look like a USDT address on "+in.Network)
		return
	}
	a.st.SetPayoutAddress(acct.ID, in.Network, in.Address)
	writeJSON(w, map[string]any{"ok": true, "note": "Payouts to this address start after a 48-hour safety hold."})
}

// GET /api/customer (customer) -> credits and recent charges
func (a *API) Customer(w http.ResponseWriter, r *http.Request) {
	acct := a.auth(w, r, "customer")
	if acct == nil {
		return
	}
	writeJSON(w, map[string]any{"account": map[string]any{"id": acct.ID, "email": acct.Email}, "credits": acct.Credits,
		"ledger": a.st.RecentLedger(acct.ID, 50)})
}

func (a *API) admin(w http.ResponseWriter, r *http.Request) bool {
	if a.adminToken == "" || bearer(r) != a.adminToken {
		apiError(w, 401, "admin token required")
		return false
	}
	return true
}

// GET /api/admin/payouts?min_usd=10&format=csv -> this week's USDT batch
func (a *API) PayoutBatch(w http.ResponseWriter, r *http.Request) {
	if !a.admin(w, r) {
		return
	}
	minUSD, _ := strconv.ParseFloat(r.URL.Query().Get("min_usd"), 64)
	if minUSD <= 0 {
		minUSD = 10
	}
	lines := a.st.PayoutCandidates(int64(minUSD * microUSD))
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		cw := csv.NewWriter(w)
		cw.Write([]string{"account_id", "email", "network", "address", "amount_usdt"})
		for _, l := range lines {
			cw.Write([]string{l.AccountID, l.Email, l.Network, l.Address, fmt.Sprintf("%.2f", float64(l.Amount)/microUSD)})
		}
		cw.Flush()
		return
	}
	writeJSON(w, map[string]any{"min_usd": minUSD, "lines": lines})
}

// POST /api/admin/payouts/mark-sent {account_id, amount_usdt, network, address, tx_hash}
func (a *API) MarkPaid(w http.ResponseWriter, r *http.Request) {
	if !a.admin(w, r) {
		return
	}
	var in struct {
		AccountID  string  `json:"account_id"`
		AmountUSDT float64 `json:"amount_usdt"`
		Network    string  `json:"network"`
		Address    string  `json:"address"`
		TxHash     string  `json:"tx_hash"`
	}
	if readJSON(r, &in) != nil || in.AccountID == "" || in.TxHash == "" || in.AmountUSDT <= 0 {
		apiError(w, 400, "account_id, amount_usdt and tx_hash are required")
		return
	}
	if err := a.st.RecordPayout(in.AccountID, int64(in.AmountUSDT*microUSD), in.Network, in.Address, in.TxHash); err != nil {
		apiError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// POST /api/admin/credits {account_id, usd} -> top up a customer (beta: manual billing)
func (a *API) GrantCredits(w http.ResponseWriter, r *http.Request) {
	if !a.admin(w, r) {
		return
	}
	var in struct {
		AccountID string  `json:"account_id"`
		USD       float64 `json:"usd"`
	}
	if readJSON(r, &in) != nil || in.AccountID == "" || in.USD == 0 {
		apiError(w, 400, "account_id and usd are required")
		return
	}
	amt := int64(in.USD * microUSD)
	a.st.AdjustCredits(in.AccountID, amt)
	a.st.AddLedger(in.AccountID, "", "", "credit_grant", amt, 0, "admin top-up")
	writeJSON(w, map[string]any{"ok": true})
}

// GET /api/network -> public stats for the landing page
func (a *API) Network(w http.ResponseWriter, r *http.Request) {
	a.hub.mu.Lock()
	online := 0
	for _, n := range a.hub.nodes {
		if n.Online {
			online++
		}
	}
	a.hub.mu.Unlock()
	st := a.st.netStats()
	writeJSON(w, map[string]any{"devices_online": online, "models": a.hub.OnlineModels(),
		"jobs_today": st.JobsToday, "tokens_today": st.TokensToday, "devices_registered": st.Devices, "waitlist": st.Waitlist})
}
