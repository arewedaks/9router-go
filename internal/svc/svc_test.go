package svc

import (
	"strings"
	"testing"
)

func TestUnitFileCoreDirectives(t *testing.T) {
	spec := Spec{
		BinaryPath: "/opt/9router-go/9router-go",
		WorkingDir: "/var/lib/9router",
		Ports:      "20128",
		DataDir:    "/var/lib/9router",
	}
	u := spec.UnitFile()

	// The one directive that actually survives a reboot.
	if !strings.Contains(u, "WantedBy=multi-user.target") {
		t.Fatal("missing WantedBy: service would not auto-start on boot")
	}
	for _, want := range []string{
		"[Install]",
		"ExecStart=/opt/9router-go/9router-go",
		"Environment=PORT=20128",
		"Environment=DATA_DIR=/var/lib/9router",
		"WorkingDirectory=/var/lib/9router",
		"Restart=on-failure",
		"After=network-online.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q\ngot:\n%s", want, u)
		}
	}
}

func TestUnitFileOmitsEmptyOptionals(t *testing.T) {
	u := Spec{BinaryPath: "/usr/local/bin/9router-go"}.UnitFile()
	if strings.Contains(u, "User=") {
		t.Error("empty RunAsUser must not emit User= (systemd rejects empty value)")
	}
	if strings.Contains(u, "WorkingDirectory=") {
		t.Error("empty WorkingDir must not emit WorkingDirectory=")
	}
}

func TestUnitFileCustomEnvironment(t *testing.T) {
	spec := Spec{
		BinaryPath:  "/usr/bin/9router-go",
		Environment: []string{"Environment=JWT_SECRET=x"},
	}
	if !strings.Contains(spec.UnitFile(), "Environment=JWT_SECRET=x") {
		t.Error("custom Environment entries must pass through verbatim")
	}
}
