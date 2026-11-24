package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
)

// Batch API (ARCHITECTURE.md §6): jobs that can wait are stored in a queue table and run
// whenever a suitable device is idle — phones and slow devices included. Priced with
// INFERA_BATCH_DISCOUNT (default 50% off). Claiming a job is one conditional UPDATE, so
// several server instances can share the queue safely.
//
//   POST /v1/batch            {"requests":[{"model":…,"messages":[…]} | {"model":…,"input":…}, …]}  → 202 + job ids
//   GET  /v1/batch/{id}       → status and result of one job
//   GET  /v1/batch?status=…   → your recent jobs

const batchSchema = `
CREATE TABLE IF NOT EXISTS batch_jobs (
  id TEXT PRIMARY KEY,
  customer_id TEXT NOT NULL,
  kind TEXT NOT NULL,             -- chat | embeddings
  model TEXT NOT NULL,
  request TEXT NOT NULL,
  status TEXT NOT NULL,           -- queued | running | done | failed
  result TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  attempts INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  started_at INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS batch_status ON batch_jobs(status, created_at);
CREATE INDEX IF NOT EXISTS batch_customer ON batch_jobs(customer_id, created_at);
`

const (
	batchMaxPerCall  = 1000
	batchMaxAttempts = 5
	batchExpiry      = 24 * 3600 // seconds a job may wait before it fails
)

func (s *Store) MigrateBatch() error {
	q := batchSchema
	if s.pg {
		q = strings.ReplaceAll(q, "INTEGER", "BIGINT")
	}
	_, err := s.db.Exec(q)
	// Jobs left "running" by a restart go back to the queue.
	s.exec(`UPDATE batch_jobs SET status='queued' WHERE status='running'`)
	return err
}

