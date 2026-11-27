package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Video Studio v0 (G34): one request in, one finished video out, made by many devices.
//
//   POST /v1/studio/videos {prompt, duration, aspect, voice, voiceover, captions, webhook_url}
//   GET  /v1/studio/videos/{id}            status, plan and per-shot progress
//   GET  /v1/studio/videos/{id}/content    the finished MP4
//
// Workflow (each step is a normal sealed job, paid to the device that runs it):
//   1. director   a chat model writes the shot list and narration (any chat device)
//   2. voice-over Kokoro reads the narration; its sentence timings become captions
//   3. shots      in parallel: AI clips on GPUs (Wan), code-rendered cards split into frame
//                 ranges across computers, or clips reused from the library
//   4. edit       one device joins the shots, fits them to the aspect ratio, adds voice and captions
// Intermediate files move device-to-device through the artifact store, not job messages.

const (
	studioFPS       = 24
	modelRender     = "infera-render"
	modelCompose    = "infera-compose"
	studioMaxShots  = 14
	studioRangeSecs = 2.0 // code-rendered shots are split into ranges this long when several devices can render
)

const studioSchema = `
CREATE TABLE IF NOT EXISTS studio_videos (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL,
  prompt TEXT NOT NULL,
  params TEXT NOT NULL,
  status TEXT NOT NULL,
  plan TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  artifact TEXT NOT NULL DEFAULT '',
  cost INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  completed_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS studio_clips (
  key TEXT PRIMARY KEY,
  artifact TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
`

func (s *Store) MigrateStudio() error {
	q := studioSchema
	if s.pg {
		q = strings.ReplaceAll(q, "INTEGER", "BIGINT")
	}
	_, err := s.db.Exec(q)
	return err
}

type studioParams struct {
	Prompt    string `json:"prompt"`
	Duration  int    `json:"duration"`
	Aspect    string `json:"aspect"`
	Voice     string `json:"voice"`
	Voiceover *bool  `json:"voiceover"`
	Captions  *bool  `json:"captions"`
	Webhook   string `json:"webhook_url"`
	Review    bool   `json:"review"` // stop after planning so the user can approve or edit the plan
	// Optional: the app's own shot list (skips the director), same shape as plan.shots.
	Shots   []studioShot `json:"shots"`
	Palette []string     `json:"palette"`
}

type studioShot struct {
	Type     string  `json:"type"` // ai | card
	Seconds  float64 `json:"seconds"`
	Prompt   string  `json:"prompt,omitempty"` // ai: what the camera sees
	Text     string  `json:"text,omitempty"`   // card: headline
	Sub      string  `json:"sub,omitempty"`    // card: smaller line
	Narrate  string  `json:"narration,omitempty"`
	Status   string  `json:"status"`
	Source   string  `json:"source,omitempty"` // generated | reused | card (fallback)
	Note     string  `json:"note,omitempty"`
	artifact []string
}

type studioPlan struct {
	Title    string       `json:"title"`
	Palette  []string     `json:"palette"` // background, text, accent (hex)
	Shots    []studioShot `json:"shots"`
	Director string       `json:"director"` // model that wrote the plan, or "template"
	Voice    string       `json:"voice_status,omitempty"`
}

type Studio struct {
	g   *Gateway
	art *Artifacts
}

func (st *Studio) dims(aspect string) (w, h int, clip string) {
	switch aspect {
	case "9:16":
		return 720, 1280, "480x832"
	case "1:1":
		return 720, 720, "832x480"
	}
	return 1280, 720, "832x480"
}

