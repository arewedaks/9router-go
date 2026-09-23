// Package sysmetrics reports this process's CPU and memory use, plus the host's
// memory, for the dashboard's resource card.
//
// On Linux the numbers come from /proc and syscall.Getrusage rather than a
// dependency: the process's own CPU time is in /proc/self/stat, its resident set
// in /proc/self/statm, and host memory in /proc/meminfo. That covers the
// deployment this dashboard is used for (Docker/systemd on Linux) with no new
// module. Other platforms report Supported=false rather than guessing.
package sysmetrics

// Sample is one reading. Percentages are 0-100 and may exceed 100 for CPU on a
// multi-core host, which is the conventional meaning of "one core saturated".
type Sample struct {
	Supported bool `json:"supported"`

	// Process CPU: total since start and the share of ONE core over the interval
	// between samples. A value above 100 means more than one core was busy.
	CPUPercent     float64 `json:"cpuPercent"`
	CPUUserSeconds float64 `json:"cpuUserSeconds"`
	CPUSysSeconds  float64 `json:"cpuSysSeconds"`
	NumCPU         int     `json:"numCpu"`

	// Process memory.
	RSSBytes   uint64 `json:"rssBytes"`
	HeapBytes  uint64 `json:"heapBytes"`
	Goroutines int    `json:"goroutines"`

	// Host memory. Available is what the kernel reports as reclaimable, which is
	// the honest "how much is left" figure — Free alone excludes cache and reads
	// as alarmingly low on a healthy box.
	HostTotalBytes     uint64 `json:"hostTotalBytes"`
	HostAvailableBytes uint64 `json:"hostAvailableBytes"`
	HostUsedPercent    float64 `json:"hostUsedPercent"`

	// UptimeSeconds is the process's own uptime, not the host's.
	UptimeSeconds float64 `json:"uptimeSeconds"`
}
