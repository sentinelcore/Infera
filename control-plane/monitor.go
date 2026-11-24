package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

// Monitoring (GAPS G15): Prometheus-style /metrics, a public /status page, and alerts to a
// webhook (Slack or Discord incoming-webhook URL in INFERA_ALERT_WEBHOOK) on crashes and
// on queues nobody can serve.

type Monitor struct {
	start    time.Time
	mu       sync.Mutex
	requests map[string]int64 // "route code" -> count
	jobs     map[string]int64 // "done" / "aborted:<cause>" -> count
	latSum   float64
	latN     int64
	events   []string // recent incidents, newest first
	lastSent map[string]time.Time
	webhook  string
}

func NewMonitor() *Monitor {
	return &Monitor{start: time.Now(), requests: map[string]int64{}, jobs: map[string]int64{}, lastSent: map[string]time.Time{}, webhook: os.Getenv("INFERA_ALERT_WEBHOOK")}
}

var mon = NewMonitor()

func (m *Monitor) Job(outcome string, d time.Duration) {
	m.mu.Lock()
	m.jobs[outcome]++
	if outcome == "done" {
		m.latSum += d.Seconds()
		m.latN++
	}
	m.mu.Unlock()
}

// Incident records an event for the status page and, at most once per 10 minutes per kind,
// posts it to the alert webhook.
func (m *Monitor) Incident(kind, msg string) {
	line := time.Now().UTC().Format("2006-01-02 15:04") + " UTC · " + msg
	m.mu.Lock()
	m.events = append([]string{line}, m.events...)
	if len(m.events) > 20 {
		m.events = m.events[:20]
	}
	send := m.webhook != "" && time.Since(m.lastSent[kind]) > 10*time.Minute
	if send {
		m.lastSent[kind] = time.Now()
	}
	m.mu.Unlock()
	log.Printf("incident %s: %s", kind, msg)
	if send {
		go func() {
			b, _ := json.Marshal(map[string]string{"text": "Infera: " + msg, "content": "Infera: " + msg})
			http.Post(m.webhook, "application/json", bytes.NewReader(b))
		}()
	}
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Hijack lets device WebSocket connections through the wrapper.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		s.code = 101
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("hijack not supported")
}

// Wrap counts requests by route and status and turns panics into a 500 plus an alert.
func (m *Monitor) Wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		defer func() {
			if p := recover(); p != nil {
				m.Incident("panic", fmt.Sprintf("server error on %s %s: %v", r.Method, r.URL.Path, p))
				log.Printf("panic: %v\n%s", p, debug.Stack())
				http.Error(rec, "internal error", 500)
			}
			route := r.URL.Path
			for _, p := range []string{"/v1/batch/", "/api/waitlist/", "/s/", "/pair/"} {
				if strings.HasPrefix(route, p) {
					route = p + "*"
				}
			}
			if strings.HasPrefix(route, "/v1/") || strings.HasPrefix(route, "/api/") || strings.HasPrefix(route, "/ws/") {
				m.mu.Lock()
				m.requests[fmt.Sprintf("%s %d", route, rec.code)]++
				m.mu.Unlock()
			}
		}()
		h.ServeHTTP(rec, r)
	})
}

// Watch raises an alert when batch jobs wait for a model with no device online for it.
func (m *Monitor) Watch(g *Gateway) {
	for {
		time.Sleep(time.Minute)
		online := g.hub.OnlineModels()
		rows, err := g.st.query(`SELECT model, COUNT(*) FROM batch_jobs WHERE status='queued' AND created_at<? GROUP BY model`, now()-15*60)
		if err != nil {
			continue
		}
		for rows.Next() {
			var model string
			var n int
			rows.Scan(&model, &n)
			if online[model] == 0 {
				m.Incident("starved:"+model, fmt.Sprintf("%d batch jobs for %s have waited over 15 minutes and no device serving it is online", n, model))
			}
		}
		rows.Close()
	}
}

type statusData struct {
	UptimeHours   float64        `json:"uptime_hours"`
	DevicesOnline int            `json:"devices_online"`
	Models        map[string]int `json:"models_online"`
	JobsDone      int64          `json:"jobs_done_since_start"`
	JobsAborted   int64          `json:"jobs_aborted_since_start"`
	AvgJobSeconds float64        `json:"avg_job_seconds"`
	QueueDepth    int            `json:"batch_queued"`
	Today         NetStats       `json:"today"`
	Incidents     []string       `json:"recent_incidents"`
	State         string         `json:"state"`
}

