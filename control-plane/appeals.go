package main

import (
	"fmt"
	"net/http"
	"strings"
)

// Appeals (ARCHITECTURE §3.4): an owner can appeal any penalty, suspension or removal with one
// tap from the dashboard. An operator reviews it; an upheld appeal gives back the score points,
// refunds the debit for that job and lifts a suspension or removal.
//
//   POST /api/owner/appeal          {event_id, message}        (owner key)
//   GET  /api/admin/appeals                                    (admin token) open appeals
//   POST /api/admin/appeals/resolve {id, accept, note}         (admin token)

const appealSchema = `
CREATE TABLE IF NOT EXISTS appeals (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id INTEGER NOT NULL UNIQUE,
  device_id TEXT NOT NULL,
  owner_id TEXT NOT NULL,
  message TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open',  -- open | upheld | rejected
  note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  resolved_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS appeals_status ON appeals(status, created_at);
`

// Penalties that can be appealed, within 14 days.
var appealable = map[string]bool{CauseOwnerAction: true, CauseNotDelivered: true, CauseDeviceFailure: true,
	CauseWrongResult: true, "suspension": true, "removed": true}

const appealWindow = 14 * 24 * 3600

// removedUntil marks a device removed from the network (3 suspensions in 30 days).
const removedUntil int64 = 253402300799 // 9999-12-31

func (s *Store) MigrateAppeals() error {
	q := appealSchema
	if s.pg {
		q = strings.NewReplacer("INTEGER PRIMARY KEY AUTOINCREMENT", "BIGSERIAL PRIMARY KEY", "INTEGER", "BIGINT").Replace(q)
	}
	_, err := s.db.Exec(q)
	return err
}

type Appeal struct {
	ID         int64   `json:"id"`
	EventID    int64   `json:"event_id"`
	DeviceID   string  `json:"device_id"`
	OwnerID    string  `json:"owner_id"`
	Message    string  `json:"message"`
	Status     string  `json:"status"`
	Note       string  `json:"note"`
	CreatedAt  int64   `json:"created_at"`
	Cause      string  `json:"cause"`
	Delta      float64 `json:"delta"`
	Evidence   string  `json:"evidence"`
	JobID      string  `json:"job_id"`
	ResolvedAt int64   `json:"resolved_at,omitempty"`
}

const appealCols = `a.id,a.event_id,a.device_id,a.owner_id,a.message,a.status,a.note,a.created_at,a.resolved_at,e.cause,e.delta,e.evidence,e.job_id`

func scanAppeal(sc interface{ Scan(...any) error }) (Appeal, error) {
	var a Appeal
	err := sc.Scan(&a.ID, &a.EventID, &a.DeviceID, &a.OwnerID, &a.Message, &a.Status, &a.Note, &a.CreatedAt, &a.ResolvedAt, &a.Cause, &a.Delta, &a.Evidence, &a.JobID)
	return a, err
}