type batchJob struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Model      string          `json:"model"`
	Status     string          `json:"status"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	Attempts   int             `json:"attempts"`
	CreatedAt  int64           `json:"created_at"`
	FinishedAt int64           `json:"finished_at,omitempty"`
	request    string
	customer   string
}

func (g *Gateway) BatchSubmit(w http.ResponseWriter, r *http.Request) {
	acct, err := g.st.AccountByKey(bearer(r))
	if err != nil || acct.Kind != "customer" {
		apiError(w, 401, "missing or invalid API key")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		apiError(w, 400, "could not read request body")
		return
	}
	var in struct {
		Requests []json.RawMessage `json:"requests"`
	}
	if json.Unmarshal(raw, &in) != nil || len(in.Requests) == 0 {
		apiError(w, 400, `send {"requests":[{"model":…,"messages":[…]}, …]} (or "input" for embeddings)`)
		return
	}
	if len(in.Requests) > batchMaxPerCall {
		apiError(w, 400, fmt.Sprintf("at most %d requests per call", batchMaxPerCall))
		return
	}
	var est int64
	type item struct{ kind, model, body string }
	var items []item
	for i, rq := range in.Requests {
		var p struct {
			Model     string          `json:"model"`
			Messages  json.RawMessage `json:"messages"`
			Input     json.RawMessage `json:"input"`
			MaxTokens int             `json:"max_tokens"`
		}
		if json.Unmarshal(rq, &p) != nil || p.Model == "" || (len(p.Messages) == 0 && len(p.Input) == 0) {
			apiError(w, 400, fmt.Sprintf("request %d needs \"model\" and \"messages\" (chat) or \"input\" (embeddings)", i))
			return
		}
		kind := "chat"
		if len(p.Input) > 0 && len(p.Messages) == 0 {
			kind = "embeddings"
		}
		if p.MaxTokens <= 0 {
			p.MaxTokens = 512
		}
		cr := chatRequest{Model: p.Model, MaxTokens: p.MaxTokens, queued: true}
		if kind == "embeddings" {
			cr.jobType = "embeddings"
		}
		est += g.jobCost(cr, len(rq)/4, p.MaxTokens)
		items = append(items, item{kind, p.Model, string(rq)})
	}
	if acct.Credits < est {
		apiError(w, 402, fmt.Sprintf("not enough credits: this batch can cost up to $%.4f, balance is $%.4f", float64(est)/microUSD, float64(acct.Credits)/microUSD))
		return
	}
	ids := make([]string, 0, len(items))
	t := now()
	for _, it := range items {
		id := "bj_" + randomHex(10)
		if _, err := g.st.exec(`INSERT INTO batch_jobs(id,customer_id,kind,model,request,status,created_at) VALUES(?,?,?,?,?,?,?)`,
			id, acct.ID, it.kind, it.model, it.body, "queued", t); err != nil {
			apiError(w, 500, "could not queue the batch")
			return
		}
		ids = append(ids, id)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"object": "batch", "ids": ids, "status": "queued",
		"note": "Jobs run when a suitable device is idle, usually within minutes, at most 24 hours. Poll GET /v1/batch/{id}."})
}

func (g *Gateway) scanBatch(rows interface{ Scan(...any) error }) (*batchJob, error) {
	j := &batchJob{}
	var result string
	err := rows.Scan(&j.ID, &j.customer, &j.Kind, &j.Model, &j.request, &j.Status, &result, &j.Error, &j.Attempts, &j.CreatedAt, &j.FinishedAt)
	if result != "" {
		j.Result = json.RawMessage(result)
	}
	return j, err
}

const batchCols = `id,customer_id,kind,model,request,status,result,error,attempts,created_at,finished_at`

func (g *Gateway) BatchGet(w http.ResponseWriter, r *http.Request) {
	acct, err := g.st.AccountByKey(bearer(r))
	if err != nil || acct.Kind != "customer" {
		apiError(w, 401, "missing or invalid API key")
		return
	}
	j, err := g.scanBatch(g.st.queryRow(`SELECT `+batchCols+` FROM batch_jobs WHERE id=? AND customer_id=?`, r.PathValue("id"), acct.ID))
	if err != nil {
		apiError(w, 404, "no batch job with that id")
		return
	}
	writeJSON(w, j)
}

func (g *Gateway) BatchList(w http.ResponseWriter, r *http.Request) {
	acct, err := g.st.AccountByKey(bearer(r))
	if err != nil || acct.Kind != "customer" {
		apiError(w, 401, "missing or invalid API key")
		return
	}
	q := `SELECT ` + batchCols + ` FROM batch_jobs WHERE customer_id=?`
	args := []any{acct.ID}
	if st := r.URL.Query().Get("status"); st != "" {
		q += ` AND status=?`
		args = append(args, st)
	}
	rows, err := g.st.query(q+` ORDER BY created_at DESC LIMIT 200`, args...)
	if err != nil {
		apiError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	var out []*batchJob
	for rows.Next() {
		if j, err := g.scanBatch(rows); err == nil {
			out = append(out, j)
		}
	}
	writeJSON(w, map[string]any{"object": "list", "data": out})
}

// BatchWorker hands queued jobs to idle devices, oldest first.
func (g *Gateway) BatchWorker() {
	for {
		time.Sleep(2 * time.Second)
		g.dispatchBatch()
	}
}

func (g *Gateway) dispatchBatch() int {
	g.st.exec(`UPDATE batch_jobs SET status='failed', error='no device took the job within 24 hours', finished_at=? WHERE status='queued' AND created_at<?`, now(), now()-batchExpiry)
	rows, err := g.st.query(`SELECT ` + batchCols + ` FROM batch_jobs WHERE status='queued' ORDER BY created_at LIMIT 200`)
	if err != nil {
		return 0
	}
	var jobs []*batchJob
	for rows.Next() {
		if j, err := g.scanBatch(rows); err == nil {
			jobs = append(jobs, j)
		}
	}
	rows.Close()
	started := 0
	busy := map[string]bool{}
	for _, j := range jobs {
		var dev *Node
		for _, n := range g.hub.CandidatesFor(j.Model, "sandboxed", true, "batch_chat") {
			if !busy[n.ID] {
				dev = n
				break
			}
		}
		if dev == nil {
			continue // nobody free for this model right now
		}
		res, err := g.st.exec(`UPDATE batch_jobs SET status='running', started_at=?, attempts=attempts+1 WHERE id=? AND status='queued'`, now(), j.ID)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // another instance claimed it
		}
		busy[dev.ID] = true
		started++
		go g.runBatch(j, dev)
	}
	return started
}

func (g *Gateway) runBatch(j *batchJob, n *Node) {
	acct, err := g.st.AccountByID(j.customer)
	if err != nil {
		return
	}
	raw := []byte(j.request)
	req := chatRequest{queued: true, InferaBatch: true, Privacy: "sandboxed"}
	json.Unmarshal(raw, &req)
	if req.MaxTokens <= 0 {
		req.MaxTokens = 512
	}
	req.Privacy = "sandboxed"
	if j.Kind == "embeddings" {
		req.jobType = "embeddings"
	}
	jobID := "job_" + randomHex(10)
	rec := httptest.NewRecorder()
	sw := newStreamWriter(rec, false, jobID, j.Model)
	sw.embed = j.Kind == "embeddings"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	res := g.runOnDevice(ctx, n, acct, jobID, req, raw, len(raw)/4, sw)
	cancel()
	if res == runDone && rec.Code < 300 {
		g.st.exec(`UPDATE batch_jobs SET status='done', result=?, finished_at=? WHERE id=?`, rec.Body.String(), now(), j.ID)
		return
	}
	if j.Attempts+1 >= batchMaxAttempts {
		g.st.exec(`UPDATE batch_jobs SET status='failed', error=?, finished_at=? WHERE id=?`, fmt.Sprintf("failed on %d devices", batchMaxAttempts), now(), j.ID)
		return
	}
	g.st.exec(`UPDATE batch_jobs SET status='queued' WHERE id=?`, j.ID) // try another device
	log.Printf("batch %s: attempt failed on %s, re-queued", j.ID, n.ID)
}
