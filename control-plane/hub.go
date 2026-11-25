package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Messages between node and hub (ARCHITECTURE.md §4). Every message has a "t" field.
type Msg struct {
	T string `json:"t"`

	// hello
	AgentVersion string          `json:"agent_version,omitempty"`
	Platform     string          `json:"platform,omitempty"`
	Bench        json.RawMessage `json:"bench,omitempty"`
	Class        string          `json:"class,omitempty"`
	Privacy      string          `json:"privacy,omitempty"`
	Models       []string        `json:"models,omitempty"`

	// state
	Status string  `json:"status,omitempty"` // idle | busy | paused
	Online *bool   `json:"online,omitempty"`
	Mode   string  `json:"mode,omitempty"` // auto | ask
	TPS    float64 `json:"tps,omitempty"`
	Slots  int     `json:"slots,omitempty"` // hello: jobs the device can run at once (batching)

	// jobs
	JobID      string  `json:"job_id,omitempty"`
	JobType    string  `json:"job_type,omitempty"`
	Model      string  `json:"model,omitempty"`
	EstTokens  int     `json:"est_tokens,omitempty"`
	EstPay     float64 `json:"est_pay_usd,omitempty"`
	TTL        int     `json:"ttl_s,omitempty"`
	SandboxPub string  `json:"sandbox_pub,omitempty"`
	Attest     string  `json:"attestation,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	EPK        string  `json:"epk,omitempty"`
	Nonce      string  `json:"n,omitempty"`
	CT         string  `json:"c,omitempty"`
	Seq        int     `json:"seq,omitempty"`
	R          bool    `json:"r,omitempty"` // tok: reasoning text rather than answer text
	TokensIn   int     `json:"tokens_in,omitempty"`
	TokensOut  int     `json:"tokens_out,omitempty"`
	Receipt    string  `json:"receipt,omitempty"`
	Sig        string  `json:"sig,omitempty"`

	// config from hub
	Dependability  float64 `json:"dependability,omitempty"`
	SuspendedUntil int64   `json:"suspended_until,omitempty"`
	Band           *Band   `json:"band,omitempty"`
	Message        string  `json:"message,omitempty"`
}

type Node struct {
	ID       string
	OwnerID  string
	Class    string
	Privacy  string
	Models   []string
	Mode     string
	Status   string
	Online   bool
	TPS      float64
	Slots    int                    // jobs at once (1 unless the device batches)
	quitting bool                   // owner asked to quit (bye: owner_quit) – a running job stopped now is an owner action
	declines map[string]*declineLog // job type -> recent owner declines (G9: learn what an owner doesn't want)

	send chan Msg
	jobs map[string]chan Msg // running or offered job -> inbox
	mu   sync.Mutex
}

type Hub struct {
	st    *Store
	mu    sync.Mutex
	nodes map[string]*Node
}

func NewHub(st *Store) *Hub { return &Hub{st: st, nodes: map[string]*Node{}} }

type declineLog struct {
	n    int
	last time.Time
}

// NoteOffer records how the owner answered an offer of this job type. Declines (or letting an
// "ask" card run out) count up; an accept clears the count.
func (n *Node) NoteOffer(jobType string, accepted bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.declines == nil {
		n.declines = map[string]*declineLog{}
	}
	if accepted {
		delete(n.declines, jobType)
		return
	}
	d := n.declines[jobType]
	if d == nil {
		d = &declineLog{}
		n.declines[jobType] = d
	}
	if time.Since(d.last) > 24*time.Hour {
		d.n = 0
	}
	d.n++
	d.last = time.Now()
}

// Avoids reports whether the owner declined this job type 3+ times in the last 24 hours.
func (n *Node) Avoids(jobType string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	d := n.declines[jobType]
	return d != nil && d.n >= 3 && time.Since(d.last) < 24*time.Hour
}

// CandidatesFor is Candidates with devices whose owner keeps declining this job type moved
// to the back: they're only offered the job when nobody else can take it.
func (h *Hub) CandidatesFor(model, privacy string, batch bool, jobType string) []*Node {
	c := h.Candidates(model, privacy, batch)
	sort.SliceStable(c, func(i, j int) bool { return !c[i].Avoids(jobType) && c[j].Avoids(jobType) })
	return c
}

var classRank = map[string]int{"A": 5, "B": 4, "C": 3, "M1": 2, "M2": 1}
var privacyRank = map[string]int{"sandboxed": 1, "sealed": 2, "confidential": 3}

// Candidates returns online, idle, non-suspended devices that serve the model at the
// required privacy level, best first.
func (h *Hub) Candidates(model, privacy string, batch bool) []*Node {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*Node
	for _, n := range h.nodes {
		n.mu.Lock()
		ok := n.Online && n.Status == "idle" && privacyRank[n.Privacy] >= privacyRank[privacy] && contains(n.Models, model) && len(n.jobs) < max(1, n.Slots)
		n.mu.Unlock()
		if !ok || classRank[n.Class] == 0 { // unrated devices (below the benchmark minimum) get no jobs
			continue
		}
		dev, err := h.st.Device(n.ID)
		if err != nil || dev.SuspendedUntil > now() {
			continue
		}
		band := BandFor(dev.Dependability)
		if band.Suspended || (band.BatchOnly && !batch) {
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		// auto-approve first (no human wait), then class, then speed
		if (out[i].Mode == "auto") != (out[j].Mode == "auto") {
			return out[i].Mode == "auto"
		}
		if classRank[out[i].Class] != classRank[out[j].Class] {
			return classRank[out[i].Class] > classRank[out[j].Class]
		}
		return out[i].TPS > out[j].TPS
	})
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if strings.EqualFold(v, x) {
			return true
		}
	}
	return false
}

func (h *Hub) OnlineModels() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := map[string]int{}
	for _, n := range h.nodes {
		if n.Online {
			for _, x := range n.Models {
				m[x]++
			}
		}
	}
	return m
}

func (h *Hub) Node(id string) *Node {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.nodes[id]
}

// OpenJob registers an inbox for a job on a node.
func (n *Node) OpenJob(jobID string) chan Msg {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch := make(chan Msg, 256)
	n.jobs[jobID] = ch
	return ch
}

func (n *Node) CloseJob(jobID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.jobs, jobID)
}

func (n *Node) Send(m Msg) {
	select {
	case n.send <- m:
	default:
		log.Printf("node %s send queue full, dropping %s", n.ID, m.T)
	}
}

// ServeNode handles /ws/node?device_id=...&token=...
func (h *Hub) ServeNode(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("device_id")
	tok := r.URL.Query().Get("token")
	if tok == "" {
		tok = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	dev, err := h.st.DeviceAuth(id, tok)
	if err != nil {
		http.Error(w, "unknown device or wrong token; pair the device again", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(4 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	n := &Node{ID: dev.ID, OwnerID: dev.OwnerID, Class: dev.Class, Privacy: dev.PrivacyLevel, Mode: "auto", Status: "paused",
		send: make(chan Msg, 512), jobs: map[string]chan Msg{}}

	h.mu.Lock()
	if old := h.nodes[dev.ID]; old != nil {
		old.mu.Lock()
		old.Online = false
		old.mu.Unlock()
	}
	h.nodes[dev.ID] = n
	h.mu.Unlock()
	log.Printf("node %s connected", dev.ID)

	go func() { // writer
		ping := time.NewTicker(20 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-n.send:
				b, _ := json.Marshal(m)
				wctx, c2 := context.WithTimeout(ctx, 10*time.Second)
				err := c.Write(wctx, websocket.MessageText, b)
				c2()
				if err != nil {
					cancel()
					return
				}
			case <-ping.C:
				pctx, c2 := context.WithTimeout(ctx, 10*time.Second)
				err := c.Ping(pctx)
				c2()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	h.sendConfig(n)
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			break
		}
		var m Msg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		h.handle(n, m)
	}

	// Disconnected: every job still open on this node is aborted. The cause depends on
	// whether the owner told us they were quitting.
	n.mu.Lock()
	n.Online = false
	cause := CauseDeviceFailure
	if n.quitting {
		cause = CauseOwnerAction
	}
	for jid, ch := range n.jobs {
		select {
		case ch <- Msg{T: "lost", JobID: jid, Reason: cause}:
		default:
		}
	}
	n.mu.Unlock()
	h.mu.Lock()
	if h.nodes[dev.ID] == n {
		delete(h.nodes, dev.ID)
	}
	h.mu.Unlock()
	c.Close(websocket.StatusNormalClosure, "")
	log.Printf("node %s disconnected (%s)", dev.ID, cause)
}

func (h *Hub) sendConfig(n *Node) {
	dev, err := h.st.Device(n.ID)
	if err != nil {
		return
	}
	b := BandFor(dev.Dependability)
	n.Send(Msg{T: "config", Class: dev.Class, Privacy: dev.PrivacyLevel, Dependability: dev.Dependability,
		SuspendedUntil: dev.SuspendedUntil, Band: &b})
}

func (h *Hub) handle(n *Node, m Msg) {
	h.st.TouchDevice(n.ID)
	switch m.T {
	case "hello":
		class := m.Class
		if _, ok := classRank[class]; !ok {
			class = "unrated"
		}
		privacy := m.Privacy
		if _, ok := privacyRank[privacy]; !ok {
			privacy = "sandboxed"
		}
		// Phase 1: the sandboxed level is the only one granted without attestation.
		if m.Attest == "" {
			privacy = "sandboxed"
		}
		h.st.UpdateDeviceProfile(n.ID, m.Platform, class, privacy, string(m.Bench), strings.Join(m.Models, ","))
		n.mu.Lock()
		n.Class, n.Privacy, n.Models = class, privacy, m.Models
		if m.Mode != "" {
			n.Mode = m.Mode
		}
		n.TPS = m.TPS
		n.Slots = m.Slots
		if n.Slots < 1 || n.Slots > 8 {
			n.Slots = 1
		}
		n.mu.Unlock()
		h.sendConfig(n)
	case "state":
		n.mu.Lock()
		if m.Status != "" {
			n.Status = m.Status
		}
		if m.Online != nil {
			n.Online = *m.Online
		}
		if m.Mode != "" {
			n.Mode = m.Mode
		}
		n.mu.Unlock()
	case "bye":
		n.mu.Lock()
		n.quitting = m.Reason == "owner_quit"
		n.mu.Unlock()
	default:
		if m.JobID == "" {
			return
		}
		n.mu.Lock()
		ch := n.jobs[m.JobID]
		n.mu.Unlock()
		if ch != nil {
			select {
			case ch <- m:
			default:
			}
		}
	}
}

// GET /api/admin/nodes: live state of connected devices and why each isn't taking jobs.
func (a *API) AdminNodes(w http.ResponseWriter, r *http.Request) {
	if !a.admin(w, r) {
		return
	}
	a.hub.mu.Lock()
	nodes := make([]*Node, 0, len(a.hub.nodes))
	for _, n := range a.hub.nodes {
		nodes = append(nodes, n)
	}
	a.hub.mu.Unlock()
	var out []map[string]any
	for _, n := range nodes {
		n.mu.Lock()
		v := map[string]any{"id": n.ID, "online": n.Online, "status": n.Status, "mode": n.Mode, "class": n.Class,
			"models": n.Models, "running_jobs": len(n.jobs), "slots": max(1, n.Slots), "tps": n.TPS}
		var why []string
		if !n.Online {
			why = append(why, "owner set offline")
		}
		if n.Status != "idle" {
			why = append(why, "device reports "+n.Status)
		}
		if len(n.jobs) >= max(1, n.Slots) {
			why = append(why, "all job slots busy")
		}
		if classRank[n.Class] == 0 {
			why = append(why, "unrated class")
		}
		n.mu.Unlock()
		if dev, err := a.st.Device(n.ID); err == nil {
			v["dependability"] = dev.Dependability
			v["name"] = dev.Name
			if dev.SuspendedUntil > now() {
				why = append(why, "suspended")
			}
			if b := BandFor(dev.Dependability); b.Suspended {
				why = append(why, "dependability band suspended")
			} else if b.BatchOnly {
				why = append(why, "batch jobs only (dependability)")
			}
		}
		v["not_taking_jobs_because"] = why
		out = append(out, v)
	}
	writeJSON(w, map[string]any{"nodes": out})
}