// POST /v1/studio/videos
func (st *Studio) Create(w http.ResponseWriter, r *http.Request) {
	acct := st.g.customer(w, r)
	if acct == nil {
		return
	}
	var p studioParams
	if readJSON(r, &p) != nil || strings.TrimSpace(p.Prompt) == "" {
		apiError(w, 400, "request needs \"prompt\"")
		return
	}
	if len(p.Prompt) > 2000 {
		apiError(w, 400, "prompt is limited to 2000 characters")
		return
	}
	if p.Duration == 0 {
		p.Duration = 30
	}
	if p.Duration < 10 || p.Duration > 60 {
		apiError(w, 400, "duration must be 10 to 60 seconds")
		return
	}
	if p.Aspect == "" {
		p.Aspect = "16:9"
	}
	if p.Aspect != "16:9" && p.Aspect != "9:16" && p.Aspect != "1:1" {
		apiError(w, 400, "aspect must be 16:9, 9:16 or 1:1")
		return
	}
	if p.Voice == "" {
		p.Voice = "alloy"
	}
	if est := st.estimate(p); acct.Credits < est {
		apiError(w, 402, fmt.Sprintf("not enough credits: this video can cost up to $%.4f", float64(est)/microUSD))
		return
	}
	if !st.online(modelCompose) {
		apiError(w, 503, "no device can edit videos right now (computers running the Infera media server); try again later")
		return
	}
	id := "studio_" + randomHex(12)
	raw, _ := json.Marshal(p)
	if _, err := st.g.st.exec(`INSERT INTO studio_videos(id,account_id,prompt,params,status,created_at) VALUES(?,?,?,?,?,?)`,
		id, acct.ID, p.Prompt, string(raw), "queued", now()); err != nil {
		apiError(w, 500, "could not queue the video")
		return
	}
	go st.run(id, acct, p)
	v, _ := st.view(id, acct.ID)
	writeJSON(w, v)
}

// estimate: worst case, every second is AI footage.
func (st *Studio) estimate(p studioParams) int64 {
	c := st.g.cfg
	usd := float64(p.Duration)*(c.VideoPrice+c.ComposePrice) + 1500/1e6*c.SpeechPrice + 0.002
	return int64(math.Ceil(usd * microUSD))
}

func (st *Studio) set(id string, kv map[string]any) {
	for k, v := range kv {
		st.g.st.exec(`UPDATE studio_videos SET `+k+`=? WHERE id=?`, v, id)
	}
}

func (st *Studio) savePlan(id string, plan *studioPlan, mu *sync.Mutex) {
	mu.Lock()
	b, _ := json.Marshal(plan)
	mu.Unlock()
	st.set(id, map[string]any{"plan": string(b)})
}

func (st *Studio) fail(id string, p studioParams, msg string) {
	st.set(id, map[string]any{"status": "failed", "error": msg, "completed_at": now()})
	st.notify(id, p)
}

// run is the whole workflow, in the background.
func (st *Studio) run(id string, acct *Account, p studioParams) {
	defer func() {
		if rec := recover(); rec != nil {
			st.fail(id, p, fmt.Sprintf("internal error: %v", rec))
		}
	}()
	var mu sync.Mutex
	st.set(id, map[string]any{"status": "planning"})
	var plan *studioPlan
	if len(p.Shots) > 0 {
		b, _ := json.Marshal(map[string]any{"title": leadWords(p.Prompt, 6), "palette": p.Palette, "shots": p.Shots})
		if plan = parsePlan(string(b), p.Duration); plan != nil {
			plan.Director = "provided"
		}
	}
	if plan == nil {
		plan = st.direct(acct, p)
	}
	if p.Review {
		st.savePlan(id, plan, &mu)
		st.set(id, map[string]any{"status": "awaiting_approval"})
		st.notify(id, p)
		return
	}
	st.produce(id, acct, p, plan)
}

