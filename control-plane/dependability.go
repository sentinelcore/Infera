package main

import (
	"fmt"
	"math"
	"time"
)

// Abort causes (ARCHITECTURE.md §3.4).
const (
	CauseOwnerAction   = "owner_action"   // app quit, "Stop now", shutdown, sleep, went offline
	CauseNotDelivered  = "not_delivered"  // accepted but no first token / missed deadline
	CauseDeviceFailure = "device_failure" // connection drop, crash, out of memory, heat cut-off
	CauseWrongResult   = "wrong_result"   // failed verification
	CauseNoFault       = "no_fault"       // our outage, customer cancelled
)

type Band struct {
	Name       string  `json:"name"`
	HoldDays   int     `json:"hold_days"`
	Multiplier float64 `json:"multiplier"`
	BatchOnly  bool    `json:"batch_only"`
	Suspended  bool    `json:"suspended"`
}

func BandFor(score float64) Band {
	switch {
	case score >= 90:
		return Band{"90–100", 3, 1.00, false, false}
	case score >= 70:
		return Band{"70–89", 7, 0.95, false, false}
	case score >= 50:
		return Band{"50–69", 14, 0.85, true, false}
	case score >= 30:
		return Band{"30–49", 30, 0.70, true, false}
	default:
		return Band{"0–29", 0, 0, true, true}
	}
}

type penaltyRule struct {
	debitMultiple float64
	scoreDelta    float64
}

var penaltyRules = map[string]penaltyRule{
	CauseOwnerAction:   {2, -10},
	CauseNotDelivered:  {1, -8},
	CauseDeviceFailure: {1, -5},
	CauseWrongResult:   {3, -15},
	CauseNoFault:       {0, 0},
}

// Dependability applies rewards and penalties to devices and writes them to the ledger.
type Dependability struct{ st *Store }

// Penalize applies the penalty for a job that stopped midway. jobValue is the owner's
// expected pay for the job (micro-USD); the job pay itself is always forfeited by the caller.
func (d *Dependability) Penalize(dev *DeviceRow, jobID, cause string, jobValue int64, evidence string) {
	rule, ok := penaltyRules[cause]
	if !ok || cause == CauseNoFault {
		d.st.AddScoreEvent(dev.ID, jobID, CauseNoFault, 0, evidence)
		return
	}
	t := now()
	debit := int64(math.Round(float64(jobValue) * rule.debitMultiple))
	if cause == CauseDeviceFailure {
		weekly := d.st.CountScoreEvents(dev.ID, CauseDeviceFailure, t-7*24*3600)
		firstWeek := t-dev.CreatedAt < 7*24*3600
		if weekly < 2 || firstWeek {
			debit = 0 // first two device failures a week, and the first week, are not debited
		}
	}
	if debit > 0 {
		d.st.AddLedger(dev.OwnerID, dev.ID, jobID, "debit", -debit, 0, fmt.Sprintf("%s: %s", cause, evidence))
	}
	score := clamp(dev.Dependability+rule.scoreDelta, 0, 100)
	d.st.AddScoreEvent(dev.ID, jobID, cause, rule.scoreDelta, evidence)

	suspendedUntil := dev.SuspendedUntil
	if cause == CauseOwnerAction && d.st.CountScoreEvents(dev.ID, CauseOwnerAction, t-24*3600) >= 3 {
		suspendedUntil = t + 24*3600
		d.st.AddScoreEvent(dev.ID, jobID, "suspension", 0, "3 owner aborts in 24 hours: suspended 24 hours")
	}
	if score < 30 {
		suspendedUntil = t + 72*3600
		score = 30 // back on probation at 30 after the suspension
		d.st.AddScoreEvent(dev.ID, jobID, "suspension", 0, "score below 30: suspended 72 hours, payouts frozen")
	}
	d.st.SetDeviceScore(dev.ID, score, 0, suspendedUntil)
}

// Reward credits a completed job: +1 (+0.5 for jobs under a minute), max +5 a day,
// plus +3 for every 50 jobs in a row without an abort.
func (d *Dependability) Reward(dev *DeviceRow, jobID string, dur time.Duration) {
	t := now()
	dayStart := time.Now().Truncate(24 * time.Hour).Unix()
	delta := 1.0
	if dur < time.Minute {
		delta = 0.5
	}
	already := d.st.SumScoreDelta(dev.ID, "completed", dayStart)
	if already+delta > 5 {
		delta = math.Max(0, 5-already)
	}
	streak := dev.Streak + 1
	score := dev.Dependability + delta
	d.st.AddScoreEvent(dev.ID, jobID, "completed", delta, "")
	if streak%50 == 0 {
		score += 3
		d.st.AddScoreEvent(dev.ID, jobID, "streak_bonus", 3, fmt.Sprintf("%d jobs in a row", streak))
	}
	_ = t
	d.st.SetDeviceScore(dev.ID, clamp(score, 0, 100), streak, dev.SuspendedUntil)
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
