// Package svc generates and manages the systemd unit for 9router-go so the
// gateway survives a reboot: "systemctl enable" is what makes the service come
// back up after boot, and the installer wires it in one shot.
//
// Design constraints:
//   - pure standard library (os/exec, os, fmt, path/filepath) — no external deps
//   - every shell command goes through exec.Command, never os.Shell, so a
//     malformed path cannot become an injection point
//   - idempotent: re-running install while the unit already exists just
//     overwrites it and restarts the service, which is the safe behaviour
package svc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	ServiceName = "9router-go"
	UnitPath    = "/etc/systemd/system/9router-go.service"
)

// Spec is everything the unit file needs. All fields except BinaryPath and
// WorkingDir are resolved from sensible defaults by Default().
type Spec struct {
	BinaryPath  string // absolute path to the 9router-go executable
	WorkingDir  string // directory to run from (data dir home)
	Environment []string
	Ports       string // PORT env value, e.g. "20128"
	DataDir     string // DATA_DIR env value
	RunAsUser   string // systemd User=; empty means run as the invoking user's root
}

// Default builds a Spec for the current machine: the binary next to the
// running executable (so "sudo 9router-go service install" just works), the
// data dir under the running user's home, and the documented default port.
func Default() Spec {
	exe, _ := os.Executable()
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/root"
	}
	return Spec{
		BinaryPath: filepath.Clean(exe),
		WorkingDir: home,
		Ports:      "20128",
		DataDir:    filepath.Join(home, ".9router"),
		RunAsUser:  "", // root: the installer is expected to be run via sudo
	}
}

// UnitFile renders the systemd unit text for this spec.
func (s Spec) UnitFile() string {
	envLines := append([]string{}, s.Environment...)
	if s.Ports != "" {
		envLines = append(envLines, fmt.Sprintf("Environment=PORT=%s", s.Ports))
	}
	if s.DataDir != "" {
		envLines = append(envLines, fmt.Sprintf("Environment=DATA_DIR=%s", s.DataDir))
	}

	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=9Router-Go AI API proxy gateway\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	if s.RunAsUser != "" {
		fmt.Fprintf(&b, "User=%s\n", s.RunAsUser)
	}
	for _, e := range envLines {
		b.WriteString(e + "\n")
	}
	if s.WorkingDir != "" {
		fmt.Fprintf(&b, "WorkingDirectory=%s\n", s.WorkingDir)
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", s.BinaryPath)
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=3\n")
	// A router that is down must not stay down: come back fast, keep trying.
	// `always` (not `on-failure`) because the process can also exit 0 on a
	// transient condition; a gateway that is silently gone after a reboot is
	// worse than a noisy restart loop that journalctl explains.
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

// Install writes the unit file, reloads systemd, enables and starts the
// service. Requires root (the caller is expected to have checked).
func Install(s Spec) error {
	if err := ensureRoot("install"); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(UnitPath), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(UnitPath, []byte(s.UnitFile()), 0644); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", ServiceName); err != nil {
		return err
	}
	// `enable` can succeed yet leave the unit unenabled (a stale symlink, a
	// masked unit, a distro patching the target). Verify the boot wiring
	// instead of trusting the exit code: this is the whole point of --service.
	if state, err := enablement(); err != nil {
		return err
	} else if !isEnabledState(state) {
		return fmt.Errorf("unit is not enabled for boot (is-enabled: %s); 9router-go would NOT start after a reboot", state)
	}
	// enable alone only makes it start at boot; start now so the operator
	// gets immediate feedback instead of "it'll work after I reboot".
	if err := systemctl("start", ServiceName); err != nil {
		// The usual causes are swallowed by systemctl's own error line: a port
		// already held by a manually started instance, or a bad data dir. Show
		// the unit's log inline so the operator doesn't have to know to ask.
		if status, sErr := exec.Command("systemctl", "status", ServiceName, "--no-pager", "-l").CombinedOutput(); sErr == nil || len(status) > 0 {
			fmt.Fprintf(os.Stderr, "\n%s\n", strings.TrimSpace(string(status)))
		}
		return err
	}
	state, _ := enablement()
	fmt.Printf("systemd service %q installed, %s (boot), and started.\n", ServiceName, state)
	fmt.Printf("  status:  systemctl status %s\n", ServiceName)
	fmt.Printf("  logs:    journalctl -u %s -f\n", ServiceName)
	fmt.Printf("  stop:    systemctl stop %s\n", ServiceName)
	return nil
}

// Uninstall disables and removes the service. Idempotent: a missing unit is
// not an error.
func Uninstall() error {
	if err := ensureRoot("uninstall"); err != nil {
		return err
	}
	_ = systemctl("stop", ServiceName) // stop first so the unit file can be
	// removed without "Text file busy" on the ExecStart target.
	_ = systemctl("disable", ServiceName)
	if err := os.Remove(UnitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	fmt.Println("systemd service removed. 9router-go will no longer start at boot.")
	return nil
}

// Status reports whether the unit is installed and whether it is running.
// Both halves are best-effort so the command is useful in a degraded state
// (e.g. installed but the machine just booted and network-online isn't up).
func Status() (installed, running bool, err error) {
	_, statErr := os.Stat(UnitPath)
	installed = statErr == nil

	out, e := exec.Command("systemctl", "is-active", ServiceName).Output()
	active := strings.TrimSpace(string(out))
	running = active == "active" || active == "activating"
	if e != nil {
		// systemctl exit 3 = not-active/inactive, 4 = no such unit — both are
		// legitimate "stopped" answers, not errors. Only a missing systemctl
		// binary (exec.ErrNotFound) or anything else is worth surfacing.
		if _, ok := e.(*exec.ExitError); !ok {
			return installed, false, fmt.Errorf("systemctl is-active: %w", e)
		}
	}
	return installed, running, nil
}

func ensureRoot(op string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd is only available on Linux (this is %s)", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("service %s requires root: re-run as 'sudo 9router-go service %s'", op, op)
	}
	return nil
}

func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// enablement returns the unit's boot state as reported by systemd, e.g.
// "enabled", "disabled", "masked", "static".
func enablement() (string, error) {
	out, err := exec.Command("systemctl", "is-enabled", ServiceName).Output()
	state := strings.TrimSpace(string(out))
	// is-enabled exits non-zero for disabled/masked/static, which are exactly
	// the answers we want to read, so only a missing systemctl is an error.
	if err != nil && state == "" {
		return "", fmt.Errorf("systemctl is-enabled: %w", err)
	}
	return state, nil
}

func isEnabledState(state string) bool {
	switch state {
	case "enabled", "enabled-runtime", "alias":
		return true
	}
	return false
}
