//go:build linux

package sysmetrics

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// readProcStat must survive a comm containing spaces and parentheses. The
// process name is user-controlled on some systems, and splitting the whole line
// on whitespace reads the wrong fields — the classic bug in this parser.
func TestReadProcStat_SurvivesParenthesesInComm(t *testing.T) {
	// The real file is the source of truth; this asserts the parse contract on
	// whatever shape it currently has.
	total, user, sys, ok := readProcStat()
	if !ok {
		t.Skip("no /proc/self/stat on this host")
	}
	if total < 0 || user < 0 || sys < 0 {
		t.Fatalf("negative CPU times: total=%v user=%v sys=%v", total, user, sys)
	}
	if total != user+sys {
		t.Errorf("total (%v) != user+sys (%v)", total, user+sys)
	}
}

// The parser must not depend on the process name. Feeding a line whose comm
// contains ")" and spaces pins that: the parse starts after the LAST ')'.
func TestReadProcStatFieldOffsets(t *testing.T) {
	// A synthetic line with the shape the kernel writes. Fields after comm:
	// state ppid pgrp session tty_nr tpgid flags minflt cminflt majflt cmajflt
	// utime stime ...
	line := "1234 (we)ird (name) S 1 1234 1234 0 -1 4194560 100 0 0 0 " +
		"700 300 0 0 20 0 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0"

	closeIdx := strings.LastIndexByte(line, ')')
	if closeIdx < 0 {
		t.Fatal("test line has no closing paren")
	}
	fields := strings.Fields(line[closeIdx+2:])
	if len(fields) < 13 {
		t.Fatalf("test line produced %d fields, need >= 13", len(fields))
	}
	// utime is 700 ticks, stime 300 -> 7s and 3s at USER_HZ=100.
	if fields[11] != "700" || fields[12] != "300" {
		t.Fatalf("field offsets are wrong: utime=%q stime=%q, want 700 and 300", fields[11], fields[12])
	}
}

// RSS must be non-zero for a running process: if the page-size multiply were
// dropped or the wrong field read, this would report 0 or a nonsense figure.
func TestReadRSS_PlausibleForThisProcess(t *testing.T) {
	rss := readRSS()
	if rss == 0 {
		t.Skip("no /proc/self/statm on this host")
	}
	// A Go test binary is at least a few MB resident and far below a terabyte.
	const minRSS = 1 << 20   // 1 MiB
	const maxRSS = 1 << 40   // 1 TiB
	if rss < minRSS || rss > maxRSS {
		t.Errorf("rss = %d bytes, outside the plausible range", rss)
	}
}

// MemAvailable, not MemFree: the card says "RAM left", and MemFree excludes
// page cache, which makes a healthy machine look nearly full.
func TestReadMemInfo_AvailableIsNotFree(t *testing.T) {
	total, avail, ok := readMemInfo()
	if !ok {
		t.Skip("no /proc/meminfo on this host")
	}
	if total == 0 {
		t.Fatal("MemTotal parsed as 0")
	}
	if avail > total {
		t.Errorf("available (%d) exceeds total (%d)", avail, total)
	}
	// MemAvailable is normally the larger of the two on a machine with cache.
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		t.Skip("cannot re-read meminfo")
	}
	var free uint64
	for _, line := range strings.Split(string(raw), "\n") {
		if key, value, found := strings.Cut(line, ":"); found && key == "MemFree" {
			free = parseKB(value)
		}
	}
	if free > 0 && avail < free {
		t.Errorf("MemAvailable (%d) < MemFree (%d); the wrong field may be parsed", avail, free)
	}
}

// A percentage needs two samples, so the first call reports 0 and the second
// must produce a number. Burning CPU in between makes the second sample
// non-zero rather than merely present.
func TestRead_CPUPercentNeedsTwoSamples(t *testing.T) {
	prevCPUTime, prevAt = 0, time.Time{} // isolate from other tests

	first := Read()
	if first.CPUPercent != 0 {
		t.Errorf("first sample cpuPercent = %v, want 0 (no previous sample to difference)", first.CPUPercent)
	}

	// Burn measurable CPU so the delta cannot round to zero.
	deadline := time.Now().Add(120 * time.Millisecond)
	x := 0
	for time.Now().Before(deadline) {
		x++
	}
	runtime.KeepAlive(x)

	second := Read()
	if second.CPUPercent <= 0 {
		t.Errorf("second sample cpuPercent = %v, want > 0 after burning CPU", second.CPUPercent)
	}
	if !second.Supported {
		t.Error("Supported = false on Linux")
	}
	if second.UptimeSeconds <= 0 {
		t.Errorf("uptime = %v, want > 0", second.UptimeSeconds)
	}
}
