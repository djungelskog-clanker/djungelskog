package main

import (
	"sort"
	"time"
)

// costs keeps a running tally of what claude turns cost (api-equivalent usd,
// as reported by the cli), per berlin day, per person and per thread.
type costDay struct {
	USD     float64            `json:"usd"`
	Turns   int                `json:"turns"`
	ByUser  map[string]float64 `json:"by_user"`
	ByThrd  map[string]float64 `json:"by_thread"`
	Updated time.Time          `json:"updated"`
}

type costData map[string]*costDay // "2006-01-02" -> day

// turnCost turns the cli's cumulative per-process cost into this turn's cost.
func turnCost(cumulative, last float64) float64 {
	if cumulative >= last {
		return cumulative - last
	}
	return cumulative // new process
}

func recordCost(s *jsonStore[costData], now time.Time, user, thread string, usd float64) {
	if usd <= 0 {
		return
	}
	day := now.In(berlin).Format("2006-01-02")
	_ = s.Update(func(v costData) costData {
		if v == nil {
			v = costData{}
		}
		d := v[day]
		if d == nil {
			d = &costDay{ByUser: map[string]float64{}, ByThrd: map[string]float64{}}
			v[day] = d
		}
		d.USD += usd
		d.Turns++
		if user != "" {
			d.ByUser[user] += usd
		}
		d.ByThrd[thread] += usd
		d.Updated = now
		// keep ~3 months
		if len(v) > 95 {
			var days []string
			for k := range v {
				days = append(days, k)
			}
			sort.Strings(days)
			for _, k := range days[:len(days)-95] {
				delete(v, k)
			}
		}
		return v
	})
}

// spentSince sums the days from `from` (inclusive) to today.
func spentSince(v costData, from time.Time) (usd float64, turns int) {
	start := from.In(berlin).Format("2006-01-02")
	for day, d := range v {
		if day >= start {
			usd += d.USD
			turns += d.Turns
		}
	}
	return
}