// produce makes the video from an (approved) plan.
func (st *Studio) produce(id string, acct *Account, p studioParams, plan *studioPlan) {
	defer func() {
		if rec := recover(); rec != nil {
			st.fail(id, p, fmt.Sprintf("internal error: %v", rec))
		}
	}()
	var mu sync.Mutex
	W, H, clipSize := st.dims(p.Aspect)
	videoModel := st.videoModel()
	for i := range plan.Shots {
		if plan.Shots[i].Type == "ai" && videoModel == "" {
			plan.Shots[i] = cardFromShot(plan.Shots[i], "no GPU with a video model is online; drawn as a card")
		}
	}
	st.savePlan(id, plan, &mu)

	// 2. Voice-over first: its length sets the video's length, its sentences become captions.
	var audio string
	var captions []map[string]any
	narration := narrationOf(plan)
	if (p.Voiceover == nil || *p.Voiceover) && narration != "" {
		st.set(id, map[string]any{"status": "voicing"})
		if res, art, ok := st.speech(acct, narration, p.Voice); ok {
			audio = art
			dur, _ := res["duration"].(float64)
			if chunks, ok := res["chunks"].([]any); ok {
				for _, c := range chunks {
					if m, ok := c.(map[string]any); ok {
						captions = append(captions, m)
					}
				}
			}
			fitShots(plan, math.Max(float64(p.Duration), dur+0.6))
			plan.Voice = "done"
		} else {
			plan.Voice = "no voice device online; made without voice-over"
		}
	}
	if len(captions) == 0 && (p.Captions == nil || *p.Captions) {
		captions = timedCaptions(plan)
	}
	if p.Captions != nil && !*p.Captions {
		captions = nil
	}
	st.savePlan(id, plan, &mu)

	// 3. Shots in parallel.
	st.set(id, map[string]any{"status": "rendering"})
	renderers := len(st.g.hub.CandidatesFor(modelRender, "sandboxed", true, "studio"))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range plan.Shots {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			mu.Lock()
			shot := plan.Shots[i]
			plan.Shots[i].Status = "rendering"
			mu.Unlock()
			st.savePlan(id, plan, &mu)
			var arts []string
			var ok bool
			if shot.Type == "ai" {
				arts, ok = st.aiShot(acct, shot, videoModel, clipSize, &mu, plan, i)
				if !ok { // GPU failed: still deliver, as a card
					mu.Lock()
					plan.Shots[i] = cardFromShot(shot, "AI shot failed on every device; drawn as a card")
					shot = plan.Shots[i]
					mu.Unlock()
				}
			}
			if shot.Type == "card" {
				arts, ok = st.cardShot(acct, shot, plan.Palette, W, H, renderers)
			}
			mu.Lock()
			if ok {
				plan.Shots[i].Status = "done"
				plan.Shots[i].artifact = arts
				if plan.Shots[i].Source == "" {
					plan.Shots[i].Source = map[string]string{"ai": "generated", "card": "card"}[plan.Shots[i].Type]
				}
			} else {
				plan.Shots[i].Status = "failed"
			}
			mu.Unlock()
			st.savePlan(id, plan, &mu)
		}(i)
	}
	wg.Wait()
	var segs []map[string]any
	for _, s := range plan.Shots {
		if s.Status != "done" {
			st.fail(id, p, "some shots could not be rendered on any device; retry later")
			return
		}
		for k, a := range s.artifact {
			slot := s.Seconds / float64(len(s.artifact))
			segs = append(segs, map[string]any{"url": st.art.URL(a), "seconds": slot, "first_of_shot": k == 0})
		}
	}

	// 4. Edit.
	st.set(id, map[string]any{"status": "editing"})
	outID, outURL := st.art.New("final")
	total := 0.0
	for _, s := range plan.Shots {
		total += s.Seconds
	}
	job := map[string]any{"task": "compose", "width": W, "height": H, "fps": studioFPS, "segments": segs, "captions": captions, "output_url": outURL}
	if audio != "" {
		job["audio_url"] = st.art.URL(audio)
	}
	if _, ok := st.task(acct, modelCompose, job, st.g.mediaFixed("compose", total)); !ok || !st.art.Exists(outID) {
		st.fail(id, p, "no device could edit the video; retry later")
		return
	}
	st.set(id, map[string]any{"status": "completed", "artifact": outID, "completed_at": now()})
	st.notify(id, p)
}

// POST /v1/studio/videos/{id}/approve {shots?, palette?}: start rendering an approved plan,
// optionally with the user's edits.
func (st *Studio) Approve(w http.ResponseWriter, r *http.Request) {
	acct := st.g.customer(w, r)
	if acct == nil {
		return
	}
	id := r.PathValue("id")
	var status, params, planJSON string
	if st.g.st.queryRow(`SELECT status,params,plan FROM studio_videos WHERE id=? AND account_id=?`, id, acct.ID).Scan(&status, &params, &planJSON) != nil {
		apiError(w, 404, "no such video")
		return
	}
	if status != "awaiting_approval" {
		apiError(w, 409, "the video is "+status+", not awaiting approval")
		return
	}
	var p studioParams
	json.Unmarshal([]byte(params), &p)
	var in struct {
		Shots   []studioShot `json:"shots"`
		Palette []string     `json:"palette"`
		Title   string       `json:"title"`
	}
	readJSON(r, &in)
	plan := &studioPlan{}
	json.Unmarshal([]byte(planJSON), plan)
	if len(in.Shots) > 0 {
		pal := in.Palette
		if len(pal) == 0 {
			pal = plan.Palette
		}
		b, _ := json.Marshal(map[string]any{"title": plan.Title, "palette": pal, "shots": in.Shots})
		edited := parsePlan(string(b), p.Duration)
		if edited == nil {
			apiError(w, 400, "the edited shots are not valid: each needs a type (ai or card), and ai shots a prompt")
			return
		}
		edited.Director = plan.Director + " + edited"
		plan = edited
	}
	if in.Title != "" {
		plan.Title = clip(in.Title, 80)
	}
	if acct.Credits < st.estimate(p) {
		apiError(w, 402, "not enough credits for this video")
		return
	}
	res, err := st.g.st.exec(`UPDATE studio_videos SET status='approved' WHERE id=? AND status='awaiting_approval'`, id)
	if n, _ := res.RowsAffected(); err != nil || n == 0 {
		apiError(w, 409, "already approved")
		return
	}
	var mu sync.Mutex
	st.savePlan(id, plan, &mu)
	go st.produce(id, acct, p, plan)
	v, _ := st.view(id, acct.ID)
	writeJSON(w, v)
}

