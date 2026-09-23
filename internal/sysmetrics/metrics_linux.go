//go:build linux

package sysmetrics

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	startedAt = time.Now()

	prevMu      sync.Mutex
	prevCPUTime float64
	prevAt      time.Time
)

// Read samples the current process and host.
//
// CPUPercent is computed against the previous call rather than since process
// start: a since-start average flattens every spike and reads as a flat line on
// a long-running server, which is exactly the number an operator does not need.
func Read() Sample {
	s := Sample{
		Supported:  true,
		NumCPU:     runtime.NumCPU(),
		Goroutines: runtime.NumGoroutine(),
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s.HeapBytes = ms.HeapAlloc

	if cpu, user, sys, ok := readProcStat(); ok {
		now := time.Now()
		prevMu.Lock()
		if !prevAt.IsZero() {
			if elapsed := now.Sub(prevAt).Seconds(); elapsed > 0 {
				s.CPUPercent = round2((cpu - prevCPUTime) / elapsed * 100)
			}
		}
		prevCPUTime = cpu
		prevAt = now
		prevMu.Unlock()
		s.CPUUserSeconds = round2(user)
		s.CPUSysSeconds = round2(sys)
	}

	s.RSSBytes = readRSS()
	if total, avail, ok := readMemInfo(); ok {
		s.HostTotalBytes = total
		s.HostAvailableBytes = avail
		if total > 0 {
			s.HostUsedPercent = round2(float64(total-avail) / float64(total) * 100)
		}
	}
	s.UptimeSeconds = round2(time.Since(startedAt).Seconds())
	return s
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

// readProcStat returns total, user and sys CPU seconds for this process.
//
// /proc/self/stat is space-separated but field 2 (comm) may itself contain
// spaces inside parentheses, so the parse starts after the LAST ')'. Splitting
// the whole line naively is the classic way to read the wrong field here.
func readProcStat() (total, user, sys float64, ok bool) {
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, 0, 0, false
	}
	line := string(raw)
	close := strings.LastIndexByte(line, ')')
	if close < 0 || close+2 >= len(line) {
		return 0, 0, 0, false
	}
	fields := strings.Fields(line[close+2:])
	// After the ')' the fields are state, ppid, ... utime is index 11 and stime
	// index 12 (utime is field 14 overall, so 14-3 = 11 here).
	if len(fields) < 13 {
		return 0, 0, 0, false
	}
	utime, err1 := strconv.ParseFloat(fields[11], 64)
	stime, err2 := strconv.ParseFloat(fields[12], 64)
	if err1 != nil || err2 != nil {
		return 0, 0, 0, false
	}
	const hz = 100 // USER_HZ; 100 on every Linux ABI this runs on.
	user = utime / hz
	sys = stime / hz
	return user + sys, user, sys, true
}

// readRSS returns the resident set size in bytes. /proc/self/statm field 2 is
// resident pages, which is what "how much RAM is this using" means; the heap
// counter alone omits the runtime and any cgo allocation.
func readRSS() uint64 {
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}

// readMemInfo returns MemTotal and MemAvailable in bytes.
//
// MemAvailable, not MemFree: MemFree excludes page cache, so a healthy machine
// that has read a lot of files reports almost no free memory. MemAvailable is
// the kernel's own estimate of what a new allocation could get without swapping.
func readMemInfo() (total, available uint64, ok bool) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch key {
		case "MemTotal":
			total = parseKB(value)
		case "MemAvailable":
			available = parseKB(value)
		}
	}
	return total, available, total > 0
}

// parseKB reads a "  12345 kB" value into bytes.
func parseKB(v string) uint64 {
	fields := strings.Fields(v)
	if len(fields) == 0 {
		return 0
	}
	kb, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return kb * 1024
}

// HostLoadAverage is the 1/5/15-minute load, reported separately from the
// sample because it is host-wide and not a process metric.
func HostLoadAverage() (float64, bool) {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 0, false
	}
	// Sysinfo returns load scaled by 65536 on Linux.
	return float64(info.Loads[0]) / 65536, true
}
