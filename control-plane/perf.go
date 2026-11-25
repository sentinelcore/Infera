package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Public performance numbers per model (what marketplaces and buyers check):
// uptime (share of minutes with at least one device serving it), time to first token
// p50/p95, generation speed, success rate. GET /api/performance, page at /performance.

func (s *Store) MigratePerf() {
	big := "INTEGER"
	if s.pg {
		big = "BIGINT"
	}
	if s.pg {
		s.db.Exec(`ALTER TABLE jobs ADD COLUMN IF NOT EXISTS ttft_ms ` + big + ` NOT NULL DEFAULT 0`)
	} else {
		s.db.Exec(`ALTER TABLE jobs ADD COLUMN ttft_ms INTEGER NOT NULL DEFAULT 0`)
	}
	s.db.Exec(`CREATE TABLE IF NOT EXISTS availability (minute ` + big + ` NOT NULL, model TEXT NOT NULL, devices ` + big + ` NOT NULL, PRIMARY KEY (minute, model))`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS jobs_started ON jobs(started_at)`)
}

func (s *Store) SetJobTTFT(jobID string, ms int64) {
	s.exec(`UPDATE jobs SET ttft_ms=? WHERE id=?`, ms, jobID)
}

// SampleAvailability records, once a minute, how many devices serve each model.
func (h *Hub) SampleAvailability() {
	for {
		minute := time.Now().Unix() / 60
		counts := map[string]int{}
		h.mu.Lock()
		for _, n := range h.nodes {
			n.mu.Lock()
			if n.Online && n.Status != "paused" {
				for _, m := range n.Models {
					counts[m]++
				}
			}
			n.mu.Unlock()
		}
		h.mu.Unlock()
		// Every model ever served gets a row, so minutes with nobody online count as down.
		for _, m := range h.st.KnownModels() {
			if _, ok := counts[m]; !ok {
				counts[m] = 0
			}
		}
		for m, c := range counts {
			h.st.exec(`INSERT INTO availability(minute,model,devices) VALUES(?,?,?) ON CONFLICT (minute, model) DO UPDATE SET devices=excluded.devices`, minute, m, c)
		}
		h.st.exec(`DELETE FROM availability WHERE minute < ?`, minute-31*24*60)
		time.Sleep(time.Until(time.Unix((minute+1)*60, 0)))
	}
}

func (s *Store) KnownModels() []string {
	rows, err := s.query(`SELECT DISTINCT model FROM availability`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		rows.Scan(&m)
		out = append(out, m)
	}
	return out
}

type ModelPerf struct {
	Model         string  `json:"model"`
	UptimePct     float64 `json:"uptime_pct"`
	Requests      int     `json:"requests"`
	SuccessPct    float64 `json:"success_pct"`
	TTFTp50ms     int64   `json:"ttft_p50_ms"`
	TTFTp95ms     int64   `json:"ttft_p95_ms"`
	TokensPerSec  float64 `json:"tokens_per_second"`
	DevicesOnline int     `json:"devices_online"`
}

func pct(v []int64, p float64) int64 {
	if len(v) == 0 {
		return 0
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	i := int(float64(len(v)-1) * p)
	return v[i]
}

// Performance over the last `days` days.
func (s *Store) Performance(days int, online map[string]int) []ModelPerf {
	since := time.Now().Unix() - int64(days)*86400
	perf := map[string]*ModelPerf{}
	get := func(m string) *ModelPerf {
		if perf[m] == nil {
			perf[m] = &ModelPerf{Model: m, DevicesOnline: online[m]}
		}
		return perf[m]
	}
	// Uptime from availability samples.
	if rows, err := s.query(`SELECT model, COUNT(*), CAST(COALESCE(SUM(CASE WHEN devices>0 THEN 1 ELSE 0 END),0) AS BIGINT) FROM availability WHERE minute>=? GROUP BY model`, since/60); err == nil {
		for rows.Next() {
			var m string
			var total, up int64
			rows.Scan(&m, &total, &up)
			if total > 0 {
				get(m).UptimePct = 100 * float64(up) / float64(total)
			}
		}
		rows.Close()
	}
	// Jobs: customer jobs only (canaries and re-runs are internal).
	ttft := map[string][]int64{}
	var genTok, genSec = map[string]float64{}, map[string]float64{}
	ok, bad := map[string]int{}, map[string]int{}
	rows, err := s.query(`SELECT j.model, j.status, j.abort_cause, j.tokens_out, j.ttft_ms, j.started_at, j.finished_at FROM jobs j
	  JOIN accounts a ON a.id=j.customer_id WHERE j.started_at>=? AND a.email NOT LIKE 'canary@%'`, since)
	if err == nil {
		for rows.Next() {
			var m, st, cause string
			var tout, t, st0, st1 int64
			rows.Scan(&m, &st, &cause, &tout, &t, &st0, &st1)
			p := get(m)
			p.Requests++
			switch {
			case st == "done":
				ok[m]++
				if t > 0 {
					ttft[m] = append(ttft[m], t)
				}
				if d := st1 - st0; d > 0 && tout > 0 {
					genTok[m] += float64(tout)
					genSec[m] += float64(d)
				}
			case st == "aborted" && cause != CauseNoFault:
				bad[m]++
			}
		}
		rows.Close()
	}
	var out []ModelPerf
	for m, p := range perf {
		if n := ok[m] + bad[m]; n > 0 {
			p.SuccessPct = 100 * float64(ok[m]) / float64(n)
		}
		p.TTFTp50ms, p.TTFTp95ms = pct(ttft[m], 0.5), pct(ttft[m], 0.95)
		if genSec[m] > 0 {
			p.TokensPerSec = genTok[m] / genSec[m]
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

func (g *Gateway) onlineByModel() map[string]int {
	on := map[string]int{}
	g.hub.mu.Lock()
	defer g.hub.mu.Unlock()
	for _, n := range g.hub.nodes {
		n.mu.Lock()
		if n.Online && n.Status != "paused" {
			for _, m := range n.Models {
				on[m]++
			}
		}
		n.mu.Unlock()
	}
	return on
}

// GET /api/performance?days=7
func (g *Gateway) PerformanceJSON(w http.ResponseWriter, r *http.Request) {
	days := 7
	if d := r.URL.Query().Get("days"); d == "1" || d == "30" {
		fmt.Sscan(d, &days)
	}
	writeJSON(w, map[string]any{"days": days, "fallback_backstop": g.cfg.FallbackURL != "", "models": g.st.Performance(days, g.onlineByModel())})
}

// GET /performance
func (g *Gateway) PerformancePage(w http.ResponseWriter, r *http.Request) {
	rows := g.st.Performance(7, g.onlineByModel())
	var b strings.Builder
	for _, p := range rows {
		ms := func(v int64) string {
			if v == 0 {
				return "–"
			}
			return fmt.Sprintf("%.2f s", float64(v)/1000)
		}
		sp := "–"
		if p.TokensPerSec > 0 {
			sp = fmt.Sprintf("%.1f tok/s", p.TokensPerSec)
		}
		succ := "–"
		if p.Requests > 0 {
			succ = fmt.Sprintf("%.1f%%", p.SuccessPct)
		}
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%.2f%%</td><td>%d</td><td>%s</td><td>%s / %s</td><td>%s</td><td>%d</td></tr>",
			htmlEsc(p.Model), p.UptimePct, p.Requests, succ, ms(p.TTFTp50ms), ms(p.TTFTp95ms), sp, p.DevicesOnline)
	}
	if b.Len() == 0 {
		b.WriteString(`<tr><td colspan="7">No data yet.</td></tr>`)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Infera performance</title>
<style>body{font-family:system-ui,sans-serif;background:#0e1412;color:#e3ebe7;margin:0;padding:24px 16px}main{max-width:900px;margin:0 auto}
.w{overflow-x:auto}table{width:100%%;border-collapse:collapse;min-width:640px}th,td{text-align:left;padding:8px 6px;border-bottom:1px solid #2f3e37;font-variant-numeric:tabular-nums}th{color:#9aaba3;font-size:.8rem;font-weight:600}p{color:#9aaba3}a{color:#4cc398}</style></head>
<body><main><h1>Performance · last 7 days</h1>
<p>Measured on real customer requests. Uptime = share of minutes with at least one device serving the model. Time to first token includes the device accepting the job. JSON: <a href="/api/performance">/api/performance</a>.</p>
<div class="w"><table><tr><th>Model</th><th>Uptime</th><th>Requests</th><th>Success</th><th>First token p50 / p95</th><th>Speed</th><th>Devices now</th></tr>%s</table></div>
<p>%s · <a href="/status">Status</a> · <a href="/privacy">Data &amp; privacy</a> · <a href="/">Infera</a></p></main></body></html>`,
		b.String(), map[bool]string{true: "Requests are backed by a rented-capacity backstop when no device is free.", false: "No backstop configured: requests fail with 503 when no device is free."}[g.cfg.FallbackURL != ""])
}
