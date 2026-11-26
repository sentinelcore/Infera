package main

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Hedged requests (G12): for interactive chat, if the first device hasn't started answering
// within INFERA_HEDGE_MS (default 2500 ms), the same request is offered to a second device.
// Whichever streams its first token first wins; the other is cancelled at no cost to
// anyone (no charge, no penalty, recorded as no_fault "hedged"). The customer is billed
// once, for the winner.

type hedgeRace struct {
	mu      sync.Mutex
	winner  string
	cancels map[string]context.CancelFunc
}

// claim is called by a run right before it writes its first output to the customer.
// The first caller wins and cancels the others.
func (h *hedgeRace) claim(jobID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.winner == "" {
		h.winner = jobID
		for id, c := range h.cancels {
			if id != jobID {
				c()
			}
		}
	}
	return h.winner == jobID
}

// lost reports whether another run already won.
func (h *hedgeRace) lost(jobID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.winner != "" && h.winner != jobID
}

func (h *hedgeRace) add(jobID string, c context.CancelFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancels[jobID] = c
}

func (h *hedgeRace) claimed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.winner != ""
}

// hedgeable: interactive chat only (not batch, embeddings, canaries or re-runs).
func (g *Gateway) hedgeable(req chatRequest, candidates int) bool {
	return g.cfg.HedgeAfter > 0 && candidates >= 2 && !req.InferaBatch && (req.jobType == "" || req.jobType == "completion") && req.expect == "" && !req.rerun && !req.queued
}

type hedgeResult struct {
	jobID string
	res   runResult
}

// runHedged runs the request on a, and also on b if a hasn't started answering by HedgeAfter.
// Returns the winning result, or the outcome to continue the normal retry loop with.
func (g *Gateway) runHedged(r *http.Request, a, b *Node, acct *Account, req chatRequest, raw []byte, estIn int, sw *streamWriter) runResult {
	race := &hedgeRace{cancels: map[string]context.CancelFunc{}}
	req.race = race
	results := make(chan hedgeResult, 2)
	start := func(n *Node) string {
		id := "job_" + randomHex(10)
		ctx, cancel := context.WithCancel(r.Context())
		race.add(id, cancel)
		go func() {
			defer cancel()
			results <- hedgeResult{id, g.runOnDevice(ctx, n, acct, id, req, raw, estIn, sw)}
		}()
		return id
	}
	start(a)
	running := 1
	timer := time.NewTimer(g.cfg.HedgeAfter)
	defer timer.Stop()
	var best runResult = runNotAccepted
	for running > 0 {
		select {
		case <-timer.C:
			if !race.claimed() && running == 1 && r.Context().Err() == nil {
				start(b)
				running++
				mon.Job("hedged", 0)
			}
		case hr := <-results:
			running--
			switch hr.res {
			case runDone, runClientGone:
				return hr.res
			case runAbortedAfterOutput:
				best = runAbortedAfterOutput
			}
			// The first device failed before answering: start the second one now.
			if running == 0 && !race.claimed() && timer.Stop() && r.Context().Err() == nil {
				start(b)
				running++
			}
		}
	}
	return best
}