func (m *Monitor) status(g *Gateway) statusData {
	s := statusData{UptimeHours: time.Since(m.start).Hours(), Models: g.hub.OnlineModels(), Today: g.st.netStats()}
	for _, n := range s.Models {
		_ = n
	}
	g.hub.mu.Lock()
	for _, n := range g.hub.nodes {
		if n.Online {
			s.DevicesOnline++
		}
	}
	g.hub.mu.Unlock()
	g.st.queryRow(`SELECT COUNT(*) FROM batch_jobs WHERE status='queued'`).Scan(&s.QueueDepth)
	m.mu.Lock()
	for k, v := range m.jobs {
		if k == "done" {
			s.JobsDone += v
		} else {
			s.JobsAborted += v
		}
	}
	if m.latN > 0 {
		s.AvgJobSeconds = m.latSum / float64(m.latN)
	}
	s.Incidents = append([]string{}, m.events...)
	m.mu.Unlock()
	s.State = "operational"
	if s.DevicesOnline == 0 {
		s.State = "degraded: no devices online"
	}
	return s
}

// GET /metrics — Prometheus text format.
func (m *Monitor) Metrics(g *Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := m.status(g)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "infera_up 1\ninfera_uptime_seconds %.0f\ninfera_devices_online %d\ninfera_devices_registered %d\ninfera_batch_queued %d\n",
			time.Since(m.start).Seconds(), s.DevicesOnline, s.Today.Devices, s.QueueDepth)
		models := make([]string, 0, len(s.Models))
		for k := range s.Models {
			models = append(models, k)
		}
		sort.Strings(models)
		for _, k := range models {
			fmt.Fprintf(w, "infera_model_devices_online{model=%q} %d\n", k, s.Models[k])
		}
		m.mu.Lock()
		for k, v := range m.jobs {
			outcome, cause, _ := strings.Cut(k, ":")
			fmt.Fprintf(w, "infera_jobs_total{outcome=%q,cause=%q} %d\n", outcome, cause, v)
		}
		fmt.Fprintf(w, "infera_job_seconds_sum %.3f\ninfera_job_seconds_count %d\n", m.latSum, m.latN)
		for k, v := range m.requests {
			route, code, _ := strings.Cut(k, " ")
			fmt.Fprintf(w, "infera_http_requests_total{route=%q,code=%q} %d\n", route, code, v)
		}
		m.mu.Unlock()
	}
}

// GET /api/status (JSON) and GET /status (page).
func (m *Monitor) StatusJSON(g *Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { writeJSON(w, m.status(g)) }
}

func (m *Monitor) StatusPage(g *Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := m.status(g)
		color := "#4cc398"
		if s.State != "operational" {
			color = "#e6ae45"
		}
		var models strings.Builder
		for k, v := range s.Models {
			fmt.Fprintf(&models, "<tr><td>%s</td><td>%d</td></tr>", htmlEsc(k), v)
		}
		if models.Len() == 0 {
			models.WriteString(`<tr><td colspan="2">No devices online right now</td></tr>`)
		}
		var inc strings.Builder
		for _, e := range s.Incidents {
			fmt.Fprintf(&inc, "<li>%s</li>", htmlEsc(e))
		}
		if inc.Len() == 0 {
			inc.WriteString("<li>No incidents since the last restart.</li>")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="refresh" content="30"><title>Infera status</title>
<style>body{font-family:system-ui,sans-serif;background:#0e1412;color:#e3ebe7;margin:0;padding:24px max(16px,env(safe-area-inset-left))}main{max-width:760px;margin:0 auto}
.s{display:inline-block;padding:6px 14px;border-radius:999px;background:%s;color:#0e1412;font-weight:700}table{width:100%%;border-collapse:collapse;margin-top:8px}td{padding:6px 0;border-bottom:1px solid #2f3e37}
.g{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:10px;margin:18px 0}.c{background:#161f1b;border:1px solid #2f3e37;border-radius:10px;padding:12px}.k{color:#9aaba3;font-size:.8rem}.v{font-size:1.4rem;font-weight:700}a{color:#4cc398}</style></head>
<body><main><h1>Infera status</h1><span class="s">%s</span>
<div class="g"><div class="c"><div class="k">Devices online</div><div class="v">%d</div></div><div class="c"><div class="k">Jobs today</div><div class="v">%d</div></div>
<div class="c"><div class="k">Batch jobs waiting</div><div class="v">%d</div></div><div class="c"><div class="k">Avg job time</div><div class="v">%.1fs</div></div></div>
<h2>Models online</h2><table>%s</table><h2>Recent incidents</h2><ul>%s</ul><p style="color:#9aaba3">Up %.1f hours · refreshes every 30 s · <a href="/">Infera</a></p></main></body></html>`,
			color, htmlEsc(s.State), s.DevicesOnline, s.Today.JobsToday, s.QueueDepth, s.AvgJobSeconds, models.String(), inc.String(), s.UptimeHours)
	}
}

func htmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}
