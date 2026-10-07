//go:build race

package web

// raceEnabled reports whether the race detector is on; it makes timing
// checks meaningless.
const raceEnabled = true
