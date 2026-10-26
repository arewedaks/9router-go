package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v2"

	"9router/proxy/internal/config"
	"9router/proxy/internal/mitm"
	"9router/proxy/internal/svc"
)

func resolveDataDir() string {
	d := os.Getenv("DATA_DIR")
	if d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".9router"
	}
	return home + "/.9router"
}

func mitmEnable(_ *cli.Context) error {
	dataDir := resolveDataDir()
	mgr := mitm.NewManager(dataDir)
	if err := mgr.Enable(); err != nil {
		return fmt.Errorf("MITM enable failed: %w", err)
	}
	fmt.Println("MITM proxy enabled. Intercepted traffic on :443 → 9router proxy.")
	return nil
}

func mitmDisable(_ *cli.Context) error {
	dataDir := resolveDataDir()
	mgr := mitm.NewManager(dataDir)
	if err := mgr.Disable(); err != nil {
		return fmt.Errorf("MITM disable failed: %w", err)
	}
	fmt.Println("MITM proxy disabled.")
	return nil
}

func mitmStatus(_ *cli.Context) error {
	dataDir := resolveDataDir()
	mgr := mitm.NewManager(dataDir)
	status := mgr.Status()
	fmt.Printf("Running: %v\n", status["running"])
	fmt.Printf("CA installed: %v\n", status["ca_installed"])
	fmt.Printf("MITM dir: %v\n", status["mitm_dir"])
	return nil
}

// uninstallCommand removes everything the installer and a self-update leave on
// the machine: the systemd unit, the binary plus its .old/.bak-* rotation
// leftovers, and — only with --purge — the data directory.
//
// Without --yes it prints the plan and touches nothing: the binary it would
// delete is the one running the command, so the destructive step is opt-in.
func uninstallCommand() *cli.Command {
	return &cli.Command{
		Name:  "uninstall",
		Usage: "Remove the systemd unit and the installed binary (--purge also deletes the data)",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "purge", Usage: "also delete the data directory (database, config, logs)"},
			&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "actually remove; without it the plan is only printed"},
		},
		Action: uninstall,
	}
}

func uninstall(cCtx *cli.Context) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate this binary: %w", err)
	}
	exe = resolveExecutable(exe)

	targets := staleBinaries(exe)
	dataDir := config.ResolveDataDir()
	purge := cCtx.Bool("purge")

	fmt.Println("This will remove:")
	fmt.Printf("  systemd unit   %s (system or user unit, if installed)\n", svc.ServiceName)
	for _, p := range targets {
		fmt.Printf("  binary         %s\n", p)
	}
	if purge {
		fmt.Printf("  data dir       %s\n", dataDir)
	} else {
		fmt.Printf("  (data dir kept: %s — rerun with --purge to delete it)\n", dataDir)
	}

	if !cCtx.Bool("yes") {
		fmt.Println("\nNothing removed. Re-run with --yes to proceed.")
		return nil
	}

	// Stop the unit first: it holds the binary open, and its restart policy
	// would put the proxy back on the port a second after we delete it.
	if err := svc.Uninstall(); err != nil {
		// A per-user unit (systemctl --user) needs no root, and svc only
		// manages the system-wide one, so clean that up here instead.
		if removeUserUnit() {
			fmt.Printf("removed user unit %s\n", userUnitPath())
		} else {
			fmt.Fprintf(os.Stderr, "systemd unit: %v (continuing)\n", err)
		}
	}

	for _, p := range targets {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "remove %s: %v\n", p, err)
			continue
		}
		fmt.Printf("removed %s\n", p)
	}

	if purge {
		if dataDir == "" || dataDir == "/" || dataDir == "." {
			return fmt.Errorf("refusing to delete data dir %q", dataDir)
		}
		if err := os.RemoveAll(dataDir); err != nil {
			return fmt.Errorf("remove data dir: %w", err)
		}
		fmt.Printf("removed %s\n", dataDir)
	}

	fmt.Println("\nUninstalled. A 9router-go still running from another terminal keeps the port until it is stopped.")
	return nil
}

// userUnitPath is where a `systemctl --user` unit for this service lives.
func userUnitPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "systemd", "user", svc.ServiceName+".service")
}

// removeUserUnit disables and deletes a per-user systemd unit. No root needed,
// so it works for the installs the system unit path never covers.
func removeUserUnit() bool {
	unit := userUnitPath()
	if unit == "" {
		return false
	}
	if _, err := os.Stat(unit); err != nil {
		return false
	}
	// --now stops it before the file disappears, otherwise it stays up.
	_ = exec.Command("systemctl", "--user", "disable", "--now", svc.ServiceName).Run()
	if err := os.Remove(unit); err != nil {
		return false
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return true
}

// invokedPath is how the command was called: the absolute path of argv[0],
// looked up in PATH when the operator typed a bare name.
func invokedPath() string {
	a := os.Args[0]
	if a == "" {
		return ""
	}
	if strings.ContainsRune(a, os.PathSeparator) {
		if abs, err := filepath.Abs(a); err == nil {
			return abs
		}
		return a
	}
	if p, err := exec.LookPath(a); err == nil {
		return p
	}
	return ""
}

// resolveExecutable follows the symlink an install may have created (e.g.
// /usr/local/bin/9router-go -> ~/.local/bin/9router-go), so the removal hits
// the real file; a deleted image (running after a self-update swapped it) is
// returned as-is.
func resolveExecutable(exe string) string {
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		return real
	}
	return exe
}

// staleBinaries lists the binary to remove plus the copies a self-update or a
// manual backup left beside it (<name>.old, <name>.bak-<hhmm>), and the symlink
// that pointed at it when the install used one.
func staleBinaries(exe string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		if _, err := os.Lstat(p); err != nil {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	// argv[0], not os.Executable(): /proc/self/exe is already resolved, so it
	// hides the PATH symlink the operator actually invoked.
	if invoked := invokedPath(); invoked != "" && invoked != exe {
		if fi, err := os.Lstat(invoked); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			add(invoked)
		}
	}
	add(exe)
	add(exe + ".old")
	if matches, _ := filepath.Glob(exe + ".bak-*"); len(matches) > 0 {
		for _, m := range matches {
			add(m)
		}
	}
	return out
}
