package main

import (
	"encoding/csv"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Beta growth tools (docs/BETA_LAUNCH_X.md): waitlist with referral codes, public
// leaderboard, network stats, and shareable "my device earned X this week" pages for X.

const growthSchema = `
CREATE TABLE IF NOT EXISTS waitlist (
  code TEXT PRIMARY KEY,          -- the person's own referral code
  email TEXT NOT NULL UNIQUE,
  x_handle TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL,             -- contributor | developer | both
  device TEXT NOT NULL DEFAULT '',
  referred_by TEXT NOT NULL DEFAULT '',
  invited INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS waitlist_ref ON waitlist(referred_by);
`

// Each referral moves you up the queue as if you'd joined a day earlier.
const referralBoost = 86400 * 1000 // waitlist times are in milliseconds

func (s *Store) MigrateGrowth() error {
	q := growthSchema
	if s.pg {
		q = strings.ReplaceAll(q, "INTEGER", "BIGINT")
	}
	_, err := s.db.Exec(q)
	return err
}

type WaitlistEntry struct {
	Code      string `json:"referral_code"`
	Email     string `json:"email,omitempty"`
	XHandle   string `json:"x_handle,omitempty"`
	Kind      string `json:"kind"`
	Device    string `json:"device,omitempty"`
	Referrals int    `json:"referrals"`
	Position  int    `json:"position"`
	Total     int    `json:"total"`
	Invited   bool   `json:"invited"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Store) waitlistEntry(where string, arg any) (*WaitlistEntry, error) {
	e := &WaitlistEntry{}
	var invited int
	err := s.queryRow(`SELECT code,email,x_handle,kind,device,invited,created_at,
	  (SELECT COUNT(*) FROM waitlist r WHERE r.referred_by=w.code) FROM waitlist w WHERE `+where, arg).
		Scan(&e.Code, &e.Email, &e.XHandle, &e.Kind, &e.Device, &invited, &e.CreatedAt, &e.Referrals)
	if err != nil {
		return nil, err
	}
	e.Invited = invited == 1
	score := e.CreatedAt - int64(e.Referrals)*referralBoost
	// Queue order: score (join time minus a day per referral), then join order (rowid-like by created_at, code).
	s.queryRow(`SELECT COUNT(*) FROM (SELECT code, created_at, created_at - ? * (SELECT COUNT(*) FROM waitlist r WHERE r.referred_by=w.code) AS score
	  FROM waitlist w WHERE invited=0) q WHERE q.score < ? OR (q.score = ? AND (q.created_at < ? OR (q.created_at = ? AND q.code < ?)))`,
		referralBoost, score, score, e.CreatedAt, e.CreatedAt, e.Code).Scan(&e.Position)
	e.Position++
	s.queryRow(`SELECT COUNT(*) FROM waitlist WHERE invited=0`).Scan(&e.Total)
	return e, nil
}

var handleRe = regexp.MustCompile(`^@?[A-Za-z0-9_]{1,15}$`)

// POST /api/waitlist {email, x_handle, kind, device, ref}
func (a *API) JoinWaitlist(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, XHandle, Kind, Device, Ref string }
	if readJSON(r, &in) != nil || !strings.Contains(in.Email, "@") || len(in.Email) > 200 {
		apiError(w, 400, "enter a valid email")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if in.Kind != "contributor" && in.Kind != "developer" && in.Kind != "both" {
		in.Kind = "contributor"
	}
	if in.XHandle != "" && !handleRe.MatchString(in.XHandle) {
		apiError(w, 400, "that doesn't look like an X handle")
		return
	}
	in.XHandle = strings.TrimPrefix(in.XHandle, "@")
	if len(in.Device) > 80 {
		in.Device = in.Device[:80]
	}
	if e, err := a.st.waitlistEntry("email=?", in.Email); err == nil {
		writeJSON(w, e) // already on the list: show their spot
		return
	}
	ref := ""
	if in.Ref != "" {
		var x string
		if a.st.queryRow(`SELECT code FROM waitlist WHERE code=?`, strings.ToUpper(in.Ref)).Scan(&x) == nil {
			ref = x
		}
	}
	code := strings.ToUpper(randomHex(3))
	if _, err := a.st.exec(`INSERT INTO waitlist(code,email,x_handle,kind,device,referred_by,created_at) VALUES(?,?,?,?,?,?,?)`,
		code, in.Email, in.XHandle, in.Kind, in.Device, ref, time.Now().UnixMilli()); err != nil {
		apiError(w, 500, "could not join the waitlist")
		return
	}
	e, _ := a.st.waitlistEntry("code=?", code)
	writeJSON(w, e)
}

// GET /api/waitlist/:code
func (a *API) WaitlistStatus(w http.ResponseWriter, r *http.Request) {
	e, err := a.st.waitlistEntry("code=?", strings.ToUpper(r.PathValue("code")))
	if err != nil {
		apiError(w, 404, "no waitlist spot with that code")
		return
	}
	e.Email = ""
	writeJSON(w, e)
}

// GET /api/admin/waitlist?format=csv — queue order
// POST /api/admin/waitlist/invite {count} — marks the next N as invited and returns them
func (a *API) AdminWaitlist(w http.ResponseWriter, r *http.Request) {
	if !a.admin(w, r) {
		return
	}
	limit := 1000000
	if r.Method == http.MethodPost {
		var in struct {
			Count int `json:"count"`
		}
		readJSON(r, &in)
		if in.Count <= 0 || in.Count > 1000 {
			apiError(w, 400, "count must be 1–1000")
			return
		}
		limit = in.Count
	}
	rows, err := a.st.query(`SELECT code,email,x_handle,kind,device,created_at,
	  (SELECT COUNT(*) FROM waitlist r WHERE r.referred_by=w.code) AS refs FROM waitlist w WHERE invited=0
	  ORDER BY created_at - ? * (SELECT COUNT(*) FROM waitlist r WHERE r.referred_by=w.code), created_at, code LIMIT ?`, referralBoost, limit)
	if err != nil {
		apiError(w, 500, err.Error())
		return
	}
	var list []WaitlistEntry
	for rows.Next() {
		var e WaitlistEntry
		rows.Scan(&e.Code, &e.Email, &e.XHandle, &e.Kind, &e.Device, &e.CreatedAt, &e.Referrals)
		e.Position = len(list) + 1
		list = append(list, e)
	}
	rows.Close()
	if r.Method == http.MethodPost {
		for _, e := range list {
			a.st.exec(`UPDATE waitlist SET invited=1 WHERE code=?`, e.Code)
		}
	}
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		cw := csv.NewWriter(w)
		cw.Write([]string{"position", "email", "x_handle", "kind", "device", "referrals", "code"})
		for _, e := range list {
			cw.Write([]string{strconv.Itoa(e.Position), e.Email, e.XHandle, e.Kind, e.Device, strconv.Itoa(e.Referrals), e.Code})
		}
		cw.Flush()
		return
	}
	writeJSON(w, map[string]any{"entries": list, "invite_code": a.inviteCode})
}

type LeaderRow struct {
	Rank     int     `json:"rank"`
	Device   string  `json:"device"`
	Class    string  `json:"class"`
	Jobs     int     `json:"jobs"`
	Tokens   int64   `json:"tokens"`
	Earned   int64   `json:"earned"`
	Score    float64 `json:"dependability"`
	DeviceID string  `json:"-"`
}

// leaderboard ranks devices by verified jobs in the last `days` days. Device names are not
// shown (they are often a person's computer name); each device gets a short public tag.
func (s *Store) leaderboard(days, limit int) []LeaderRow {
	rows, err := s.query(`SELECT d.id, d.class, d.dependability, COUNT(j.id),
	  CAST(COALESCE(SUM(j.tokens_in + j.tokens_out),0) AS BIGINT), CAST(COALESCE(SUM(j.owner_pay),0) AS BIGINT)
	  FROM jobs j JOIN devices d ON d.id=j.device_id
	  WHERE j.status='done' AND j.route='device' AND j.finished_at>=?
	  GROUP BY d.id, d.class, d.dependability ORDER BY COUNT(j.id) DESC LIMIT ?`, now()-int64(days)*86400, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []LeaderRow
	for rows.Next() {
		var r LeaderRow
		rows.Scan(&r.DeviceID, &r.Class, &r.Score, &r.Jobs, &r.Tokens, &r.Earned)
		r.Rank = len(out) + 1
		r.Device = publicTag(r.DeviceID)
		out = append(out, r)
	}
	return out
}

func publicTag(deviceID string) string {
	id := strings.TrimPrefix(deviceID, "dev_")
	if len(id) > 6 {
		id = id[:6]
	}
	return "node-" + id
}

// GET /api/leaderboard?days=7
func (a *API) Leaderboard(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 90 {
		days = 7
	}
	writeJSON(w, map[string]any{"days": days, "rows": a.st.leaderboard(days, 100)})
}

type NetStats struct {
	JobsToday   int   `json:"jobs_today"`
	TokensToday int64 `json:"tokens_today"`
	Devices     int   `json:"devices_registered"`
	Waitlist    int   `json:"waitlist"`
}

func (s *Store) netStats() NetStats {
	var n NetStats
	start := time.Now().Truncate(24 * time.Hour).Unix()
	s.queryRow(`SELECT COUNT(*), CAST(COALESCE(SUM(tokens_in+tokens_out),0) AS BIGINT) FROM jobs WHERE status='done' AND finished_at>=?`, start).Scan(&n.JobsToday, &n.TokensToday)
	s.queryRow(`SELECT COUNT(*) FROM devices`).Scan(&n.Devices)
	s.queryRow(`SELECT COUNT(*) FROM waitlist`).Scan(&n.Waitlist)
	return n
}

// GET /s/:tag — public share page for X ("my device earned X this week"). The tag is the
// device's public tag; the page shows only that device's last-7-day jobs and earnings.
func (a *API) SharePage(w http.ResponseWriter, r *http.Request) {
	tag := r.PathValue("tag")
	var row *LeaderRow
	for _, l := range a.st.leaderboard(7, 100000) {
		if l.Device == tag {
			l := l
			row = &l
			break
		}
	}
	title := "Infera: earn from your idle laptop, desktop or phone"
	desc := "Install once. Your device serves AI jobs while you're away and earns USDT."
	if row != nil {
		title = fmt.Sprintf("My device earned $%.2f this week serving AI on Infera", float64(row.Earned)/microUSD)
		desc = fmt.Sprintf("%s · %d jobs · rank #%d · class %s. Join the beta and put your idle devices to work.", row.Device, row.Jobs, row.Rank, row.Class)
	}
	ref := html.EscapeString(r.URL.Query().Get("ref"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>%[1]s</title>
<meta property="og:title" content="%[1]s"><meta property="og:description" content="%[2]s">
<meta name="twitter:card" content="summary"><meta name="twitter:title" content="%[1]s"><meta name="twitter:description" content="%[2]s">
<meta http-equiv="refresh" content="2;url=/?ref=%[3]s#home">
<style>body{font-family:system-ui,sans-serif;background:#0e1412;color:#e3ebe7;display:grid;place-items:center;min-height:100vh;margin:0;padding:16px;text-align:center}
h1{font-size:1.6rem;max-width:30ch}p{color:#9aaba3}a{color:#4cc398}</style></head>
<body><div><h1>%[1]s</h1><p>%[2]s</p><p><a href="/?ref=%[3]s#home">Join the Infera beta →</a></p></div></body></html>`,
		html.EscapeString(title), html.EscapeString(desc), ref)
}