// task runs one studio step on a device serving model; returns the device's JSON. Devices
// that are busy (all job slots in use, or the GPU on another video) are waited for, up to
// 20 minutes; a step that fails on 4 devices gives up.
func (st *Studio) task(acct *Account, model string, payload map[string]any, cost int64) (map[string]any, bool) {
	raw, _ := json.Marshal(payload)
	req := chatRequest{Model: model, Privacy: "sandboxed", InferaBatch: true, jobType: "studio", fixedCost: cost}
	failures := 0
	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) && failures < 4 {
		for _, n := range st.g.hub.CandidatesFor(model, "sandboxed", true, "studio") {
			sw := newStreamWriter(&discardWriter{}, false, "job_"+randomHex(10), model)
			sw.media = "studio"
			res := st.g.runOnDevice(context.Background(), n, acct, sw.jobID, req, raw, 1, sw)
			if res == runNotAccepted {
				continue // busy or declined: free, try the next device
			}
			if res == runDone {
				var out map[string]any
				if json.Unmarshal([]byte(sw.buf.String()), &out) == nil && out["error"] == nil {
					return out, true
				}
			}
			failures++
			break
		}
		if !st.online(model) {
			return nil, false // nobody serves this at all any more
		}
		time.Sleep(2 * time.Second)
	}
	return nil, false
}

// online: some connected device serves model (busy or not).
func (st *Studio) online(model string) bool {
	st.g.hub.mu.Lock()
	defer st.g.hub.mu.Unlock()
	for _, n := range st.g.hub.nodes {
		n.mu.Lock()
		ok := n.Online && contains(n.Models, model)
		n.mu.Unlock()
		if ok {
			return true
		}
	}
	return false
}

func (st *Studio) speech(acct *Account, text, voice string) (map[string]any, string, bool) {
	model := ""
	for _, c := range catalog() {
		if c.Kind == "speech" && st.online(c.ID) {
			model = c.ID
			break
		}
	}
	if model == "" {
		return nil, "", false
	}
	id, url := st.art.New("voice")
	res, ok := st.task(acct, model, map[string]any{"task": "speech", "text": text, "voice": voice, "output_url": url},
		st.g.mediaCost("speech", len([]rune(text))))
	return res, id, ok && st.art.Exists(id)
}

func (st *Studio) videoModel() string {
	for _, c := range catalog() {
		if c.Kind == "video" && st.online(c.ID) {
			return c.ID
		}
	}
	return ""
}

// aiShot: reuse a matching clip from the library, else generate it on a GPU.
func (st *Studio) aiShot(acct *Account, shot studioShot, model, size string, mu *sync.Mutex, plan *studioPlan, i int) ([]string, bool) {
	secs := int(math.Min(5, math.Max(1, math.Round(shot.Seconds))))
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s", model, strings.ToLower(strings.TrimSpace(shot.Prompt)), secs, size)))
	key := hex.EncodeToString(h[:16])
	var art string
	if st.g.st.queryRow(`SELECT artifact FROM studio_clips WHERE key=?`, key).Scan(&art) == nil && st.art.Exists(art) {
		mu.Lock()
		plan.Shots[i].Source = "reused"
		mu.Unlock()
		return []string{art}, true
	}
	id, url := st.art.New("clip")
	_, ok := st.task(acct, model, map[string]any{"task": "clip", "prompt": shot.Prompt, "seconds": secs, "size": size, "output_url": url},
		st.g.mediaCost("video", secs))
	if !ok || !st.art.Exists(id) {
		return nil, false
	}
	st.g.st.exec(`DELETE FROM studio_clips WHERE key=?`, key)
	st.g.st.exec(`INSERT INTO studio_clips(key,artifact,created_at) VALUES(?,?,?)`, key, id, now())
	return []string{id}, true
}