func (s *Store) OpenAppeals() []Appeal {
	rows, err := s.query(`SELECT ` + appealCols + ` FROM appeals a JOIN score_events e ON e.id=a.event_id WHERE a.status='open' ORDER BY a.id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Appeal
	for rows.Next() {
		if a, err := scanAppeal(rows); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// FileAppeal checks the event belongs to one of the owner's devices and is appealable.
func (s *Store) FileAppeal(ownerID string, eventID int64, message string) error {
	var devID, cause string
	var created int64
	err := s.queryRow(`SELECT e.device_id,e.cause,e.created_at FROM score_events e JOIN devices d ON d.id=e.device_id
	  WHERE e.id=? AND d.owner_id=?`, eventID, ownerID).Scan(&devID, &cause, &created)
	if err != nil {
		return fmt.Errorf("no such penalty on your devices")
	}
	if !appealable[cause] {
		return fmt.Errorf("only penalties, suspensions and removals can be appealed")
	}
	if now()-created > appealWindow {
		return fmt.Errorf("appeals must be filed within 14 days")
	}
	if len(message) > 2000 {
		message = message[:2000]
	}
	if _, err := s.exec(`INSERT INTO appeals(event_id,device_id,owner_id,message,created_at) VALUES(?,?,?,?,?)`,
		eventID, devID, ownerID, message, now()); err != nil {
		return fmt.Errorf("this penalty has already been appealed")
	}
	return nil
}

// ResolveAppeal closes an open appeal. Upheld: score points back, the job's debit refunded,
// and a suspension or removal lifted.
func (s *Store) ResolveAppeal(id int64, accept bool, note string) (*Appeal, error) {
	a, err := scanAppeal(s.queryRow(`SELECT `+appealCols+` FROM appeals a JOIN score_events e ON e.id=a.event_id WHERE a.id=?`, id))
	if err != nil {
		return nil, fmt.Errorf("no such appeal")
	}
	if a.Status != "open" {
		return nil, fmt.Errorf("appeal already %s", a.Status)
	}
	status := "rejected"
	if accept {
		status = "upheld"
	}
	res, err := s.exec(`UPDATE appeals SET status=?, note=?, resolved_at=? WHERE id=? AND status='open'`, status, note, now(), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("appeal already resolved")
	}
	a.Status, a.Note = status, note
	if !accept {
		return &a, nil
	}
	dev, err := s.Device(a.DeviceID)
	if err != nil {
		return &a, nil
	}
	score := clamp(dev.Dependability-a.Delta, 0, 100)
	until := dev.SuspendedUntil
	if a.Cause == "suspension" || a.Cause == "removed" {
		until = 0
		if score < 50 {
			score = 50
		}
	}
	s.SetDeviceScore(dev.ID, score, dev.Streak, until)
	s.AddScoreEvent(dev.ID, a.JobID, "appeal_upheld", -a.Delta, fmt.Sprintf("appeal #%d upheld: %s", a.ID, note))
	if a.JobID != "" {
		var debited int64
		s.queryRow(`SELECT CAST(COALESCE(SUM(amount),0) AS BIGINT) FROM ledger WHERE device_id=? AND job_id=? AND kind='debit'`, dev.ID, a.JobID).Scan(&debited)
		if debited < 0 {
			s.AddLedger(dev.OwnerID, dev.ID, a.JobID, "debit", -debited, 0, fmt.Sprintf("appeal #%d upheld: debit refunded", a.ID))
		}
	}
	return &a, nil
}

// POST /api/owner/appeal
func (a *API) FileAppeal(w http.ResponseWriter, r *http.Request) {
	acct := a.auth(w, r, "owner")
	if acct == nil {
		return
	}
	var in struct {
		EventID int64  `json:"event_id"`
		Message string `json:"message"`
	}
	if readJSON(r, &in) != nil || in.EventID == 0 {
		apiError(w, 400, "event_id is required")
		return
	}
	if err := a.st.FileAppeal(acct.ID, in.EventID, strings.TrimSpace(in.Message)); err != nil {
		apiError(w, 400, err.Error())
		return
	}
	mon.Incident("appeal", fmt.Sprintf("New appeal from owner %s on penalty #%d", acct.ID, in.EventID))
	writeJSON(w, map[string]any{"ok": true})
}

// GET /api/admin/appeals, POST /api/admin/appeals/resolve
func (a *API) AdminAppeals(w http.ResponseWriter, r *http.Request) {
	if !a.admin(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"appeals": a.st.OpenAppeals()})
		return
	}
	var in struct {
		ID     int64  `json:"id"`
		Accept bool   `json:"accept"`
		Note   string `json:"note"`
	}
	if readJSON(r, &in) != nil || in.ID == 0 {
		apiError(w, 400, "id is required")
		return
	}
	ap, err := a.st.ResolveAppeal(in.ID, in.Accept, in.Note)
	if err != nil {
		apiError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "appeal": ap})
}
