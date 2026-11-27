package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Audio and video generation (G33), OpenAI-compatible:
//
//   POST /v1/audio/speech              text to speech (Kokoro …), answers with the audio file
//   POST /v1/videos                    text to video (Wan 2.1 …), asynchronous like OpenAI's
//   GET  /v1/videos/{id}               status: queued | in_progress | completed | failed
//   GET  /v1/videos/{id}/content       the MP4 once completed (kept 24 hours)
//
// Both run on desktops with a GPU through the node's media server, sealed like any other job.
// Speech is billed per 1M input characters, video per second of output.

const videoKeep = 24 * 3600

const videoSchema = `
CREATE TABLE IF NOT EXISTS videos (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL,
  model TEXT NOT NULL,
  prompt TEXT NOT NULL,
  seconds INTEGER NOT NULL,
  size TEXT NOT NULL,
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  completed_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS videos_created ON videos(created_at);
`

func (s *Store) MigrateVideos() error {
	q := videoSchema
	if s.pg {
		q = strings.ReplaceAll(q, "INTEGER", "BIGINT")
	}
	_, err := s.db.Exec(q)
	return err
}

// discardWriter lets a background job reuse the streamWriter without a customer connection.
type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header {
	if d.h == nil {
		d.h = http.Header{}
	}
	return d.h
}
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardWriter) WriteHeader(int)             {}

func (g *Gateway) customer(w http.ResponseWriter, r *http.Request) *Account {
	acct, err := g.st.AccountByKey(bearer(r))
	if err != nil || acct.Kind != "customer" {
		apiError(w, 401, "missing or invalid API key; create one on the Developers tab of the dashboard")
		return nil
	}
	return acct
}

// runMedia offers a speech or video job to up to 5 devices serving the model.
func (g *Gateway) runMedia(r *http.Request, acct *Account, req chatRequest, payload []byte, est int, sw *streamWriter) bool {
	tried := 0
	go g.hub.NotifyMissed(req.Model, float64(g.jobCost(req, est, 0))*g.cfg.OwnerShare/microUSD)
	for _, n := range g.hub.CandidatesFor(req.Model, req.Privacy, true, req.jobType) {
		if tried >= 5 {
			break
		}
		tried++
		res := g.runOnDevice(r.Context(), n, acct, sw.jobID, req, payload, est, sw)
		if res == runDone || res == runClientGone {
			return true
		}
		sw.reset("job_" + randomHex(10))
	}
	return false
}

// POST /v1/audio/speech {model, input, voice, response_format, speed}
func (g *Gateway) Speech(w http.ResponseWriter, r *http.Request) {
	acct := g.customer(w, r)
	if acct == nil {
		return
	}
	release := g.limit.Acquire(w, acct.ID)
	if release == nil {
		return
	}
	defer release()
	var in struct {
		Model  string  `json:"model"`
		Input  string  `json:"input"`
		Voice  string  `json:"voice"`
		Format string  `json:"response_format"`
		Speed  float64 `json:"speed"`
	}
	if readJSON(r, &in) != nil || in.Model == "" || strings.TrimSpace(in.Input) == "" {
		apiError(w, 400, "request needs \"model\" and \"input\"")
		return
	}
	if len(in.Input) > 4096 {
		apiError(w, 400, "input is limited to 4096 characters")
		return
	}
	if in.Format == "" {
		in.Format = "mp3"
	}
	if !strings.Contains(" mp3 wav opus flac aac pcm ", " "+in.Format+" ") {
		apiError(w, 400, "response_format must be mp3, wav, opus, flac, aac or pcm")
		return
	}
	if in.Speed == 0 {
		in.Speed = 1
	}
	if in.Speed < 0.25 || in.Speed > 4 {
		apiError(w, 400, "speed must be between 0.25 and 4")
		return
	}
	payload, _ := json.Marshal(map[string]any{"input": in.Input, "voice": in.Voice, "response_format": in.Format, "speed": in.Speed})
	req := chatRequest{Model: in.Model, Privacy: "sandboxed", InferaBatch: true, jobType: "speech"}
	chars := len([]rune(in.Input))
	if est := g.jobCost(req, chars, 0); acct.Credits < est {
		apiError(w, 402, fmt.Sprintf("not enough credits: this request can cost up to $%.6f", float64(est)/microUSD))
		return
	}
	sw := newStreamWriter(w, false, "job_"+randomHex(10), in.Model)
	sw.media = "speech"
	if !g.runMedia(r, acct, req, payload, chars, sw) {
		apiError(w, 503, fmt.Sprintf("no device is serving speech model %q right now; GET /v1/models lists what's online", in.Model))
	}
}