// cardShot renders a code-drawn shot, split into frame ranges across devices when it helps.
func (st *Studio) cardShot(acct *Account, shot studioShot, palette []string, w, h, renderers int) ([]string, bool) {
	total := int(math.Round(shot.Seconds * studioFPS))
	parts := 1
	if renderers > 1 {
		parts = int(math.Min(float64(renderers), math.Ceil(shot.Seconds/studioRangeSecs)))
	}
	arts := make([]string, parts)
	oks := make([]bool, parts)
	var wg sync.WaitGroup
	for k := 0; k < parts; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			a, b := total*k/parts, total*(k+1)/parts
			id, url := st.art.New("render")
			scene := map[string]any{"kind": "card", "text": shot.Text, "sub": shot.Sub, "palette": palette}
			_, ok := st.task(acct, modelRender, map[string]any{"task": "render", "scene": scene, "width": w, "height": h, "fps": studioFPS,
				"frame_start": a, "frame_end": b, "total_frames": total, "output_url": url}, st.g.mediaFixed("render", float64(b-a)/studioFPS))
			arts[k], oks[k] = id, ok && st.art.Exists(id)
		}(k)
	}
	wg.Wait()
	for _, ok := range oks {
		if !ok {
			return nil, false
		}
	}
	return arts, true
}

func (st *Studio) notify(id string, p studioParams) {
	if p.Webhook == "" || !strings.HasPrefix(p.Webhook, "https://") {
		return
	}
	var acct string
	st.g.st.queryRow(`SELECT account_id FROM studio_videos WHERE id=?`, id).Scan(&acct)
	v, err := st.view(id, acct)
	if err != nil {
		return
	}
	b, _ := json.Marshal(v)
	c := &http.Client{Timeout: 10 * time.Second}
	if resp, err := c.Post(p.Webhook, "application/json", strings.NewReader(string(b))); err == nil {
		resp.Body.Close()
	}
}

type studioView struct {
	ID          string      `json:"id"`
	Object      string      `json:"object"`
	Status      string      `json:"status"`
	Prompt      string      `json:"prompt"`
	Progress    int         `json:"progress"`
	Plan        *studioPlan `json:"plan,omitempty"`
	Error       string      `json:"error,omitempty"`
	CreatedAt   int64       `json:"created_at"`
	CompletedAt int64       `json:"completed_at,omitempty"`
}

func (st *Studio) view(id, acctID string) (*studioView, error) {
	v := &studioView{Object: "studio.video"}
	var plan string
	err := st.g.st.queryRow(`SELECT id,status,prompt,plan,error,created_at,completed_at FROM studio_videos WHERE id=? AND account_id=?`, id, acctID).
		Scan(&v.ID, &v.Status, &v.Prompt, &plan, &v.Error, &v.CreatedAt, &v.CompletedAt)
	if err != nil {
		return nil, err
	}
	if plan != "" {
		v.Plan = &studioPlan{}
		json.Unmarshal([]byte(plan), v.Plan)
	}
	switch v.Status {
	case "planning":
		v.Progress = 5
	case "voicing":
		v.Progress = 15
	case "rendering":
		done := 0
		if v.Plan != nil && len(v.Plan.Shots) > 0 {
			for _, s := range v.Plan.Shots {
				if s.Status == "done" {
					done++
				}
			}
			v.Progress = 20 + 65*done/len(v.Plan.Shots)
		}
	case "editing":
		v.Progress = 90
	case "completed":
		v.Progress = 100
	}
	return v, nil
}

func (st *Studio) Get(w http.ResponseWriter, r *http.Request) {
	acct := st.g.customer(w, r)
	if acct == nil {
		return
	}
	v, err := st.view(r.PathValue("id"), acct.ID)
	if err != nil {
		apiError(w, 404, "no such video")
		return
	}
	writeJSON(w, v)
}

