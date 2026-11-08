package main

import "math/rand/v2"

// planTitles label the task list above a reply. slack shows a generic
// "thinking" otherwise, which is boring.
var planTitles = []string{
	"rummaging through the shelves",
	"assembling without the manual",
	"counting the little allen keys",
	"consulting the meatball oracle",
	"thinking very hard with my stuffing",
	"wandering the showroom",
	"looking for the exit (there isn't one)",
	"checking the warehouse aisle",
	"flat-packing some thoughts",
	"sniffing around",
	"doing bear things",
	"pressing buttons carefully",
	"gathering crumbs",
	"hibernating productively",
}

func randomPlanTitle() string { return planTitles[rand.IntN(len(planTitles))] }
