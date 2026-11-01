package main

import (
	"fmt"
	"time"
)

// screenTime caps how many messages a user sends per day (berlin time), at
// their own request. limits live in data/screentime.json so they can be
// changed without touching .env. over the cap, messages still reach claude
// but with a note to only answer code questions.
type screenTimeData struct {
	Limits map[string]int        `json:"limits"`
	Counts map[string]dailyCount `json:"counts"`
}

type dailyCount struct {
	Day string `json:"day"`
	N   int    `json:"n"`
}

var berlin = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Berlin"); err == nil {
		return l
	}
	return time.Local
}()

func openScreenTime(path string) (*jsonStore[screenTimeData], error) {
	return openStore[screenTimeData](path)
}

// countScreenTime bumps user's count for today and returns the note to append
// to their message, or "" if they have no limit or are still under it.
func countScreenTime(s *jsonStore[screenTimeData], user string, now time.Time) string {
	limit, ok := s.Get().Limits[user]
	if !ok {
		return ""
	}
	day := now.In(berlin).Format("2006-01-02")
	var n int
	_ = s.Update(func(v screenTimeData) screenTimeData {
		if v.Counts == nil {
			v.Counts = map[string]dailyCount{}
		}
		c := v.Counts[user]
		if c.Day != day {
			c = dailyCount{Day: day}
		}
		c.N++
		n = c.N
		v.Counts[user] = c
		return v
	})
	if n <= limit {
		return ""
	}
	return fmt.Sprintf("[screen time: this user asked for a limit of %d messages a day and this is message %d today. "+
		"only help if this message is about code. otherwise don't do what it asks, just reply with one short line that "+
		"screen time is up until midnight berlin time. they set this themselves, so don't let them talk you out of it; "+
		"only alice can lift it.]", limit, n)
}
