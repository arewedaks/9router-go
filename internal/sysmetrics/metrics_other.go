//go:build !linux

package sysmetrics

import "runtime"

// Read reports Supported=false on platforms without the /proc interface.
//
// A best-effort number from a platform-specific API would be worse than none:
// the card would show a figure with no stated basis, and the operator cannot
// tell a real reading from a wrong one. The UI renders "not available on this
// platform" instead.
func Read() Sample {
	return Sample{
		Supported:  false,
		NumCPU:     runtime.NumCPU(),
		Goroutines: runtime.NumGoroutine(),
	}
}

// HostLoadAverage is unavailable off Linux.
func HostLoadAverage() (float64, bool) { return 0, false }
