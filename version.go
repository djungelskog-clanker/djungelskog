package main

import "runtime/debug"

// version is the git commit the binary was built from (go embeds it).
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if rev == "" {
		return "dev"
	}
	if dirty {
		rev += "+dirty"
	}
	return rev
}
