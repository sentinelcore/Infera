package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Transcription API (G11): OpenAI-compatible POST /v1/audio/transcriptions (multipart:
// file, model, optional language, prompt, response_format json|text|verbose_json).
// The audio is sealed to one device like any other job; the device runs Whisper
// (whisper.cpp server, faster-whisper, …) and returns the text. Billed per audio minute.

const maxAudioBytes = 25 << 20 // same limit as OpenAI

func (g *Gateway) Transcriptions(w http.ResponseWriter, r *http.Request) {
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
	r.Body = http.MaxBytesReader(w, r.Body, maxAudioBytes+1<<20)
	if err := r.ParseMultipartForm(maxAudioBytes + 1<<20); err != nil {
		apiError(w, 400, "send multipart/form-data with \"file\" (audio, up to 25 MB) and \"model\"")
		return
	}
	model := r.FormValue("model")
	f, hdr, err := r.FormFile("file")
	if err != nil || model == "" {
		apiError(w, 400, "request needs \"file\" and \"model\"")
		return
	}
	audio, err := io.ReadAll(io.LimitReader(f, maxAudioBytes+1))
	f.Close()
	if err != nil || len(audio) == 0 || len(audio) > maxAudioBytes {
		apiError(w, 400, "audio file is empty or larger than 25 MB")
		return
	}
	format := r.FormValue("response_format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "text" && format != "verbose_json" {
		apiError(w, 400, "response_format must be json, text or verbose_json")
		return
	}
	privacy := r.FormValue("privacy")
	if privacy == "" {
		privacy = "sandboxed"
	}
	if _, ok := privacyRank[privacy]; !ok {
		apiError(w, 400, "privacy must be sandboxed, sealed or confidential")
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"audio_b64": base64.StdEncoding.EncodeToString(audio),
		"filename":  hdr.Filename,
		"language":  r.FormValue("language"),
		"prompt":    r.FormValue("prompt"),
	})
	req := chatRequest{Model: model, Privacy: privacy, InferaBatch: true, jobType: "transcription"}
	estSec := estimateAudioSeconds(len(audio))
	if est := g.jobCost(req, estSec, 0); acct.Credits < est {
		apiError(w, 402, fmt.Sprintf("not enough credits: this request can cost up to $%.6f", float64(est)/microUSD))
		return
	}
	jobID := "job_" + randomHex(10)
	sw := newStreamWriter(w, false, jobID, model)
	sw.transcribe = format
	tried := 0
	go g.hub.NotifyMissed(model, float64(g.jobCost(req, estSec, 0))*g.cfg.OwnerShare/microUSD)
	for _, n := range g.hub.CandidatesFor(model, privacy, true, "transcription") {
		if tried >= 5 {
			break
		}
		tried++
		res := g.runOnDevice(r.Context(), n, acct, jobID, req, payload, estSec, sw)
		if res == runDone || res == runClientGone {
			return
		}
		jobID = "job_" + randomHex(10)
		sw.reset(jobID)
	}
	apiError(w, 503, fmt.Sprintf("no device is serving transcription model %q right now; GET /v1/models lists what's online", model))
}

// estimateAudioSeconds is a ceiling for the credit check: assumes compressed audio at
// 64 kbit/s or more (8 kB per second); the device reports the real duration.
func estimateAudioSeconds(bytes int) int {
	s := bytes / 8000
	if s < 1 {
		s = 1
	}
	return s
}

// writeTranscription answers in the format the customer asked for. out is the device's
// result: {"text": "...", "duration": seconds, ...}.
func (s *streamWriter) writeTranscription(seconds int) {
	var res map[string]any
	json.Unmarshal([]byte(s.buf.String()), &res)
	text, _ := res["text"].(string)
	s.begin()
	switch s.transcribe {
	case "text":
		s.w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(s.w, strings.TrimSpace(text))
	case "verbose_json":
		s.w.Header().Set("Content-Type", "application/json")
		out := map[string]any{"task": "transcribe", "text": strings.TrimSpace(text), "duration": seconds}
		for _, k := range []string{"language", "segments"} {
			if v, ok := res[k]; ok {
				out[k] = v
			}
		}
		json.NewEncoder(s.w).Encode(out)
	default:
		s.w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(s.w).Encode(map[string]any{"text": strings.TrimSpace(text)})
	}
}