func (st *Studio) Content(w http.ResponseWriter, r *http.Request) {
	acct := st.g.customer(w, r)
	if acct == nil {
		return
	}
	var status, art string
	if st.g.st.queryRow(`SELECT status,artifact FROM studio_videos WHERE id=? AND account_id=?`, r.PathValue("id"), acct.ID).Scan(&status, &art) != nil {
		apiError(w, 404, "no such video")
		return
	}
	if status != "completed" {
		apiError(w, 409, "the video is "+status+"; poll GET /v1/studio/videos/{id} until it is completed")
		return
	}
	if !st.art.Exists(art) {
		apiError(w, 410, "the video has expired (kept 48 hours)")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", `inline; filename="infera-studio.mp4"`)
	http.ServeFile(w, r, st.art.Path(art))
}

// ---- planning ----

const directorPrompt = `You are the director of a short video. Write the shot list as JSON only, no other text.
Schema: {"title": string, "palette": [background hex, text hex, accent hex],
 "shots": [{"type": "ai" | "card", "seconds": number, "prompt": string, "text": string, "sub": string, "narration": string}]}
Rules:
- %d seconds in total, %d to %d shots, each 2 to 5 seconds.
- "ai" shots are filmed by an AI video model: "prompt" describes one simple, visual scene (subject, setting, camera, light). Avoid close-ups of faces and hands, text, logos.
- "card" shots are designed graphics: "text" is a short headline (max 6 words), "sub" an optional line (max 10 words). Use cards for the opening title, key facts and the final call to action.
- Start with a card, end with a card, mostly "ai" shots between.
- "narration" is what the voice-over says during that shot: one short sentence, natural spoken English.
Video request: %s /no_think`

// direct asks a chat model on the network for the plan; falls back to a template.
func (st *Studio) direct(acct *Account, p studioParams) *studioPlan {
	minShots, maxShots := max(3, p.Duration/5), min(studioMaxShots, max(4, p.Duration/2))
	if model := st.chatModel(); model != "" {
		msg, _ := json.Marshal(map[string]string{"role": "user", "content": fmt.Sprintf(directorPrompt, p.Duration, minShots, maxShots, p.Prompt)})
		req := chatRequest{Model: model, Messages: []json.RawMessage{msg}, MaxTokens: 1500, Privacy: "sandboxed", InferaBatch: true}
		raw, _ := json.Marshal(map[string]any{"model": model, "messages": []json.RawMessage{msg}, "max_tokens": 1500, "temperature": 0.7})
		for attempt := 0; attempt < 2; attempt++ {
			for _, n := range st.g.hub.CandidatesFor(model, "sandboxed", true, "batch_chat") {
				sw := newStreamWriter(&discardWriter{}, false, "job_"+randomHex(10), model)
				sw.media = "studio"
				if st.g.runOnDevice(context.Background(), n, acct, sw.jobID, req, raw, len(raw)/4, sw) == runDone {
					if plan := parsePlan(sw.buf.String(), p.Duration); plan != nil {
						plan.Director = model
						return plan
					}
				}
				break // one device per attempt
			}
		}
	}
	return templatePlan(p)
}

// chatModel: the largest general chat model online.
func (st *Studio) chatModel() string {
	cs := []CatalogModel{}
	for _, c := range catalog() {
		if c.Kind == "chat" && !strings.Contains(c.ID, "coder") {
			cs = append(cs, c)
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].ParamsB > cs[j].ParamsB })
	for _, c := range cs {
		if len(st.g.hub.CandidatesFor(c.ID, "sandboxed", true, "batch_chat")) > 0 {
			return c.ID
		}
	}
	return ""
}

func parsePlan(text string, duration int) *studioPlan {
	a, b := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if a < 0 || b <= a {
		return nil
	}
	var plan studioPlan
	if json.Unmarshal([]byte(text[a:b+1]), &plan) != nil || len(plan.Shots) < 2 {
		return nil
	}
	var shots []studioShot
	for _, s := range plan.Shots {
		s.Type = strings.ToLower(strings.TrimSpace(s.Type))
		if s.Type != "ai" && s.Type != "card" {
			continue
		}
		if s.Type == "ai" && strings.TrimSpace(s.Prompt) == "" {
			s.Type = "card"
		}
		if s.Type == "card" && strings.TrimSpace(s.Text) == "" {
			s.Text = leadWords(s.Narrate, 6)
			if s.Text == "" {
				continue
			}
		}
		s.Text, s.Sub = clip(s.Text, 60), clip(s.Sub, 90)
		s.Prompt, s.Narrate = clip(s.Prompt, 400), clip(s.Narrate, 220)
		s.Status = "queued"
		shots = append(shots, s)
		if len(shots) == studioMaxShots {
			break
		}
	}
	if len(shots) < 2 {
		return nil
	}
	plan.Shots = shots
	plan.Palette = cleanPalette(plan.Palette)
	plan.Title = clip(plan.Title, 80)
	fitShots(&plan, float64(duration))
	return &plan
}

