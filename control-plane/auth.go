package main

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Email + password sign-in for the dashboard, so nobody has to keep a key.
//
//   POST /api/signup   {email, kind, invite, password}  -> account + session (+ API key for developers)
//   POST /api/login    {email, password, kind}          -> session token (ik_ses_…), works like the key
//   POST /api/logout                                    (session)
//   POST /api/account/password {password}               set or change the password (signed in)
//   POST /api/account/new-key                           developers: a new API key (the old one stops)
//
// Sessions last 90 days from last use. Accounts made before passwords existed sign in once
// with their key (or a key from support) and set a password.

const sessionTTL = 90 * 24 * 3600

const authSchema = `
CREATE TABLE IF NOT EXISTS passwords (
  account_id TEXT PRIMARY KEY,
  hash TEXT NOT NULL,
  set_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  account_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_used INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_account ON sessions(account_id);
`

func (s *Store) MigrateAuth() error {
	q := authSchema
	if s.pg {
		q = strings.ReplaceAll(q, "INTEGER", "BIGINT")
	}
	_, err := s.db.Exec(q)
	return err
}

func (s *Store) SetPassword(accountID, password string) error {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err := s.exec(`DELETE FROM passwords WHERE account_id=?`, accountID); err != nil {
		return err
	}
	_, err = s.exec(`INSERT INTO passwords(account_id,hash,set_at) VALUES(?,?,?)`, accountID, string(h), now())
	return err
}

func (s *Store) HasPassword(accountID string) bool {
	var n int
	s.queryRow(`SELECT COUNT(*) FROM passwords WHERE account_id=?`, accountID).Scan(&n)
	return n > 0
}

// CheckLogin returns the account of that kind whose password matches.
func (s *Store) CheckLogin(email, kind, password string) (*Account, bool) {
	rows, err := s.query(`SELECT a.id, p.hash FROM accounts a JOIN passwords p ON p.account_id=a.id WHERE lower(a.email)=lower(?) AND a.kind=? ORDER BY a.created_at`, strings.TrimSpace(email), kind)
	if err != nil {
		return nil, false
	}
	type cand struct{ id, hash string }
	var cs []cand
	for rows.Next() {
		var c cand
		if rows.Scan(&c.id, &c.hash) == nil {
			cs = append(cs, c)
		}
	}
	rows.Close()
	for _, c := range cs {
		if bcrypt.CompareHashAndPassword([]byte(c.hash), []byte(password)) == nil {
			a, err := s.AccountByID(c.id)
			return a, err == nil
		}
	}
	return nil, false
}

func (s *Store) NewSession(accountID string) (string, error) {
	tok := "ik_ses_" + randomHex(24)
	s.exec(`DELETE FROM sessions WHERE last_used < ?`, now()-sessionTTL)
	_, err := s.exec(`INSERT INTO sessions(token_hash,account_id,created_at,last_used) VALUES(?,?,?,?)`, hashSecret(tok), accountID, now(), now())
	return tok, err
}

func (s *Store) sessionAccount(tok string) (*Account, error) {
	var id string
	var last int64
	if err := s.queryRow(`SELECT account_id,last_used FROM sessions WHERE token_hash=?`, hashSecret(tok)).Scan(&id, &last); err != nil {
		return nil, err
	}
	if now()-last > sessionTTL {
		s.exec(`DELETE FROM sessions WHERE token_hash=?`, hashSecret(tok))
		return nil, errNoSession
	}
	if now()-last > 3600 {
		s.exec(`UPDATE sessions SET last_used=? WHERE token_hash=?`, now(), hashSecret(tok))
	}
	return s.AccountByID(id)
}

type authErr string

func (e authErr) Error() string { return string(e) }

const errNoSession = authErr("session expired")

// Login attempts: at most 10 per email per 15 minutes.
var loginTries = struct {
	sync.Mutex
	m map[string][]time.Time
}{m: map[string][]time.Time{}}

func loginAllowed(email string) bool {
	loginTries.Lock()
	defer loginTries.Unlock()
	k := strings.ToLower(strings.TrimSpace(email))
	cut := time.Now().Add(-15 * time.Minute)
	var keep []time.Time
	for _, t := range loginTries.m[k] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= 10 {
		loginTries.m[k] = keep
		return false
	}
	loginTries.m[k] = append(keep, time.Now())
	return true
}

func validPassword(p string) bool { return len(p) >= 8 && len(p) <= 200 }

// POST /api/login
func (a *API) Login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password, Kind string }
	if readJSON(r, &in) != nil || in.Email == "" || in.Password == "" {
		apiError(w, 400, "enter your email and password")
		return
	}
	if in.Kind == "" {
		in.Kind = "owner"
	}
	if !loginAllowed(in.Email) {
		apiError(w, 429, "too many tries; wait 15 minutes")
		return
	}
	acct, ok := a.st.CheckLogin(in.Email, in.Kind, in.Password)
	if !ok {
		apiError(w, 401, "wrong email or password. Accounts made before passwords: sign in once with your key and set a password, or ask support for a new key")
		return
	}
	tok, err := a.st.NewSession(acct.ID)
	if err != nil {
		apiError(w, 500, "could not sign you in")
		return
	}
	writeJSON(w, map[string]any{"session": tok, "account_id": acct.ID, "kind": acct.Kind, "email": acct.Email})
}

// POST /api/logout
func (a *API) Logout(w http.ResponseWriter, r *http.Request) {
	if t := bearer(r); strings.HasPrefix(t, "ik_ses_") {
		a.st.exec(`DELETE FROM sessions WHERE token_hash=?`, hashSecret(t))
	}
	writeJSON(w, map[string]any{"ok": true})
}

// POST /api/account/password
func (a *API) SetPassword(w http.ResponseWriter, r *http.Request) {
	acct := a.auth(w, r, "")
	if acct == nil {
		return
	}
	var in struct{ Password string }
	if readJSON(r, &in) != nil || !validPassword(in.Password) {
		apiError(w, 400, "use a password of at least 8 characters")
		return
	}
	if err := a.st.SetPassword(acct.ID, in.Password); err != nil {
		apiError(w, 500, "could not save the password")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// GET /api/account
func (a *API) AccountInfo(w http.ResponseWriter, r *http.Request) {
	acct := a.auth(w, r, "")
	if acct == nil {
		return
	}
	writeJSON(w, map[string]any{"account_id": acct.ID, "email": acct.Email, "kind": acct.Kind, "has_password": a.st.HasPassword(acct.ID)})
}

// POST /api/account/new-key: a fresh API key (shown once); the old key stops working.
func (a *API) NewKey(w http.ResponseWriter, r *http.Request) {
	acct := a.auth(w, r, "")
	if acct == nil {
		return
	}
	key, _, err := a.st.ResetKey(acct.ID)
	if err != nil {
		apiError(w, 500, "could not make a key")
		return
	}
	writeJSON(w, map[string]any{"api_key": key, "note": "Copy it now; it is shown only once. The old key no longer works."})
}
