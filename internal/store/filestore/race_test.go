//go:build race

package filestore

// raceEnabled reports whether the race detector is on; it slows the index
// build too much for timing checks.
const raceEnabled = true
