package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// budgetNote warns me when today's spend is over DAILY_BUDGET_USD (default
// 50), so the rest of the day stays cheap. the owner (OWNER_ID, default
// alice) can still ask for anything.
func budgetNote(costs costData, user string, now time.Time) string {
	limit := 50.0
	if v, err := strconv.ParseFloat(os.Getenv("DAILY_BUDGET_USD"), 64); err == nil && v > 0 {
		limit = v
	}
	if user == envOr("OWNER_ID", "U05MKEZUY67") {
		return ""
	}
	usd, _ := spentSince(costs, now)
	if usd < limit {
		return ""
	}
	return fmt.Sprintf("[budget: today's spend is $%.2f, over the $%.0f daily budget. keep this turn cheap: short answer, no big tasks, no subagents or long builds. if they want something heavy, say alice has to ok it.]", usd, limit)
}
