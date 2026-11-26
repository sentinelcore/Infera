package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Text completions (OpenAI legacy /v1/completions), mainly for code autocomplete:
// "prompt" is the code before the cursor and "suffix" the code after it (fill in the middle).
// Editors such as Continue and Tabby call this. Billed like chat tokens.
func (g *Gateway) Completions(w http.ResponseWriter, r *http.Request) {
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
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		apiError(w, 400, "could not read request body")
		return
	}
	var in struct {
		Model       string          `json:"model"`
		Prompt      json.RawMessage `json:"prompt"`
		Suffix      string          `json:"suffix"`
		MaxTokens   int             `json:"max_tokens"`
		Temperature *float64        `json:"temperature"`
		Stream      bool            `json:"stream"`
		Stop        json.RawMessage `json:"stop"`
		Privacy     string          `json:"privacy"`
	}
	if json.Unmarshal(body, &in) != nil || in.Model == "" || len(in.Prompt) == 0 {
		apiError(w, 400, "request needs \"model\" and \"prompt\"")
		return
	}
	// prompt: a string, or a one-element array of strings.
	var prompt string
	if json.Unmarshal(in.Prompt, &prompt) != nil {
		var arr []string
		if json.Unmarshal(in.Prompt, &arr) != nil || len(arr) != 1 {
			apiError(w, 400, "prompt must be a string (or an array with one string)")
			return
		}
		prompt = arr[0]
	}
	if in.MaxTokens <= 0 {
		in.MaxTokens = 128
	}
	if in.Privacy == "" {
		in.Privacy = "sandboxed"
	}
	if _, ok := privacyRank[in.Privacy]; !ok {
		apiError(w, 400, "privacy must be sandboxed, sealed or confidential")
		return
	}
	payload := map[string]any{"prompt": prompt, "max_tokens": in.MaxTokens}
	if in.Suffix != "" {
		payload["suffix"] = in.Suffix
	}
	if in.Temperature != nil {
		payload["temperature"] = *in.Temperature
	}
	if len(in.Stop) > 0 && string(in.Stop) != "null" {
		payload["stop"] = in.Stop
	}
	raw, _ := json.Marshal(payload)
	req := chatRequest{Model: in.Model, Stream: in.Stream, MaxTokens: in.MaxTokens, Privacy: in.Privacy, Temperature: in.Temperature, jobType: "completion"}
	estIn := (len(prompt) + len(in.Suffix)) / 4
	estCharge := g.cfg.cost(req.Model, estIn, req.MaxTokens)
	if acct.Credits < estCharge {
		apiError(w, 402, fmt.Sprintf("not enough credits: this request can cost up to $%.4f, balance is $%.4f", float64(estCharge)/microUSD, float64(acct.Credits)/microUSD))
		return
	}
	g.serve(w, r, acct, req, raw, estIn, estCharge, true)
}
