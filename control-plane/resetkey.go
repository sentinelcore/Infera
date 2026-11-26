package main

import (
	"fmt"
	"net/http"
)

// Lost keys: we store only a hash, so a key can't be shown again. An admin can issue a new
// key for the same account (devices, earnings and history stay); the old key stops working.
//   POST /api/admin/reset-key {account_id | device_id | email}

func (s *Store) ResetKey(accountID string) (string, string, error) {
	var kind string
	if err := s.queryRow(`SELECT kind FROM accounts WHERE id=?`, accountID).Scan(&kind); err != nil {
		return "", "", fmt.Errorf("no such account")
	}
	prefix := "ik_cust_"
	if kind == "owner" {
		prefix = "ik_own_"
	}
	key := prefix + randomHex(24)
	_, err := s.exec(`UPDATE accounts SET key_hash=? WHERE id=?`, hashSecret(key), accountID)
	return key, kind, err
}

func (a *API) AdminResetKey(w http.ResponseWriter, r *http.Request) {
	if !a.admin(w, r) {
		return
	}
	var in struct {
		AccountID string `json:"account_id"`
		DeviceID  string `json:"device_id"`
		Email     string `json:"email"`
	}
	readJSON(r, &in)
	id := in.AccountID
	switch {
	case id == "" && in.DeviceID != "":
		a.st.queryRow(`SELECT owner_id FROM devices WHERE id=?`, in.DeviceID).Scan(&id)
	case id == "" && in.Email != "":
		a.st.queryRow(`SELECT id FROM accounts WHERE email=? ORDER BY created_at LIMIT 1`, in.Email).Scan(&id)
	}
	if id == "" {
		apiError(w, 404, "no matching account")
		return
	}
	key, kind, err := a.st.ResetKey(id)
	if err != nil {
		apiError(w, 404, err.Error())
		return
	}
	writeJSON(w, map[string]any{"account_id": id, "kind": kind, "api_key": key, "note": "The old key no longer works."})
}
