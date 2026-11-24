package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// Live speed (ARCHITECTURE.md §3.1): the server tracks each device's real generation speed
// from finished jobs and moves a device down a class when it delivers well below the speed
// its own benchmark claimed. Only chat jobs with at least 50 output tokens count.

var classOrder = []string{"unrated", "M2", "M1", "C", "B", "A"}

// classFor mirrors the node's thresholds (agent/src/engine.rs classify, Android phoneClass).
func classFor(tps, paramsB, vramGB, ramGB float64, gpu, platform string) string {
	if len(platform) >= 7 && platform[:7] == "android" {
		switch {
		case tps >= 8 && ramGB >= 7.5:
			return "M1"
		case tps >= 3:
			return "M2"
		}
		return "unrated"
	}
	apple := len(gpu) >= 5 && gpu[:5] == "Apple"
	bigMem := vramGB >= 16 || (apple && ramGB >= 32)
	switch {
	case paramsB >= 7 && tps >= 40 && bigMem:
		return "A"
	case paramsB >= 7 && tps >= 25:
		return "B"
	case tps >= 10:
		return "C"
	}
	return "unrated"
}

func classRankOf(c string) int {
	for i, x := range classOrder {
		if x == c {
			return i
		}
	}
	return 0
}

// RecordSpeed updates the device's live speed (moving average) and demotes it if needed.
func (g *Gateway) RecordSpeed(dev *DeviceRow, tokensOut int, elapsed time.Duration) {
	if tokensOut < 50 || elapsed <= 0 {
		return
	}
	tps := float64(tokensOut) / elapsed.Seconds()
	var live float64
	var n int
	g.st.queryRow(`SELECT live_tps, live_n FROM devices WHERE id=?`, dev.ID).Scan(&live, &n)
	if n == 0 {
		live = tps
	} else {
		live = 0.7*live + 0.3*tps
	}
	n++
	g.st.exec(`UPDATE devices SET live_tps=?, live_n=? WHERE id=?`, live, n, dev.ID)

	var b struct {
		TPS     float64 `json:"tokens_per_s"`
		ParamsB float64 `json:"params_b"`
		VRAM    float64 `json:"vram_gb"`
		RAM     float64 `json:"ram_gb"`
		GPU     string  `json:"gpu"`
	}
	json.Unmarshal([]byte(dev.Bench), &b)
	if n < 5 || b.TPS <= 0 || live >= 0.8*b.TPS {
		return
	}
	newClass := classFor(live, b.ParamsB, b.VRAM, b.RAM, b.GPU, dev.Platform)
	if classRankOf(newClass) >= classRankOf(dev.Class) {
		return
	}
	g.st.exec(`UPDATE devices SET class=? WHERE id=?`, newClass, dev.ID)
	g.st.AddScoreEvent(dev.ID, "", "class_change", 0, fmt.Sprintf("moved from class %s to %s: real speed %.1f tok/s vs %.1f claimed", dev.Class, newClass, live, b.TPS))
	log.Printf("device %s moved from class %s to %s (live %.1f vs bench %.1f tok/s)", dev.ID, dev.Class, newClass, live, b.TPS)
	if node := g.hub.Node(dev.ID); node != nil {
		node.mu.Lock()
		node.Class = newClass
		node.mu.Unlock()
		g.hub.sendConfig(node)
	}
}
