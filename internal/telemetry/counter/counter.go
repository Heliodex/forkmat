// Package counter is a no-op stand-in for cmd/internal/telemetry/counter, which gofmt uses to record usage counters. Outside the Go tree there is no telemetry uploader to talk to, so every operation here does nothing.
//
// This mirrors the bootstrap variant of the real package: the API is the same, so the copied gofmt source only needed its import path changed.
package counter

import "flag"

type dummyCounter struct{}

func CountFlagValue(_ string, _ flag.FlagSet, _ string) {}
func CountFlags(_ string, _ flag.FlagSet) {}
func Inc(_ string) {}

func New(_ string) dummyCounter {
	return dummyCounter{}
}

func NewStack(_ string, _ int) dummyCounter {
	return dummyCounter{}
}

func Open() {}
func (dc dummyCounter) Inc() {}