// writeSpeech answers with the audio file the device made.
func (s *streamWriter) writeSpeech() {
	var res struct {
		Audio  string `json:"audio_b64"`
		Format string `json:"format"`
	}
	json.Unmarshal([]byte(s.buf.String()), &res)
	audio, err := base64.StdEncoding.DecodeString(res.Audio)
	if err != nil || len(audio) == 0 {
		apiError(s.w, 502, "the device returned no audio; retry the request")
		return
	}
	types := map[string]string{"mp3": "audio/mpeg", "wav": "audio/wav", "opus": "audio/ogg", "flac": "audio/flac", "aac": "audio/aac", "pcm": "audio/pcm"}
	s.begin()
	ct := types[res.Format]
	if ct == "" {
		ct = "application/octet-stream"
	}
	s.w.Header().Set("Content-Type", ct)
	s.w.Write(audio)
}

type videoView struct {
	ID          string         `json:"id"`
	Object      string         `json:"object"`
	Model       string         `json:"model"`
	Status      string         `json:"status"`
	Progress    int            `json:"progress"`
	Prompt      string         `json:"prompt"`
	Seconds     string         `json:"seconds"`
	Size        string         `json:"size"`
	CreatedAt   int64          `json:"created_at"`
	CompletedAt *int64         `json:"completed_at"`
	ExpiresAt   *int64         `json:"expires_at"`
	Error       map[string]any `json:"error"`
}

func (s *Store) video(id, acctID string) (*videoView, error) {
	v := &videoView{Object: "video"}
	var sec, done int64
	var errMsg string
	err := s.queryRow(`SELECT id,model,prompt,seconds,size,status,error,created_at,completed_at FROM videos WHERE id=? AND account_id=?`, id, acctID).
		Scan(&v.ID, &v.Model, &v.Prompt, &sec, &v.Size, &v.Status, &errMsg, &v.CreatedAt, &done)
	if err != nil {
		return nil, err
	}
	v.Seconds = strconv.FormatInt(sec, 10)
	switch v.Status {
	case "completed":
		v.Progress = 100
		exp := done + videoKeep
		v.CompletedAt, v.ExpiresAt = &done, &exp
	case "failed":
		v.Error = map[string]any{"code": "video_generation_failed", "message": errMsg}
	case "in_progress":
		v.Progress = 50
	}
	return v, nil
}

