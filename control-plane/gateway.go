package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Price struct {
	In  float64 `json:"in"`  // USD per 1M prompt tokens
	Out float64 `json:"out"` // USD per 1M output tokens
}

type Config struct {
	OwnerShare    float64
	Prices        map[string]Price
	DefaultPrice  Price
	FallbackURL   string
	FallbackKey   string
	FallbackModel string
	EmbedPrice    float64 // USD per 1M input tokens for embeddings
	BatchDiscount float64 // fraction off for queued batch jobs
	FirstToken    time.Duration
	HedgeAfter    time.Duration // 0 = no hedged requests
	JobDeadline   time.Duration
}

func (c *Config) price(model string) Price {
	if p, ok := c.Prices[model]; ok {
		return p
	}
	return c.DefaultPrice
}

func (c *Config) cost(model string, tin, tout int) int64 {
	p := c.price(model)
	usd := float64(tin)/1e6*p.In + float64(tout)/1e6*p.Out
	return int64(math.Ceil(usd * microUSD))
}

type Gateway struct {
	st     *Store
	hub    *Hub
	dep    *Dependability
	cfg    *Config
	canary *Account // internal account that pays for test prompts and re-runs
	rerun  *Rerun
	limit  *Limiter
}

type chatRequest struct {
	Model       string            `json:"model"`
	Messages    []json.RawMessage `json:"messages"`
	Stream      bool              `json:"stream"`
	MaxTokens   int               `json:"max_tokens"`
	Privacy     string            `json:"privacy"`
	InferaBatch bool              `json:"infera_batch"`
	expect      string            // canary: answer the output must contain
	jobType     string            // "" (chat) or "embeddings"
	Temperature *float64          `json:"temperature"`
	rerun       bool              // this job is a verification re-run
	queued      bool              // came through the batch queue
	race        *hedgeRace        // set when this run is one side of a hedged request
}

func apiError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "code": code}})
}

