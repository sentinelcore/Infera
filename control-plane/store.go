package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Money is stored as integer micro-USD (1 USD = 1_000_000).
const microUSD = 1_000_000

// Store works with SQLite (local and small deployments) or Postgres (INFERA_DATABASE_URL=postgres://…).
type Store struct {
	db *sql.DB
	pg bool
}

// rebind turns ? placeholders into $1, $2 … for Postgres.
func (s *Store) rebind(q string) string {
	if !s.pg {
		return q
	}
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *Store) exec(q string, args ...any) (sql.Result, error) {
	return s.db.Exec(s.rebind(q), args...)
}
func (s *Store) query(q string, args ...any) (*sql.Rows, error) {
	return s.db.Query(s.rebind(q), args...)
}
func (s *Store) queryRow(q string, args ...any) *sql.Row { return s.db.QueryRow(s.rebind(q), args...) }

const schema = `
CREATE TABLE IF NOT EXISTS accounts (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL,
  kind TEXT NOT NULL,            -- owner | customer
  key_hash TEXT NOT NULL UNIQUE,
  credits INTEGER NOT NULL DEFAULT 0,   -- customer prepaid balance, micro-USD
  payout_network TEXT NOT NULL DEFAULT '',
  payout_address TEXT NOT NULL DEFAULT '',
  payout_address_set_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS pair_codes (
  code TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS devices (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  name TEXT NOT NULL,
  token_hash TEXT NOT NULL,
  pubkey TEXT NOT NULL,          -- ed25519, base64, signs receipts
  platform TEXT NOT NULL DEFAULT '',
  class TEXT NOT NULL DEFAULT 'unrated',
  privacy_level TEXT NOT NULL DEFAULT 'sandboxed',
  dependability REAL NOT NULL DEFAULT 70,
  streak INTEGER NOT NULL DEFAULT 0,
  suspended_until INTEGER NOT NULL DEFAULT 0,
  bench TEXT NOT NULL DEFAULT '{}',
  models TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  last_seen INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  customer_id TEXT NOT NULL,
  device_id TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL,
  route TEXT NOT NULL,           -- device | fallback
  status TEXT NOT NULL,          -- running | done | aborted | failed
  tokens_in INTEGER NOT NULL DEFAULT 0,
  tokens_out INTEGER NOT NULL DEFAULT 0,
  charge INTEGER NOT NULL DEFAULT 0,
  owner_pay INTEGER NOT NULL DEFAULT 0,
  abort_cause TEXT NOT NULL DEFAULT '',
  receipt TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL,
  finished_at INTEGER NOT NULL DEFAULT 0
);
-- Every money or score change is an entry, so balances can be rebuilt from history.
CREATE TABLE IF NOT EXISTS ledger (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id TEXT NOT NULL,
  device_id TEXT NOT NULL DEFAULT '',
  job_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL,            -- earning | debit | payout | charge | credit_grant
  amount INTEGER NOT NULL,       -- micro-USD, signed
  release_at INTEGER NOT NULL DEFAULT 0,
  note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS score_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  device_id TEXT NOT NULL,
  job_id TEXT NOT NULL DEFAULT '',
  cause TEXT NOT NULL,           -- completed | owner_action | not_delivered | device_failure | wrong_result | no_fault | suspension | streak_bonus
  delta REAL NOT NULL,
  evidence TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS payouts (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL,
  amount INTEGER NOT NULL,
  network TEXT NOT NULL,
  address TEXT NOT NULL,
  status TEXT NOT NULL,          -- pending | sent
  tx_hash TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS stripe_payments (
  session_id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL,
  amount INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ledger_account ON ledger(account_id);
CREATE INDEX IF NOT EXISTS score_device ON score_events(device_id);
`

