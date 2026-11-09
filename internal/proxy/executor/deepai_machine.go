package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"9router/proxy/internal/log"
)

// DeepAI Code lanes its metered calls on client identity: the vendor's desktop
// IDE sends `X-DeepAI-Code-Machine: sha256(machine-id)` and the server buckets
// the per-machine daily allowance ("Theos") by it. Reproducing the digest keeps
// the quota attached to the operator's own box instead of being treated as a
// machine-less caller.
//
// The id the vendor reads, per its own detect(), and the Linux equivalent of
// each — so a router running on any of the three emits the same header the IDE
// would on that hardware:
//
//	macOS   IOPlatformUUID from `ioreg -rd1 -c IOPlatformExpertDevice`
//	         -> there is no Linux equivalent; SMBIOS UUID differs from it by
//	            design, so the digest is deliberately NOT shared.
//	windows (Get-CimInstance Win32_ComputerSystemProduct).UUID
//	         else HKLM\SOFTWARE\Microsoft\Cryptography MachineGuid
//	         -> /sys/class/dmi/id/product_uuid is the same SMBIOS product UUID
//	            the Win32 API returns, so on one physical box the Linux and
//	            Windows digests match exactly.
//
// Every value is lowercased before hashing, which is what the vendor's
// parseSmbiosUUID / parseMachineGuid / parseIOPlatformUUID all do, and the
// digest is sha256 of that lowercased string.
//
// A VM/blank board reports a placeholder UUID; those hash to a value every such
// machine shares, so they are rejected exactly as the vendor's
// SMBIOS_PLACEHOLDERS set does and the header is omitted.

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

// deepAILinuxProductUUIDPath is the SMBIOS product UUID on Linux; it equals the
// value Windows' Win32_ComputerSystemProduct.UUID returns for the same board.
// Read without root on every mainstream distro; a container or locked-down host
// may return EACCES, in which case the header is simply omitted.
const deepAILinuxProductUUIDPath = "/sys/class/dmi/id/product_uuid"

// readFileFn is a seam so the Linux path can be exercised off Linux.
var readFileFn = os.ReadFile

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

// parseSmbiosUUID extracts and normalises the SMBIOS UUID from either the
// Windows PowerShell output or the raw Linux product_uuid file: lowercase, first
// UUID-shaped token, placeholder rejected. Both platforms funnel through here so
// one box yields one digest whichever OS the router runs on.
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

// parseMachineGuid reads the MachineGuid out of `reg query` output.
func parseMachineGuid(out string) string {
	m := regexp.MustCompile(`MachineGuid\s+REG_SZ\s+(\S+)`).FindStringSubmatch(out)
	if len(m) != 2 {
		return ""
	}
	id := strings.ToLower(strings.TrimSpace(m[1]))
	if !deepAIUUID.MatchString(id) {
		return ""
	}
	return id
}

type deepAIMachineID struct {
	value     string
	available bool
}

// detectRawMachineID returns the un-hashed platform id, or "" when this host
// exposes none (or only a placeholder).
func detectRawMachineID() string {
	switch runtime.GOOS {
	case "darwin":
		if out, err := runCmd("ioreg", "-rd1", "-c", "IOPlatformExpertDevice"); err == nil {
			return parseIOPlatformUUID(out)
		}
	case "windows":
		if out, err := runCmd("powershell", "-NoProfile", "-NonInteractive", "-Command",
			"(Get-CimInstance Win32_ComputerSystemProduct).UUID"); err == nil {
			if id := parseSmbiosUUID(out); id != "" {
				return id
			}
		}
		if out, err := runCmd("reg", "query",
			`HKLM\SOFTWARE\Microsoft\Cryptography`, "/v", "MachineGuid"); err == nil {
			return parseMachineGuid(out)
		}
	case "linux":
		// The same SMBIOS UUID Windows reads, exposed by the kernel.
		if raw, err := readFileFn(deepAILinuxProductUUIDPath); err == nil {
			return parseSmbiosUUID(string(raw))
		}
	}
	return ""
}

func detectDeepAIMachineID() deepAIMachineID {
	raw := detectRawMachineID()
	if raw == "" {
		return deepAIMachineID{}
	}
	return deepAIMachineID{value: sha256Hex(raw), available: true}
}

// deepAIMachineIDOnce computes the digest once per process; the vendor does the
// same (a cached `pending` promise), because shelling out per request would add
// latency to every call for a value that never changes. The one-time line makes
// whether the machine header is being sent verifiable from the log — the thing
// an operator needs when the upstream answers `machine_required`. Only a short
// fingerprint of the digest is logged, never the raw UUID.
var deepAIMachineIDOnce = func() deepAIMachineID {
	id := detectDeepAIMachineID()
	if id.available {
		log.Info("deepai", "machine header enabled", "digest_prefix", id.value[:8])
	} else {
		log.Info("deepai", "machine header omitted", "reason", "no readable machine id on this host")
	}
	return id
}()

func cachedDeepAIMachineID() deepAIMachineID { return deepAIMachineIDOnce }
