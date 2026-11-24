package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Canaries (test prompts, ARCHITECTURE.md §7): easy questions with known answers, sent to idle
// devices through the normal sealed-job path so a node can't tell them apart from customer jobs.
// They are paid like any small job from an internal account.

type canaryQ struct{ prompt, answer string }

var canaryQuestions = []canaryQ{
	{"What is 17 + 25? Reply with only the number.", "42"},
	{"What is 9 times 8? Reply with only the number.", "72"},
	{"What is 100 minus 1? Reply with only the number.", "99"},
	{"How many days are in a week? Reply with only the number.", "7"},
	{"What is the capital of France? Reply with one word.", "paris"},
	{"What is the capital of Japan? Reply with one word.", "tokyo"},
	{"What colour is the sky on a clear day? Reply with one word.", "blue"},
	{"What is 12 divided by 4? Reply with only the number.", "3"},
}

type Canary struct {
	gw       *Gateway
	interval time.Duration
	mu       sync.Mutex
	last     map[string]time.Time
}

func NewCanary(gw *Gateway, interval time.Duration) *Canary {
	return &Canary{gw: gw, interval: interval, last: map[string]time.Time{}}
}

// EnsureCanaryAccount returns the internal account that pays for canaries.
func EnsureCanaryAccount(st *Store) (*Account, error) {
	var id string
	if err := st.queryRow(`SELECT id FROM accounts WHERE email='canary@infera.internal'`).Scan(&id); err == nil {
		return st.AccountByID(id)
	}
	a, _, err := st.CreateAccount("canary@infera.internal", "customer", 0)
	return a, err
}

func (c *Canary) Loop() {
	if c.interval <= 0 {
		return
	}
	t := time.NewTicker(time.Duration(min64(int64(c.interval), int64(time.Minute))))
	for range t.C {
		c.gw.hub.mu.Lock()
		var due []*Node
		for _, n := range c.gw.hub.nodes {
			n.mu.Lock()
			ok := n.Online && n.Status == "idle" && len(n.jobs) == 0 && len(n.Models) > 0 && classRank[n.Class] > 0
			n.mu.Unlock()
			c.mu.Lock()
			if ok && time.Since(c.last[n.ID]) >= c.interval {
				c.last[n.ID] = time.Now()
				due = append(due, n)
			}
			c.mu.Unlock()
		}
		c.gw.hub.mu.Unlock()
		for _, n := range due {
			go c.run(n)
		}
	}
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (c *Canary) run(n *Node) {
	q := canaryQuestions[rand.Intn(len(canaryQuestions))]
	model := n.Models[0]
	body := map[string]any{"model": model, "max_tokens": 512, "temperature": 0,
		"messages": []map[string]string{{"role": "user", "content": q.prompt}}}
	raw, _ := json.Marshal(body)
	var msgs []json.RawMessage
	json.Unmarshal(mustJSON(body["messages"]), &msgs)
	req := chatRequest{Model: model, Messages: msgs, MaxTokens: 512, Privacy: "sandboxed", InferaBatch: true, expect: q.answer}
	acct, err := c.gw.st.AccountByID(c.gw.canary.ID)
	if err != nil {
		return
	}
	acct.Credits = 1 << 50 // the internal account is never short of credits
	jobID := "job_" + randomHex(10)
	rec := httptest.NewRecorder()
	sw := newStreamWriter(rec, false, jobID, model)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	res := c.gw.runOnDevice(ctx, n, acct, jobID, req, raw, len(raw)/4, sw)
	if res != runDone {
		log.Printf("canary %s on %s: not completed (%d)", jobID, n.ID, res)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// judgeCanary records a pass or fail. Two fails in the last three canaries is a wrong_result penalty.
func (g *Gateway) judgeCanary(dev *DeviceRow, jobID, output, expect string, jobValue int64) {
	pass := strings.Contains(strings.ToLower(output), strings.ToLower(expect))
	cause := "canary_pass"
	if !pass {
		cause = "canary_fail"
	}
	g.st.AddScoreEvent(dev.ID, jobID, cause, 0, fmt.Sprintf("expected %q", expect))
	if pass {
		return
	}
	rows, err := g.st.query(`SELECT cause FROM score_events WHERE device_id=? AND cause IN ('canary_pass','canary_fail') ORDER BY id DESC LIMIT 3`, dev.ID)
	if err != nil {
		return
	}
	fails := 0
	for rows.Next() {
		var c string
		rows.Scan(&c)
		if c == "canary_fail" {
			fails++
		}
	}
	rows.Close()
	if fails >= 2 {
		d, err := g.st.Device(dev.ID)
		if err != nil {
			return
		}
		g.dep.Penalize(d, jobID, CauseWrongResult, jobValue, fmt.Sprintf("%d of the last 3 test prompts answered wrong", fails))
		if n := g.hub.Node(dev.ID); n != nil {
			g.hub.sendConfig(n)
		}
	}
}