// templatePlan: a sensible video when no chat model is online or its answer didn't parse.
var briefLead = regexp.MustCompile(`(?i)^\s*(please\s+)?((make|create|generate)\s+)?(me\s+)?((a|an)\s+)?(\d+[- ]?(second|sec|s|minute|min)s?\s+)?(short\s+)?(promo(tional)?\s+|explainer\s+|social\s+)?(ad|advert|video|clip|reel|explainer)\s+(for|about|on|of)\s+`)

func templatePlan(p studioParams) *studioPlan {
	brief := briefLead.ReplaceAllString(p.Prompt, "")
	topic, details := brief, ""
	if i := strings.IndexAny(brief, ":-–"); i > 0 {
		topic, details = brief[:i], brief[i+1:]
	}
	topic = strings.TrimSpace(topic)
	if topic != "" {
		topic = strings.ToUpper(topic[:1]) + topic[1:]
	}
	subject := leadWords(brief, 12)
	n := max(2, p.Duration/5-2)
	views := []string{"wide establishing shot", "slow push-in, soft light", "detail shot, shallow depth of field", "smooth tracking shot", "aerial view", "golden hour, cinematic"}
	plan := &studioPlan{Title: leadWords(topic, 6), Palette: cleanPalette(nil), Director: "template"}
	plan.Shots = append(plan.Shots, studioShot{Type: "card", Text: leadWords(topic, 6), Sub: leadWords(details, 10), Narrate: clip(strings.TrimSpace(brief), 200), Status: "queued"})
	for i := 0; i < n; i++ {
		plan.Shots = append(plan.Shots, studioShot{Type: "ai", Prompt: subject + ", " + views[i%len(views)], Status: "queued"})
	}
	plan.Shots = append(plan.Shots, studioShot{Type: "card", Text: "Learn more", Sub: leadWords(topic, 10), Status: "queued"})
	fitShots(plan, float64(p.Duration))
	return plan
}

func cardFromShot(s studioShot, note string) studioShot {
	text := leadWords(s.Narrate, 6)
	if text == "" {
		text = leadWords(s.Prompt, 6)
	}
	return studioShot{Type: "card", Seconds: s.Seconds, Text: text, Sub: leadWords(s.Narrate, 12), Narrate: s.Narrate, Status: "queued", Note: note}
}

// fitShots scales shot lengths to the total, keeping each between 1.5 and 8 seconds.
func fitShots(plan *studioPlan, total float64) {
	if len(plan.Shots) == 0 {
		return
	}
	sum := 0.0
	for i := range plan.Shots {
		if plan.Shots[i].Seconds <= 0 {
			plan.Shots[i].Seconds = total / float64(len(plan.Shots))
		}
		sum += plan.Shots[i].Seconds
	}
	for i := range plan.Shots {
		s := plan.Shots[i].Seconds * total / sum
		plan.Shots[i].Seconds = math.Round(math.Min(8, math.Max(1.5, s))*100) / 100
	}
}

func narrationOf(plan *studioPlan) string {
	var parts []string
	for _, s := range plan.Shots {
		if t := strings.TrimSpace(s.Narrate); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// timedCaptions: without a voice-over, show each shot's narration during that shot.
func timedCaptions(plan *studioPlan) []map[string]any {
	var out []map[string]any
	t := 0.0
	for _, s := range plan.Shots {
		if strings.TrimSpace(s.Narrate) != "" {
			out = append(out, map[string]any{"text": s.Narrate, "start": t, "end": t + s.Seconds})
		}
		t += s.Seconds
	}
	return out
}

func cleanPalette(p []string) []string {
	def := []string{"#0e1412", "#f4f7f5", "#4cc398"}
	out := make([]string, 3)
	for i := range out {
		out[i] = def[i]
		if i < len(p) {
			c := strings.TrimSpace(p[i])
			if len(c) == 7 && c[0] == '#' && strings.Trim(strings.ToLower(c[1:]), "0123456789abcdef") == "" {
				out[i] = c
			}
		}
	}
	return out
}

func leadWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) > n {
		f = f[:n]
	}
	return strings.TrimRight(strings.Join(f, " "), ".,;:")
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