// OpenStore opens Postgres when path is a postgres:// URL, otherwise a SQLite file.
func OpenStore(path string) (*Store, error) {
	if strings.HasPrefix(path, "postgres://") || strings.HasPrefix(path, "postgresql://") {
		db, err := sql.Open("pgx", path)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(10)
		var perr error
		for i := 0; i < 30; i++ { // the database may still be starting
			if perr = db.Ping(); perr == nil {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if perr != nil {
			return nil, fmt.Errorf("postgres: %w", perr)
		}
		pgSchema := strings.NewReplacer("INTEGER PRIMARY KEY AUTOINCREMENT", "BIGSERIAL PRIMARY KEY", "INTEGER", "BIGINT", "REAL", "DOUBLE PRECISION").Replace(schema)
		if _, err := db.Exec(pgSchema); err != nil {
			return nil, fmt.Errorf("schema: %w", err)
		}
		db.Exec(`ALTER TABLE score_events ADD COLUMN IF NOT EXISTS decayed_30 BIGINT NOT NULL DEFAULT 0`)
		db.Exec(`ALTER TABLE score_events ADD COLUMN IF NOT EXISTS decayed_90 BIGINT NOT NULL DEFAULT 0`)
		db.Exec(`ALTER TABLE devices ADD COLUMN IF NOT EXISTS live_tps DOUBLE PRECISION NOT NULL DEFAULT 0`)
		db.Exec(`ALTER TABLE devices ADD COLUMN IF NOT EXISTS live_n BIGINT NOT NULL DEFAULT 0`)
		st := &Store{db: db, pg: true}
		if err := st.MigrateBatch(); err != nil {
			return nil, err
		}
		return st, st.MigrateGrowth()
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	// Migrations for databases created by earlier builds (errors mean the column exists).
	db.Exec(`ALTER TABLE score_events ADD COLUMN decayed_30 INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE score_events ADD COLUMN decayed_90 INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE devices ADD COLUMN live_tps REAL NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE devices ADD COLUMN live_n INTEGER NOT NULL DEFAULT 0`)
	st := &Store{db: db}
	if err := st.MigrateBatch(); err != nil {
		return nil, err
	}
	return st, st.MigrateGrowth()
}

func now() int64 { return time.Now().Unix() }

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func hashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

type Account struct {
	ID, Email, Kind              string
	Credits                      int64
	PayoutNetwork, PayoutAddress string
	PayoutAddressSetAt           int64
	CreatedAt                    int64
}

func (s *Store) CreateAccount(email, kind string, credits int64) (*Account, string, error) {
	if kind != "owner" && kind != "customer" {
		return nil, "", errors.New("kind must be owner or customer")
	}
	prefix := "ik_cust_"
	if kind == "owner" {
		prefix = "ik_own_"
	}
	key := prefix + randomHex(24)
	a := &Account{ID: "acc_" + randomHex(8), Email: strings.TrimSpace(email), Kind: kind, Credits: credits, CreatedAt: now()}
	_, err := s.exec(`INSERT INTO accounts(id,email,kind,key_hash,credits,created_at) VALUES(?,?,?,?,?,?)`,
		a.ID, a.Email, a.Kind, hashSecret(key), a.Credits, a.CreatedAt)
	if err != nil {
		return nil, "", err
	}
	if credits > 0 {
		s.AddLedger(a.ID, "", "", "credit_grant", credits, 0, "beta starter credits")
	}
	return a, key, nil
}

func (s *Store) AccountByKey(key string) (*Account, error) {
	a := &Account{}
	err := s.queryRow(`SELECT id,email,kind,credits,payout_network,payout_address,payout_address_set_at,created_at FROM accounts WHERE key_hash=?`, hashSecret(key)).
		Scan(&a.ID, &a.Email, &a.Kind, &a.Credits, &a.PayoutNetwork, &a.PayoutAddress, &a.PayoutAddressSetAt, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (s *Store) AccountByID(id string) (*Account, error) {
	a := &Account{}
	err := s.queryRow(`SELECT id,email,kind,credits,payout_network,payout_address,payout_address_set_at,created_at FROM accounts WHERE id=?`, id).
		Scan(&a.ID, &a.Email, &a.Kind, &a.Credits, &a.PayoutNetwork, &a.PayoutAddress, &a.PayoutAddressSetAt, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (s *Store) AdjustCredits(accountID string, delta int64) error {
	_, err := s.exec(`UPDATE accounts SET credits=credits+? WHERE id=?`, delta, accountID)
	return err
}

// CreditFromStripe adds paid credits once per Checkout Session (webhooks can be delivered twice).
func (s *Store) CreditFromStripe(sessionID, accountID string, amount int64) error {
	if _, err := s.AccountByID(accountID); err != nil {
		return fmt.Errorf("unknown account %s", accountID)
	}
	res, err := s.exec(`INSERT INTO stripe_payments(session_id,account_id,amount,created_at) VALUES(?,?,?,?) ON CONFLICT (session_id) DO NOTHING`, sessionID, accountID, amount, now())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil // already credited
	}
	if err := s.AdjustCredits(accountID, amount); err != nil {
		return err
	}
	return s.AddLedger(accountID, "", "", "credit_purchase", amount, 0, "Stripe "+sessionID)
}

func (s *Store) SetPayoutAddress(accountID, network, address string) error {
	_, err := s.exec(`UPDATE accounts SET payout_network=?, payout_address=?, payout_address_set_at=? WHERE id=?`, network, address, now(), accountID)
	return err
}

func (s *Store) CreatePairCode(ownerID string) (string, error) {
	b := make([]byte, 4)
	rand.Read(b)
	code := fmt.Sprintf("%06d", (uint32(b[0])<<24|uint32(b[1])<<16|uint32(b[2])<<8|uint32(b[3]))%1000000)
	_, err := s.exec(`INSERT INTO pair_codes(code,owner_id,expires_at) VALUES(?,?,?) ON CONFLICT (code) DO UPDATE SET owner_id=excluded.owner_id, expires_at=excluded.expires_at`, code, ownerID, now()+15*60)
	return code, err
}

// ConsumePairCode registers a new device for the code's owner and returns the device id and secret token.
func (s *Store) ConsumePairCode(code, name, pubkey, platform string) (string, string, error) {
	var ownerID string
	var exp int64
	err := s.queryRow(`SELECT owner_id, expires_at FROM pair_codes WHERE code=?`, code).Scan(&ownerID, &exp)
	if err != nil || exp < now() {
		return "", "", errors.New("pairing code is wrong or expired; create a new one on the dashboard")
	}
	s.exec(`DELETE FROM pair_codes WHERE code=?`, code)
	id := "dev_" + randomHex(8)
	token := "idt_" + randomHex(24)
	_, err = s.exec(`INSERT INTO devices(id,owner_id,name,token_hash,pubkey,platform,created_at) VALUES(?,?,?,?,?,?,?)`,
		id, ownerID, name, hashSecret(token), pubkey, platform, now())
	return id, token, err
}

type DeviceRow struct {
	ID, OwnerID, Name, Pubkey, Platform, Class, PrivacyLevel string
	Dependability                                            float64
	Streak                                                   int
	SuspendedUntil, CreatedAt, LastSeen                      int64
	Bench, Models                                            string
}

const deviceCols = `id,owner_id,name,pubkey,platform,class,privacy_level,dependability,streak,suspended_until,created_at,last_seen,bench,models`

func scanDevice(sc interface{ Scan(...any) error }) (*DeviceRow, error) {
	d := &DeviceRow{}
	err := sc.Scan(&d.ID, &d.OwnerID, &d.Name, &d.Pubkey, &d.Platform, &d.Class, &d.PrivacyLevel, &d.Dependability, &d.Streak, &d.SuspendedUntil, &d.CreatedAt, &d.LastSeen, &d.Bench, &d.Models)
	return d, err
}

func (s *Store) DeviceAuth(id, token string) (*DeviceRow, error) {
	row := s.queryRow(`SELECT `+deviceCols+` FROM devices WHERE id=? AND token_hash=?`, id, hashSecret(token))
	return scanDevice(row)
}

func (s *Store) Device(id string) (*DeviceRow, error) {
	return scanDevice(s.queryRow(`SELECT `+deviceCols+` FROM devices WHERE id=?`, id))
}

func (s *Store) DevicesByOwner(ownerID string) ([]*DeviceRow, error) {
	rows, err := s.query(`SELECT `+deviceCols+` FROM devices WHERE owner_id=? ORDER BY created_at`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DeviceRow
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *Store) UpdateDeviceProfile(id, platform, class, privacy, bench, models string) error {
	_, err := s.exec(`UPDATE devices SET platform=?, class=?, privacy_level=?, bench=?, models=?, last_seen=? WHERE id=?`,
		platform, class, privacy, bench, models, now(), id)
	return err
}

func (s *Store) TouchDevice(id string) {
	s.exec(`UPDATE devices SET last_seen=? WHERE id=?`, now(), id)
}

func (s *Store) SetDeviceScore(id string, score float64, streak int, suspendedUntil int64) error {
	_, err := s.exec(`UPDATE devices SET dependability=?, streak=?, suspended_until=? WHERE id=?`, score, streak, suspendedUntil, id)
	return err
}

func (s *Store) AddLedger(accountID, deviceID, jobID, kind string, amount, releaseAt int64, note string) error {
	_, err := s.exec(`INSERT INTO ledger(account_id,device_id,job_id,kind,amount,release_at,note,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		accountID, deviceID, jobID, kind, amount, releaseAt, note, now())
	return err
}

func (s *Store) AddScoreEvent(deviceID, jobID, cause string, delta float64, evidence string) error {
	_, err := s.exec(`INSERT INTO score_events(device_id,job_id,cause,delta,evidence,created_at) VALUES(?,?,?,?,?,?)`,
		deviceID, jobID, cause, delta, evidence, now())
	return err
}

func (s *Store) CountScoreEvents(deviceID, cause string, since int64) int {
	var n int
	s.queryRow(`SELECT COUNT(*) FROM score_events WHERE device_id=? AND cause=? AND created_at>=?`, deviceID, cause, since).Scan(&n)
	return n
}

func (s *Store) SumScoreDelta(deviceID, cause string, since int64) float64 {
	var v sql.NullFloat64
	s.queryRow(`SELECT CAST(SUM(delta) AS DOUBLE PRECISION) FROM score_events WHERE device_id=? AND cause=? AND created_at>=?`, deviceID, cause, since).Scan(&v)
	return v.Float64
}

type Balance struct {
	Available int64 `json:"available"` // released earnings minus debits and payouts
	Held      int64 `json:"held"`      // earnings still inside the hold window
	Paid      int64 `json:"paid"`
	Penalties int64 `json:"penalties"`
}

func (s *Store) OwnerBalance(accountID string) Balance {
	var b Balance
	t := now()
	s.queryRow(`SELECT
	  CAST(COALESCE(SUM(CASE WHEN kind='earning' AND release_at<=? THEN amount
	                    WHEN kind IN ('debit','payout') THEN amount ELSE 0 END),0) AS BIGINT),
	  CAST(COALESCE(SUM(CASE WHEN kind='earning' AND release_at>? THEN amount ELSE 0 END),0) AS BIGINT),
	  CAST(COALESCE(SUM(CASE WHEN kind='payout' THEN -amount ELSE 0 END),0) AS BIGINT),
	  CAST(COALESCE(SUM(CASE WHEN kind='debit' THEN -amount ELSE 0 END),0) AS BIGINT)
	  FROM ledger WHERE account_id=?`, t, t, accountID).Scan(&b.Available, &b.Held, &b.Paid, &b.Penalties)
	return b
}

type LedgerEntry struct {
	Kind      string `json:"kind"`
	Amount    int64  `json:"amount"`
	JobID     string `json:"job_id"`
	DeviceID  string `json:"device_id"`
	ReleaseAt int64  `json:"release_at"`
	Note      string `json:"note"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Store) RecentLedger(accountID string, limit int) []LedgerEntry {
	rows, err := s.query(`SELECT kind,amount,job_id,device_id,release_at,note,created_at FROM ledger WHERE account_id=? ORDER BY id DESC LIMIT ?`, accountID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		rows.Scan(&e.Kind, &e.Amount, &e.JobID, &e.DeviceID, &e.ReleaseAt, &e.Note, &e.CreatedAt)
		out = append(out, e)
	}
	return out
}

type ScoreEvent struct {
	DeviceID  string  `json:"device_id"`
	JobID     string  `json:"job_id"`
	Cause     string  `json:"cause"`
	Delta     float64 `json:"delta"`
	Evidence  string  `json:"evidence"`
	CreatedAt int64   `json:"created_at"`
}

func (s *Store) RecentScoreEvents(ownerID string, limit int) []ScoreEvent {
	rows, err := s.query(`SELECT e.device_id,e.job_id,e.cause,e.delta,e.evidence,e.created_at FROM score_events e
	  JOIN devices d ON d.id=e.device_id WHERE d.owner_id=? AND e.cause NOT IN ('completed','canary_pass','rerun_match') ORDER BY e.id DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ScoreEvent
	for rows.Next() {
		var e ScoreEvent
		rows.Scan(&e.DeviceID, &e.JobID, &e.Cause, &e.Delta, &e.Evidence, &e.CreatedAt)
		out = append(out, e)
	}
	return out
}

func (s *Store) InsertJob(id, customerID, deviceID, model, route string) error {
	_, err := s.exec(`INSERT INTO jobs(id,customer_id,device_id,model,route,status,started_at) VALUES(?,?,?,?,?,?,?)`,
		id, customerID, deviceID, model, route, "running", now())
	return err
}

func (s *Store) FinishJob(id, status string, tin, tout int, charge, ownerPay int64, cause, receipt string) error {
	_, err := s.exec(`UPDATE jobs SET status=?, tokens_in=?, tokens_out=?, charge=?, owner_pay=?, abort_cause=?, receipt=?, finished_at=? WHERE id=?`,
		status, tin, tout, charge, ownerPay, cause, receipt, now(), id)
	return err
}

type JobStats struct {
	Today     int   `json:"jobs_today"`
	EarnToday int64 `json:"earned_today"`
}

func (s *Store) OwnerJobStats(ownerID string) JobStats {
	var st JobStats
	start := time.Now().Truncate(24 * time.Hour).Unix()
	s.queryRow(`SELECT COUNT(*), CAST(COALESCE(SUM(owner_pay),0) AS BIGINT) FROM jobs j JOIN devices d ON d.id=j.device_id
	  WHERE d.owner_id=? AND j.status='done' AND j.finished_at>=?`, ownerID, start).Scan(&st.Today, &st.EarnToday)
	return st
}

// PayoutCandidates lists owners whose available balance is at or above min and whose
// payout address has been set for longer than the 48-hour change hold.
type PayoutLine struct {
	AccountID string `json:"account_id"`
	Email     string `json:"email"`
	Network   string `json:"network"`
	Address   string `json:"address"`
	Amount    int64  `json:"amount"`
}

func (s *Store) PayoutCandidates(min int64) []PayoutLine {
	rows, err := s.query(`SELECT id,email,payout_network,payout_address,payout_address_set_at FROM accounts WHERE kind='owner' AND payout_address!=''`)
	if err != nil {
		return nil
	}
	type acc struct {
		id, email, net, addr string
		setAt                int64
	}
	var accs []acc
	for rows.Next() {
		var a acc
		rows.Scan(&a.id, &a.email, &a.net, &a.addr, &a.setAt)
		accs = append(accs, a)
	}
	rows.Close()
	var out []PayoutLine
	for _, a := range accs {
		if now()-a.setAt < 48*3600 {
			continue
		}
		b := s.OwnerBalance(a.id)
		if b.Available >= min {
			out = append(out, PayoutLine{a.id, a.email, a.net, a.addr, b.Available})
		}
	}
	return out
}

func (s *Store) RecordPayout(accountID string, amount int64, network, address, tx string) error {
	id := "pay_" + randomHex(8)
	if _, err := s.exec(`INSERT INTO payouts(id,account_id,amount,network,address,status,tx_hash,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		id, accountID, amount, network, address, "sent", tx, now()); err != nil {
		return err
	}
	return s.AddLedger(accountID, "", "", "payout", -amount, 0, network+" "+tx)
}
