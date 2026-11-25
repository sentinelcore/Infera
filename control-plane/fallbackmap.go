package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Backup cloud model mapping: which of our model IDs the rented provider can serve, and under
// which of its names. Built at startup from the provider's own /models list (matched to each
// catalog model's Hugging Face id), refreshed hourly, and overridable with
// INFERA_FALLBACK_MODELS='{"qwen3-8b":"Qwen/Qwen3-8B", ...}'.
type fallbackMap struct {
	mu sync.RWMutex
	m  map[string]string
}

var backup = &fallbackMap{m: map[string]string{}}

func norm(id string) string {
	id = strings.ToLower(id)
	id = strings.ReplaceAll(id, "meta-llama-3.1", "llama-3.1")
	return id
}

func (g *Gateway) RunFallbackMap() {
	if g.cfg.FallbackURL == "" {
		return
	}
	for {
		m := map[string]string{}
		// Provider's list.
		req, _ := http.NewRequest("GET", strings.TrimRight(g.cfg.FallbackURL, "/")+"/models", nil)
		if g.cfg.FallbackKey != "" {
			req.Header.Set("Authorization", "Bearer "+g.cfg.FallbackKey)
		}
		c := &http.Client{Timeout: 20 * time.Second}
		if resp, err := c.Do(req); err == nil {
			var l struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			json.NewDecoder(resp.Body).Decode(&l)
			resp.Body.Close()
			have := map[string]string{}
			for _, d := range l.Data {
				have[norm(d.ID)] = d.ID
			}
			for _, cm := range catalog() {
				if cm.HFID == "" {
					continue
				}
				if id, ok := have[norm(cm.HFID)]; ok {
					m[cm.ID] = id
				}
			}
		} else {
			log.Printf("backup cloud: model list unavailable: %v", err)
		}
		// Manual overrides win.
		if s := os.Getenv("INFERA_FALLBACK_MODELS"); s != "" {
			var o map[string]string
			if json.Unmarshal([]byte(s), &o) == nil {
				for k, v := range o {
					m[k] = v
				}
			}
		}
		backup.mu.Lock()
		backup.m = m
		backup.mu.Unlock()
		log.Printf("backup cloud covers %d models: %v", len(m), m)
		time.Sleep(time.Hour)
	}
}

// fallbackFor returns the provider's model name for one of ours, if the backup serves it.
func (g *Gateway) fallbackFor(model string) (string, bool) {
	if g.cfg.FallbackURL == "" {
		return "", false
	}
	backup.mu.RLock()
	id, ok := backup.m[model]
	backup.mu.RUnlock()
	if ok {
		return id, true
	}
	if g.cfg.FallbackModel != "" { // legacy: one model for everything
		return g.cfg.FallbackModel, true
	}
	return "", false
}

func backupCovers() map[string]string {
	backup.mu.RLock()
	defer backup.mu.RUnlock()
	out := map[string]string{}
	for k, v := range backup.m {
		out[k] = v
	}
	return out
}
