//go:build loong64

package goarch

const (
	_ArchFamily = LOONG64
	_DefaultPhysPageSize = 16384
	_MinFrameSize = 8
	_PCQuantum = 4
	_StackAlign = PtrSize
)