// POST /v1/videos {model, prompt, seconds, size} (JSON or multipart form, like OpenAI's API)
func (g *Gateway) CreateVideo(w http.ResponseWriter, r *http.Request) {
	acct := g.customer(w, r)
	if acct == nil {
		return
	}
	var in struct {
		Model    string `json:"model"`
		Prompt   string `json:"prompt"`
		Seconds  any    `json:"seconds"`
		Size     string `json:"size"`
		Negative string `json:"negative_prompt"`
		Seed     *int64 `json:"seed"`
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		r.ParseMultipartForm(1 << 20)
		in.Model, in.Prompt, in.Seconds, in.Size, in.Negative = r.FormValue("model"), r.FormValue("prompt"), r.FormValue("seconds"), r.FormValue("size"), r.FormValue("negative_prompt")
	} else if readJSON(r, &in) != nil {
		apiError(w, 400, "send JSON with \"model\" and \"prompt\"")
		return
	}
	if in.Model == "" || strings.TrimSpace(in.Prompt) == "" {
		apiError(w, 400, "request needs \"model\" and \"prompt\"")
		return
	}
	sec := 5
	switch s := in.Seconds.(type) {
	case string:
		if s != "" {
			sec, _ = strconv.Atoi(s)
		}
	case float64:
		sec = int(s)
	}
	if sec < 1 || sec > 5 {
		apiError(w, 400, "seconds must be 1 to 5")
		return
	}
	if in.Size == "" {
		in.Size = "832x480"
	}
	if in.Size != "832x480" && in.Size != "480x832" {
		apiError(w, 400, "size must be 832x480 (landscape) or 480x832 (portrait)")
		return
	}
	req := chatRequest{Model: in.Model, Privacy: "sandboxed", InferaBatch: true, jobType: "video"}
	if est := g.jobCost(req, sec, 0); acct.Credits < est {
		apiError(w, 402, fmt.Sprintf("not enough credits: this video can cost up to $%.4f", float64(est)/microUSD))
		return
	}
	if len(g.hub.CandidatesFor(in.Model, "sandboxed", true, "video")) == 0 {
		apiError(w, 503, fmt.Sprintf("no device is serving video model %q right now; GET /v1/models lists what's online", in.Model))
		return
	}
	payload, _ := json.Marshal(map[string]any{"prompt": in.Prompt, "seconds": sec, "size": in.Size, "negative_prompt": in.Negative, "seed": in.Seed})
	id := "video_" + randomHex(12)
	g.st.exec(`DELETE FROM videos WHERE created_at < ?`, now()-videoKeep-3600)
	if _, err := g.st.exec(`INSERT INTO videos(id,account_id,model,prompt,seconds,size,status,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		id, acct.ID, in.Model, in.Prompt, sec, in.Size, "queued", now()); err != nil {
		apiError(w, 500, "could not queue the video")
		return
	}
	go g.makeVideo(id, acct, req, payload, sec)
	v, _ := g.st.video(id, acct.ID)
	writeJSON(w, v)
}

func (g *Gateway) makeVideo(id string, acct *Account, req chatRequest, payload []byte, sec int) {
	g.st.exec(`UPDATE videos SET status='in_progress' WHERE id=?`, id)
	sw := newStreamWriter(&discardWriter{}, false, "job_"+randomHex(10), req.Model)
	sw.media = "video"
	r, _ := http.NewRequest("POST", "/v1/videos", nil)
	if !g.runMedia(r, acct, req, payload, sec, sw) || sw.buf.Len() == 0 {
		g.st.exec(`UPDATE videos SET status='failed', error=?, completed_at=? WHERE id=?`, "no device could make this video; retry later", now(), id)
		return
	}
	var res struct {
		Video string `json:"video_b64"`
	}
	json.Unmarshal([]byte(sw.buf.String()), &res)
	if res.Video == "" {
		g.st.exec(`UPDATE videos SET status='failed', error=?, completed_at=? WHERE id=?`, "the device returned no video", now(), id)
		return
	}
	g.st.exec(`UPDATE videos SET status='completed', content=?, completed_at=? WHERE id=?`, res.Video, now(), id)
}

// GET /v1/videos/{id}
func (g *Gateway) GetVideo(w http.ResponseWriter, r *http.Request) {
	acct := g.customer(w, r)
	if acct == nil {
		return
	}
	v, err := g.st.video(r.PathValue("id"), acct.ID)
	if err != nil {
		apiError(w, 404, "no such video")
		return
	}
	writeJSON(w, v)
}

// GET /v1/videos/{id}/content
func (g *Gateway) VideoContent(w http.ResponseWriter, r *http.Request) {
	acct := g.customer(w, r)
	if acct == nil {
		return
	}
	var status, content string
	if g.st.queryRow(`SELECT status,content FROM videos WHERE id=? AND account_id=?`, r.PathValue("id"), acct.ID).Scan(&status, &content) != nil {
		apiError(w, 404, "no such video")
		return
	}
	if status != "completed" {
		apiError(w, 409, "the video is "+status+"; poll GET /v1/videos/{id} until it is completed")
		return
	}
	b, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		apiError(w, 500, "stored video is damaged")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	io.Copy(w, strings.NewReader(string(b)))
}

// mediaCost: speech per 1M characters, video per second.
func (g *Gateway) mediaCost(jobType string, units int) int64 {
	if jobType == "video" {
		return int64(math.Ceil(float64(units) * g.cfg.VideoPrice * microUSD))
	}
	return int64(math.Ceil(float64(units) / 1e6 * g.cfg.SpeechPrice * microUSD))
}

// mediaFixed: studio render / compose steps, per second of output.
func (g *Gateway) mediaFixed(task string, seconds float64) int64 {
	p := g.cfg.RenderPrice
	if task == "compose" {
		p = g.cfg.ComposePrice
	}
	return int64(math.Ceil(math.Max(seconds, 1) * p * microUSD))
}

// jobTimeout: video takes minutes before any output arrives.
func (g *Gateway) jobTimeout(req chatRequest) (first, deadline time.Duration) {
	if req.jobType == "video" || req.jobType == "studio" {
		return 25 * time.Minute, 30 * time.Minute
	}
	return g.cfg.FirstToken, g.cfg.JobDeadline
}
