package main

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"time"
)

// Partner usage reports: per account and month, requests, tokens / audio seconds and
// charges per model. Marketplaces that pay by invoice (e.g. OpenRouter) get one of these
// monthly. GET /api/admin/usage?account_id=…&month=2026-11[&format=csv]

type UsageLine struct {
	Model     string  `json:"model"`
	Requests  int64   `json:"requests"`
	TokensIn  int64   `json:"tokens_in"` // audio seconds for transcription models
	TokensOut int64   `json:"tokens_out"`
	ChargeUSD float64 `json:"charge_usd"`
}

func (s *Store) Usage(accountID string, from, to int64) []UsageLine {
	rows, err := s.query(`SELECT model, COUNT(*), CAST(COALESCE(SUM(tokens_in),0) AS BIGINT), CAST(COALESCE(SUM(tokens_out),0) AS BIGINT), CAST(COALESCE(SUM(charge),0) AS BIGINT)
	  FROM jobs WHERE customer_id=? AND started_at>=? AND started_at<? AND charge>0 GROUP BY model ORDER BY model`, accountID, from, to)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []UsageLine
	for rows.Next() {
		var u UsageLine
		var c int64
		rows.Scan(&u.Model, &u.Requests, &u.TokensIn, &u.TokensOut, &c)
		u.ChargeUSD = float64(c) / microUSD
		out = append(out, u)
	}
	return out
}

func (a *API) AdminUsage(w http.ResponseWriter, r *http.Request) {
	if !a.admin(w, r) {
		return
	}
	acct := r.URL.Query().Get("account_id")
	month := r.URL.Query().Get("month")
	if month == "" {
		month = time.Now().UTC().Format("2006-01")
	}
	start, err := time.Parse("2006-01", month)
	if acct == "" || err != nil {
		apiError(w, 400, "account_id and month=YYYY-MM are required")
		return
	}
	lines := a.st.Usage(acct, start.Unix(), start.AddDate(0, 1, 0).Unix())
	total := 0.0
	for _, l := range lines {
		total += l.ChargeUSD
	}
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="infera-usage-%s-%s.csv"`, acct, month))
		cw := csv.NewWriter(w)
		cw.Write([]string{"month", "model", "requests", "tokens_in_or_audio_seconds", "tokens_out", "charge_usd"})
		for _, l := range lines {
			cw.Write([]string{month, l.Model, fmt.Sprint(l.Requests), fmt.Sprint(l.TokensIn), fmt.Sprint(l.TokensOut), fmt.Sprintf("%.6f", l.ChargeUSD)})
		}
		cw.Write([]string{month, "TOTAL", "", "", "", fmt.Sprintf("%.6f", total)})
		cw.Flush()
		return
	}
	writeJSON(w, map[string]any{"account_id": acct, "month": month, "lines": lines, "total_usd": total})
}
