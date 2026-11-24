package main

import (
	"fmt"
	"log"
	"time"
)

// Decay (ARCHITECTURE.md §3.4): a penalty counts half after 30 days and is dropped after 90.
// Implemented by giving back half of each penalty's score at each milestone.
func (d *Dependability) DecayLoop() {
	for {
		d.DecayOnce(now())
		time.Sleep(time.Hour)
	}
}

func (d *Dependability) DecayOnce(t int64) int {
	type ev struct {
		id       int64
		device   string
		delta    float64
		created  int64
		d30, d90 int
	}
	rows, err := d.st.db.Query(`SELECT id, device_id, delta, created_at, decayed_30, decayed_90 FROM score_events
	  WHERE delta < 0 AND ((decayed_30 = 0 AND created_at <= ?) OR (decayed_90 = 0 AND created_at <= ?))`, t-30*86400, t-90*86400)
	if err != nil {
		return 0
	}
	var evs []ev
	for rows.Next() {
		var e ev
		rows.Scan(&e.id, &e.device, &e.delta, &e.created, &e.d30, &e.d90)
		evs = append(evs, e)
	}
	rows.Close()
	n := 0
	for _, e := range evs {
		back := 0.0
		stage := ""
		if e.d30 == 0 && e.created <= t-30*86400 {
			back += -e.delta / 2
			stage = "30 days"
			d.st.db.Exec(`UPDATE score_events SET decayed_30=1 WHERE id=?`, e.id)
		}
		if e.d90 == 0 && e.created <= t-90*86400 {
			back += -e.delta / 2
			stage = "90 days"
			d.st.db.Exec(`UPDATE score_events SET decayed_90=1 WHERE id=?`, e.id)
		}
		dev, err := d.st.Device(e.device)
		if err != nil || back == 0 {
			continue
		}
		d.st.SetDeviceScore(dev.ID, clamp(dev.Dependability+back, 0, 100), dev.Streak, dev.SuspendedUntil)
		d.st.AddScoreEvent(dev.ID, "", "decay", back, fmt.Sprintf("penalty from %s is %s old", time.Unix(e.created, 0).Format("2 Jan"), stage))
		n++
	}
	if n > 0 {
		log.Printf("decay: restored score on %d penalties", n)
	}
	return n
}
