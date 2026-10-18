//go:build race

package msgstore

// raceOn is set when the race detector is built in: it slows the recorder, which is CPU, far more than a bare insert.
const raceOn = true
