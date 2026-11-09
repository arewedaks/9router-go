package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// DeepAI Code lanes its metered calls on client identity: the vendor's desktop
// IDE sends `X-DeepAI-Code-Machine: sha256(machine-id)` and the server buckets
// the per-machine daily allowance ("Theos") by it. Reproducing the digest on the
// router host keeps the quota attached to the operator's own box instead of
// being treated as a machine-less caller (which the upstream answers
// `machine_required` for on an older lane).
//
// The exact id the vendor reads, per its own detect():
//
//	macOS   IOPlatformUUID from `ioreg -rd1 -c IOPlatformExpertDevice`
//	windows (Get-CimInstance Win32_ComputerSystemProduct).UUID, else
//	        HKLM\SOFTWARE\Microsoft\Cryptography MachineGuid
//
// Any other platform (a Linux router host) has no id in that map, so the header
// is omitted — the same thing the vendor's detect() does when it returns null.

var (
	deepAIIOPlatformUUID = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([^"]+)"`)
	deepAIUUID           = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	// Values a VM/blank board reports; the vendor's SMBIOS_PLACEHOLDERS set.
	deepAIPlaceholderUUIDs = map[string]bool{
		"00000000-0000-0000-0000-000000000000": true,
		"ffffffff-ffff-ffff-ffff-ffffffffffff": true,
		"03000200-0400-0500-0006-000700080009": true,
	}
)

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func parseIOPlatformUUID(ioreg string) string {
	m := deepAIIOPlatformUUID.FindStringSubmatch(ioreg)
	if len(m) != 2 {
		return ""
	}
	id := strings.ToLower(strings.TrimSpace(m[1]))
	return deepAIUUID.FindString(id)
}

func parseSmbiosUUID(out string) string {
	id := deepAIUUID.FindString(out)
	if id == "" {
		return ""
	}
	id = strings.ToLower(id)
	if deepAIPlaceholderUUIDs[id] {
		return ""
	}
	return id
}

func parseMachineGuid(out string) string {
	id := deepAIUUID.FindString(out)
	if id == "" {
		return ""
	}
	return strings.ToLower(id)
}

type deepAIMachineID struct {
	value     string
	available bool
}

func detectDeepAIMachineID() deepAIMachineID {
	var raw string
	switch runtime.GOOS {
	case "darwin":
		if out, err := runCmd("ioreg", "-rd1", "-c", "IOPlatformExpertDevice"); err == nil {
			raw = parseIOPlatformUUID(out)
		}
	case "windows":
		if out, err := runCmd("powershell", "-NoProfile", "-NonInteractive", "-Command",
			"(Get-CimInstance Win32_ComputerSystemProduct).UUID"); err == nil {
			raw = parseSmbiosUUID(out)
		}
		if raw == "" {
			if out, err := runCmd("reg", "query",
				`HKLM\SOFTWARE\Microsoft\Cryptography`, "/v", "MachineGuid"); err == nil {
				raw = parseMachineGuid(out)
			}
		}
	}
	if raw == "" {
		return deepAIMachineID{}
	}
	return deepAIMachineID{value: sha256Hex(raw), available: true}
}

// deepAIMachineIDOnce computes the digest once per process; the vendor does the
// same (a cached `pending` promise), because shelling out per request would add
// latency to every call for a value that never changes.
var deepAIMachineIDOnce = func() deepAIMachineID { return detectDeepAIMachineID() }()

func cachedDeepAIMachineID() deepAIMachineID { return deepAIMachineIDOnce }
