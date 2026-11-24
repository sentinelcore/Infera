package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http/httptest"
	"strings"
	"time"
)

// Re-runs (ARCHITECTURE.md §7): a share of finished customer jobs whose answer should be
// repeatable (chat at temperature 0, and embeddings) is run again on a second device.
// If the two disagree, a third device breaks the tie; the device in the minority gets a
// rerun_mismatch event. Two mismatches in its last five re-runs is a wrong_result penalty.
type Rerun struct {
	gw   *Gateway
	rate float64
}

func (r *Rerun) Maybe(orig *DeviceRow, req chatRequest, raw []byte, output string, pay int64) {
	repeatable := req.jobType == "embeddings" || (req.Temperature != nil && *req.Temperature == 0)
	if !repeatable || r.rate <= 0 || rand.Float64() >= r.rate {
		return
	}
	go r.run(orig, req, raw, output, pay)
}

// again runs the request on another device (not in exclude) and returns its output.
func (r *Rerun) again(req chatRequest, raw []byte, exclude map[string]bool) (string, string, bool) {
	acct, err := r.gw.st.AccountByID(r.gw.canary.ID)
	if err != nil {
		return "", "", false
	}
	acct.Credits = 1 << 50
	req.rerun = true
	for _, n := range r.gw.hub.Candidates(req.Model, req.Privacy, true) {
		if exclude[n.ID] {
			continue
		}
		jobID := "job_" + randomHex(10)
		rec := httptest.NewRecorder()
		sw := newStreamWriter(rec, false, jobID, req.Model)
		sw.embed = req.jobType == "embeddings"
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		res := r.gw.runOnDevice(ctx, n, acct, jobID, req, raw, len(raw)/4, sw)
		cancel()
		if res == runDone {
			return n.ID, sw.buf.String() + sw.rbuf.String(), true
		}
	}
	return "", "", false
}

func (r *Rerun) run(orig *DeviceRow, req chatRequest, raw []byte, output string, pay int64) {
	agree := func(a, b string) bool { return outputsAgree(req.jobType, a, b) }
	second, out2, ok := r.again(req, raw, map[string]bool{orig.ID: true})
	if !ok {
		return // no other device free right now; skip quietly
	}
	if agree(output, out2) {
		r.record(orig.ID, "rerun_match", "matched "+second)
		r.record(second, "rerun_match", "matched "+orig.ID)
		return
	}
	third, out3, ok := r.again(req, raw, map[string]bool{orig.ID: true, second: true})
	if !ok {
		r.record(orig.ID, "rerun_disagree", "differed from "+second+"; no third device to decide")
		return
	}
	switch {
	case agree(out3, output) && !agree(out3, out2):
		r.mismatch(second, "disagreed with "+orig.ID+" and "+third, pay)
	case agree(out3, out2) && !agree(out3, output):
		r.mismatch(orig.ID, "disagreed with "+second+" and "+third, pay)
	default:
		r.record(orig.ID, "rerun_disagree", "three devices gave three different answers")
	}
}

func (r *Rerun) record(deviceID, cause, evidence string) {
	r.gw.st.AddScoreEvent(deviceID, "", cause, 0, evidence)
}

func (r *Rerun) mismatch(deviceID, evidence string, pay int64) {
	r.record(deviceID, "rerun_mismatch", evidence)
	rows, err := r.gw.st.query(`SELECT cause FROM score_events WHERE device_id=? AND cause IN ('rerun_match','rerun_mismatch') ORDER BY id DESC LIMIT 5`, deviceID)
	if err != nil {
		return
	}
	bad := 0
	for rows.Next() {
		var c string
		rows.Scan(&c)
		if c == "rerun_mismatch" {
			bad++
		}
	}
	rows.Close()
	if bad < 2 {
		return
	}
	dev, err := r.gw.st.Device(deviceID)
	if err != nil {
		return
	}
	log.Printf("re-run: %s penalized (%d mismatches in last 5)", deviceID, bad)
	r.gw.dep.Penalize(dev, "", CauseWrongResult, pay, fmt.Sprintf("%d of its last 5 re-checked jobs disagreed with other devices", bad))
	if n := r.gw.hub.Node(deviceID); n != nil {
		r.gw.hub.sendConfig(n)
	}
}

// outputsAgree compares two answers to the same repeatable job.
// Chat: word overlap of the first 60 words (hardware can differ slightly even at temperature 0).
// Embeddings: average cosine similarity of the vectors.
func outputsAgree(jobType, a, b string) bool {
	if jobType == "embeddings" {
		return embeddingSimilarity(a, b) >= 0.95
	}
	return wordOverlap(a, b, 60) >= 0.5
}

func wordOverlap(a, b string, n int) float64 {
	wa, wb := firstWords(a, n), firstWords(b, n)
	if len(wa) == 0 && len(wb) == 0 {
		return 1
	}
	inter := 0
	for w := range wa {
		if wb[w] {
			inter++
		}
	}
	union := len(wa) + len(wb) - inter
	if union == 0 {
		return 1
	}
	return float64(inter) / float64(union)
}

func firstWords(s string, n int) map[string]bool {
	out := map[string]bool{}
	for i, w := range strings.Fields(strings.ToLower(s)) {
		if i >= n {
			break
		}
		out[strings.Trim(w, ".,!?;:\"'()[]")] = true
	}
	return out
}

func embeddingSimilarity(a, b string) float64 {
	var ra, rb struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(a), &ra) != nil || json.Unmarshal([]byte(b), &rb) != nil || len(ra.Data) == 0 || len(ra.Data) != len(rb.Data) {
		return 0
	}
	total := 0.0
	for i := range ra.Data {
		total += cosine(ra.Data[i].Embedding, rb.Data[i].Embedding)
	}
	return total / float64(len(ra.Data))
}

func cosine(x, y []float64) float64 {
	if len(x) != len(y) || len(x) == 0 {
		return 0
	}
	var dot, nx, ny float64
	for i := range x {
		dot += x[i] * y[i]
		nx += x[i] * x[i]
		ny += y[i] * y[i]
	}
	if nx == 0 || ny == 0 {
		return 0
	}
	return dot / (math.Sqrt(nx) * math.Sqrt(ny))
}
