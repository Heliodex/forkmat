// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package counter is a no-op stand-in for cmd/internal/telemetry/counter,
// which gofmt uses to record usage counters. Outside the Go tree there is no
// telemetry uploader to talk to, so every operation here does nothing.
//
// This mirrors the bootstrap variant of the real package: the API is the same,
// so the copied gofmt source only needed its import path changed.
package counter

import "flag"

type dummyCounter struct{}

func (dc dummyCounter) Inc() {}

func Open() {}
func Inc(name string) {}
func New(name string) dummyCounter {
	return dummyCounter{}
}
func NewStack(name string, depth int) dummyCounter {
	return dummyCounter{}
}
func CountFlags(name string, flagSet flag.FlagSet) {}
func CountFlagValue(prefix string, flagSet flag.FlagSet, flagName string) {}