func bearer(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

func (g *Gateway) Models(w http.ResponseWriter, r *http.Request) {
	var data []map[string]any
	for m, n := range g.hub.OnlineModels() {
		p := g.cfg.price(m)
		data = append(data, map[string]any{"id": m, "object": "model", "owned_by": "infera", "devices_online": n,
			"price_per_1m": map[string]float64{"input": p.In, "output": p.Out}})
	}
	if g.cfg.FallbackURL != "" && g.cfg.FallbackModel != "" {
		data = append(data, map[string]any{"id": g.cfg.FallbackModel, "object": "model", "owned_by": "infera-fallback"})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (g *Gateway) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	acct, err := g.st.AccountByKey(bearer(r))
	if err != nil || acct.Kind != "customer" {
		apiError(w, 401, "missing or invalid API key; create one on the Developers tab of the dashboard")
		return
	}
	release := g.limit.Acquire(w, acct.ID)
	if release == nil {
		return
	}
	defer release()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		apiError(w, 400, "could not read request body")
		return
	}
	var req chatRequest
	if err := json.Unmarshal(raw, &req); err != nil || req.Model == "" || len(req.Messages) == 0 {
		apiError(w, 400, "request needs \"model\" and \"messages\"")
		return
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = 512
	}
	if req.Privacy == "" {
		req.Privacy = "sandboxed"
	}
	if _, ok := privacyRank[req.Privacy]; !ok {
		apiError(w, 400, "privacy must be sandboxed, sealed or confidential")
		return
	}
	estIn := len(raw) / 4
	estCharge := g.cfg.cost(req.Model, estIn, req.MaxTokens)
	if acct.Credits < estCharge {
		apiError(w, 402, fmt.Sprintf("not enough credits: this request can cost up to $%.4f, balance is $%.4f", float64(estCharge)/microUSD, float64(acct.Credits)/microUSD))
		return
	}

	jobID := "job_" + randomHex(10)
	sw := newStreamWriter(w, req.Stream, jobID, req.Model)

	tried := 0
	cands := g.hub.CandidatesFor(req.Model, req.Privacy, req.InferaBatch, req.typeName())
	if g.hedgeable(req, len(cands)) {
		res := g.runHedged(r, cands[0], cands[1], acct, req, raw, estIn, sw)
		if res == runDone || res == runClientGone {
			return
		}
		if res == runAbortedAfterOutput && sw.committed() {
			sw.fail("the device stopped midway; retry the request")
			return
		}
		cands, tried = cands[2:], 2
		jobID = "job_" + randomHex(10)
		sw.reset(jobID)
	}
	for _, n := range cands {
		if tried >= 5 {
			break
		}
		tried++
		res := g.runOnDevice(r.Context(), n, acct, jobID, req, raw, estIn, sw)
		if res == runDone || res == runClientGone {
			return
		}
		if res == runAbortedAfterOutput && sw.committed() {
			sw.fail("the device stopped midway; retry the request")
			return
		}
		// Not accepted, or failed before anything reached the customer: try the next device.
		jobID = "job_" + randomHex(10)
		sw.reset(jobID)
	}

	if g.cfg.FallbackURL != "" {
		g.fallback(w, r, acct, jobID, req, raw, estIn)
		return
	}
	apiError(w, 503, fmt.Sprintf("no device is serving %q at privacy level %q right now; try again shortly or use GET /v1/models", req.Model, req.Privacy))
}

// typeName is the job type shown on offer cards: chat, batch_chat or embeddings.
func (req chatRequest) typeName() string {
	if req.jobType != "" {
		return req.jobType
	}
	if req.InferaBatch {
		return "batch_chat"
	}
	return "chat"
}

type runResult int

const (
	runNotAccepted runResult = iota
	runDone
	runAbortedBeforeOutput
	runAbortedAfterOutput
	runClientGone
)

func (g *Gateway) runOnDevice(ctx context.Context, n *Node, acct *Account, jobID string, req chatRequest, raw []byte, estIn int, sw *streamWriter) runResult {
	dev, err := g.st.Device(n.ID)
	if err != nil {
		return runNotAccepted
	}
	band := BandFor(dev.Dependability)
	estCharge := g.jobCost(req, estIn, req.MaxTokens)
	estPay := float64(estCharge) * g.cfg.OwnerShare * band.Multiplier / microUSD

	inbox := n.OpenJob(jobID)
	defer n.CloseJob(jobID)
	ttl := 5
	if n.Mode == "ask" {
		ttl = 30
	}
	jobType := req.typeName()
	n.Send(Msg{T: "offer", JobID: jobID, JobType: jobType, Model: req.Model, EstTokens: estIn + req.MaxTokens, EstPay: estPay, TTL: ttl, Privacy: req.Privacy})

	// 1. Wait for accept / decline.
	var accept Msg
	timer := time.NewTimer(time.Duration(ttl+2) * time.Second)
	defer timer.Stop()
wait:
	for {
		select {
		case <-ctx.Done():
			n.Send(Msg{T: "cancel", JobID: jobID})
			if req.race != nil && req.race.lost(jobID) {
				n.Send(Msg{T: "offer_expired", JobID: jobID}) // close the offer card
				return runNotAccepted                         // the other device of a hedged request answered first
			}
			return runClientGone
		case <-timer.C:
			n.Send(Msg{T: "offer_expired", JobID: jobID})
			if n.Mode == "ask" {
				n.NoteOffer(jobType, false) // the owner let the card run out
			}
			return runNotAccepted // declining or timing out an offer is free
		case m := <-inbox:
			switch m.T {
			case "accept":
				n.NoteOffer(jobType, true)
				accept = m
				break wait
			case "decline", "lost":
				if m.Reason == "user_declined" {
					n.NoteOffer(jobType, false)
				}
				return runNotAccepted
			}
		}
	}

	// 2. Seal the job to the sandbox key and send it.
	seal, err := NewSeal(jobID, accept.SandboxPub)
	if err != nil {
		g.abort(dev, jobID, CauseDeviceFailure, estPay, "accept carried an invalid sandbox key")
		return runAbortedBeforeOutput
	}
	nonce, ct, _ := seal.SealDown(raw)
	g.st.InsertJob(jobID, acct.ID, n.ID, req.Model, "device")
	n.Send(Msg{T: "payload", JobID: jobID, EPK: b64(seal.EphemeralPub), Nonce: b64(nonce), CT: b64(ct)})
	started := time.Now()

	// 3. Stream sealed output back to the customer.
	var out strings.Builder
	deadline := time.NewTimer(g.cfg.JobDeadline)
	defer deadline.Stop()
	idle := time.NewTimer(g.cfg.FirstToken)
	defer idle.Stop()
	gotFirst := false
	pieces := 0
	for {
		select {
		case <-ctx.Done():
			n.Send(Msg{T: "cancel", JobID: jobID})
			if req.race != nil && req.race.lost(jobID) {
				g.st.FinishJob(jobID, "aborted", 0, 0, 0, 0, CauseNoFault, "")
				return runNotAccepted // hedged: the other device answered first; no charge, no penalty
			}
			g.settlePartial(acct, dev, jobID, req, estIn, pieces, band, "customer cancelled")
			return runClientGone
		case <-idle.C:
			n.Send(Msg{T: "cancel", JobID: jobID})
			if !gotFirst {
				g.abort(dev, jobID, CauseNotDelivered, estPay, fmt.Sprintf("no first token within %s", g.cfg.FirstToken))
				return runAbortedBeforeOutput
			}
			g.abort(dev, jobID, CauseNotDelivered, estPay, "output stalled for 60 s")
			return runAbortedAfterOutput
		case <-deadline.C:
			n.Send(Msg{T: "cancel", JobID: jobID})
			g.abort(dev, jobID, CauseNotDelivered, estPay, fmt.Sprintf("job not finished within %s", g.cfg.JobDeadline))
			if gotFirst {
				return runAbortedAfterOutput
			}
			return runAbortedBeforeOutput
		case m := <-inbox:
			switch m.T {
			case "tok":
				plain, err := seal.OpenUp(unb64(m.Nonce), unb64(m.CT))
				if err != nil {
					n.Send(Msg{T: "cancel", JobID: jobID})
					g.abort(dev, jobID, CauseWrongResult, estPay, "output failed authentication")
					if gotFirst {
						return runAbortedAfterOutput
					}
					return runAbortedBeforeOutput
				}
				if !gotFirst && !g.hedgeClaim(req, n, jobID, sw) {
					return runNotAccepted
				}
				gotFirst = true
				pieces++
				out.Write(plain)
				if m.R {
					sw.reasoning(string(plain))
				} else {
					sw.delta(string(plain))
				}
				idle.Reset(60 * time.Second)
			case "done":
				if !gotFirst && !g.hedgeClaim(req, n, jobID, sw) {
					return runNotAccepted
				}
				output := out.String()
				if !g.checkReceipt(dev, jobID, req.Model, output, m) {
					g.abort(dev, jobID, CauseWrongResult, estPay, "receipt signature or output hash did not match")
					sw.finish(m.TokensIn, m.TokensOut)
					return runDone
				}
				tin, tout := m.TokensIn, m.TokensOut
				if tout <= 0 {
					tout = pieces
				}
				charge := g.jobCost(req, tin, tout)
				pay := int64(float64(charge) * g.cfg.OwnerShare * band.Multiplier)
				g.st.AdjustCredits(acct.ID, -charge)
				g.st.AddLedger(acct.ID, "", jobID, "charge", -charge, 0, req.Model)
				g.st.AddLedger(dev.OwnerID, dev.ID, jobID, "earning", pay, now()+int64(band.HoldDays)*24*3600,
					fmt.Sprintf("%s · %d+%d tokens · %.2fx", req.Model, tin, tout, band.Multiplier))
				g.st.FinishJob(jobID, "done", tin, tout, charge, pay, "", m.Receipt)
				g.dep.Reward(dev, jobID, time.Since(started))
				mon.Job("done", time.Since(started))
				if req.jobType == "" {
					g.RecordSpeed(dev, tout, time.Since(started))
				}
				if req.expect != "" {
					g.judgeCanary(dev, jobID, output, req.expect, pay)
				}
				if g.rerun != nil && req.expect == "" && !req.rerun {
					g.rerun.Maybe(dev, req, raw, output, pay)
				}
				g.hub.sendConfig(n)
				sw.finish(tin, tout)
				return runDone
			case "abort":
				cause := CauseDeviceFailure
				if m.Reason == "owner_stop" {
					cause = CauseOwnerAction
				}
				g.abort(dev, jobID, cause, estPay, "device reported: "+m.Reason)
				if gotFirst {
					return runAbortedAfterOutput
				}
				return runAbortedBeforeOutput
			case "lost":
				g.abort(dev, jobID, m.Reason, estPay, "connection to device lost mid-job")
				if gotFirst {
					return runAbortedAfterOutput
				}
				return runAbortedBeforeOutput
			}
		}
	}
}

// hedgeClaim: in a hedged request, only the first device to answer writes to the customer.
func (g *Gateway) hedgeClaim(req chatRequest, n *Node, jobID string, sw *streamWriter) bool {
	if req.race == nil {
		return true
	}
	if !req.race.claim(jobID) {
		n.Send(Msg{T: "cancel", JobID: jobID})
		g.st.FinishJob(jobID, "aborted", 0, 0, 0, 0, CauseNoFault, "")
		return false
	}
	sw.reset(jobID) // nothing written yet: the response carries the winner's job id
	return true
}

func (g *Gateway) checkReceipt(dev *DeviceRow, jobID, model, output string, m Msg) bool {
	parts := strings.Split(m.Receipt, "|")
	if len(parts) != 6 || parts[0] != jobID || parts[1] != model {
		return false
	}
	h := sha256.Sum256([]byte(output))
	if parts[2] != hex.EncodeToString(h[:]) {
		return false
	}
	if parts[3] != strconv.Itoa(m.TokensIn) || parts[4] != strconv.Itoa(m.TokensOut) {
		return false
	}
	return VerifyReceipt(dev.Pubkey, m.Receipt, m.Sig)
}

func (g *Gateway) abort(dev *DeviceRow, jobID, cause string, estPay float64, evidence string) {
	mon.Job("aborted:"+cause, 0)
	log.Printf("job %s aborted on %s: %s (%s)", jobID, dev.ID, cause, evidence)
	g.st.FinishJob(jobID, "aborted", 0, 0, 0, 0, cause, "")
	g.dep.Penalize(dev, jobID, cause, int64(estPay*microUSD), evidence)
	if n := g.hub.Node(dev.ID); n != nil {
		g.hub.sendConfig(n)
	}
}

// settlePartial pays for work done when the customer disconnects: no penalty to the owner.
// jobCost prices a job: chat by input and output tokens, embeddings by input tokens.
func (g *Gateway) jobCost(req chatRequest, tin, tout int) int64 {
	var c int64
	if req.jobType == "embeddings" {
		c = int64(math.Ceil(float64(tin) / 1e6 * g.cfg.EmbedPrice * microUSD))
	} else {
		c = g.cfg.cost(req.Model, tin, tout)
	}
	if req.queued { // batch API: cheaper because it can wait for an idle device
		c = int64(math.Ceil(float64(c) * (1 - g.cfg.BatchDiscount)))
	}
	return c
}

func (g *Gateway) settlePartial(acct *Account, dev *DeviceRow, jobID string, req chatRequest, estIn, pieces int, band Band, note string) {
	model := req.Model
	charge := g.jobCost(req, estIn, pieces)
	pay := int64(float64(charge) * g.cfg.OwnerShare * band.Multiplier)
	g.st.AdjustCredits(acct.ID, -charge)
	g.st.AddLedger(acct.ID, "", jobID, "charge", -charge, 0, model+" (partial)")
	if pay > 0 {
		g.st.AddLedger(dev.OwnerID, dev.ID, jobID, "earning", pay, now()+int64(band.HoldDays)*24*3600, note)
	}
	g.st.FinishJob(jobID, "aborted", estIn, pieces, charge, pay, CauseNoFault, "")
	g.st.AddScoreEvent(dev.ID, jobID, CauseNoFault, 0, note)
}

// fallback forwards the request to the rented-capacity provider (ARCHITECTURE.md §2.5).
func (g *Gateway) fallback(w http.ResponseWriter, r *http.Request, acct *Account, jobID string, req chatRequest, raw []byte, estIn int) {
	body := raw
	model := req.Model
	if g.cfg.FallbackModel != "" {
		var m map[string]any
		json.Unmarshal(raw, &m)
		m["model"] = g.cfg.FallbackModel
		delete(m, "privacy")
		delete(m, "infera_batch")
		body, _ = json.Marshal(m)
		model = g.cfg.FallbackModel
	}
	up, _ := http.NewRequestWithContext(r.Context(), "POST", strings.TrimRight(g.cfg.FallbackURL, "/")+"/chat/completions", bytes.NewReader(body))
	up.Header.Set("Content-Type", "application/json")
	if g.cfg.FallbackKey != "" {
		up.Header.Set("Authorization", "Bearer "+g.cfg.FallbackKey)
	}
	resp, err := http.DefaultClient.Do(up)
	if err != nil {
		apiError(w, 502, "fallback provider unreachable")
		return
	}
	defer resp.Body.Close()
	g.st.InsertJob(jobID, acct.ID, "", model, "fallback")
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("X-Infera-Route", "fallback")
	w.WriteHeader(resp.StatusCode)
	var buf bytes.Buffer
	tee := io.TeeReader(resp.Body, &buf)
	fl, _ := w.(http.Flusher)
	chunk := make([]byte, 4096)
	for {
		k, err := tee.Read(chunk)
		if k > 0 {
			w.Write(chunk[:k])
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	tin, tout := estIn, req.MaxTokens
	var parsed struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if !req.Stream && json.Unmarshal(buf.Bytes(), &parsed) == nil && parsed.Usage.CompletionTokens > 0 {
		tin, tout = parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens
	}
	if resp.StatusCode == 200 {
		charge := g.cfg.cost(req.Model, tin, tout)
		g.st.AdjustCredits(acct.ID, -charge)
		g.st.AddLedger(acct.ID, "", jobID, "charge", -charge, 0, model+" (fallback)")
		g.st.FinishJob(jobID, "done", tin, tout, charge, 0, "", "")
	} else {
		g.st.FinishJob(jobID, "failed", 0, 0, 0, 0, "fallback_error", "")
	}
}

// streamWriter writes either OpenAI-style SSE chunks or one final JSON response.
type streamWriter struct {
	w       http.ResponseWriter
	stream  bool
	jobID   string
	model   string
	started bool
	buf     strings.Builder
	rbuf    strings.Builder
	embed   bool // embeddings response instead of chat
}

func newStreamWriter(w http.ResponseWriter, stream bool, jobID, model string) *streamWriter {
	return &streamWriter{w: w, stream: stream, jobID: jobID, model: model}
}

// committed reports whether bytes have already been sent to the customer.
func (s *streamWriter) committed() bool { return s.stream && s.started }

func (s *streamWriter) reset(jobID string) {
	s.jobID = jobID
	s.started = false
	s.buf.Reset()
	s.rbuf.Reset()
}

func (s *streamWriter) begin() {
	if s.started {
		return
	}
	s.started = true
	s.w.Header().Set("X-Infera-Route", "device")
	s.w.Header().Set("X-Infera-Job", s.jobID)
	if s.stream {
		s.w.Header().Set("Content-Type", "text/event-stream")
		s.w.Header().Set("Cache-Control", "no-cache")
		s.w.WriteHeader(200)
	}
}

func (s *streamWriter) chunk(delta map[string]any, finish any) {
	b, _ := json.Marshal(map[string]any{"id": s.jobID, "object": "chat.completion.chunk", "created": now(), "model": s.model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	fmt.Fprintf(s.w, "data: %s\n\n", b)
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
}

// reasoning streams "thinking" text in the OpenAI-style reasoning_content field.
func (s *streamWriter) reasoning(text string) {
	if s.stream {
		first := !s.started
		s.begin()
		if first {
			s.chunk(map[string]any{"role": "assistant", "content": ""}, nil)
		}
		s.chunk(map[string]any{"reasoning_content": text}, nil)
		return
	}
	s.started = true
	s.rbuf.WriteString(text)
}

func (s *streamWriter) delta(text string) {
	if s.stream {
		first := !s.started
		s.begin()
		if first {
			s.chunk(map[string]any{"role": "assistant", "content": ""}, nil)
		}
		s.chunk(map[string]any{"content": text}, nil)
		return
	}
	s.started = true
	s.buf.WriteString(text)
}

func (s *streamWriter) finish(tin, tout int) {
	if s.embed {
		var res struct {
			Data json.RawMessage `json:"data"`
		}
		json.Unmarshal([]byte(s.buf.String()), &res)
		s.begin()
		s.w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(s.w).Encode(map[string]any{"object": "list", "data": res.Data, "model": s.model,
			"usage": map[string]int{"prompt_tokens": tin, "total_tokens": tin}})
		return
	}
	usage := map[string]int{"prompt_tokens": tin, "completion_tokens": tout, "total_tokens": tin + tout}
	if s.stream {
		s.begin()
		s.chunk(map[string]any{}, "stop")
		fmt.Fprint(s.w, "data: [DONE]\n\n")
		if f, ok := s.w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}
	s.begin()
	s.w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(s.w).Encode(map[string]any{"id": s.jobID, "object": "chat.completion", "created": now(), "model": s.model,
		"choices": []any{map[string]any{"index": 0, "message": s.message(), "finish_reason": "stop"}},
		"usage":   usage})
}

func (s *streamWriter) message() map[string]string {
	m := map[string]string{"role": "assistant", "content": s.buf.String()}
	if s.rbuf.Len() > 0 {
		m["reasoning_content"] = s.rbuf.String()
	}
	return m
}

func (s *streamWriter) fail(msg string) {
	if s.stream {
		b, _ := json.Marshal(map[string]any{"error": map[string]string{"message": msg}})
		fmt.Fprintf(s.w, "data: %s\n\ndata: [DONE]\n\n", b)
		return
	}
	apiError(s.w, 502, msg)
}

// Embeddings serves POST /v1/embeddings (OpenAI format) on devices that advertise the model.
// Embeddings can wait, so devices in the batch-only dependability bands may take them too.
func (g *Gateway) Embeddings(w http.ResponseWriter, r *http.Request) {
	acct, err := g.st.AccountByKey(bearer(r))
	if err != nil || acct.Kind != "customer" {
		apiError(w, 401, "missing or invalid API key; create one on the Developers tab of the dashboard")
		return
	}
	release := g.limit.Acquire(w, acct.ID)
	if release == nil {
		return
	}
	defer release()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		apiError(w, 400, "could not read request body")
		return
	}
	var in struct {
		Model   string          `json:"model"`
		Input   json.RawMessage `json:"input"`
		Privacy string          `json:"privacy"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Model == "" || len(in.Input) == 0 || string(in.Input) == "null" {
		apiError(w, 400, "request needs \"model\" and \"input\"")
		return
	}
	if in.Privacy == "" {
		in.Privacy = "sandboxed"
	}
	if _, ok := privacyRank[in.Privacy]; !ok {
		apiError(w, 400, "privacy must be sandboxed, sealed or confidential")
		return
	}
	req := chatRequest{Model: in.Model, Privacy: in.Privacy, InferaBatch: true, jobType: "embeddings"}
	estIn := len(raw) / 4
	if est := g.jobCost(req, estIn, 0); acct.Credits < est {
		apiError(w, 402, fmt.Sprintf("not enough credits: this request can cost up to $%.6f", float64(est)/microUSD))
		return
	}
	jobID := "job_" + randomHex(10)
	sw := newStreamWriter(w, false, jobID, in.Model)
	sw.embed = true
	tried := 0
	for _, n := range g.hub.CandidatesFor(in.Model, in.Privacy, true, "embeddings") {
		if tried >= 5 {
			break
		}
		tried++
		res := g.runOnDevice(r.Context(), n, acct, jobID, req, raw, estIn, sw)
		if res == runDone || res == runClientGone {
			return
		}
		jobID = "job_" + randomHex(10)
		sw.reset(jobID)
	}
	apiError(w, 503, fmt.Sprintf("no device is serving embeddings model %q right now; GET /v1/models lists what's online", in.Model))
}
