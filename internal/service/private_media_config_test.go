package service

import (
	"context"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

func TestPrivateMediaConfigReaderUsesOnlyTheSelectedAppAndFixedPath(t *testing.T) {
	h := newHarness(t, &testApp{id: "sonarr", done: map[string]bool{}})
	if err := h.install("sonarr"); err != nil {
		t.Fatal(err)
	}
	p, _ := h.svc.Project()
	settings, _ := p.LoadSettings()
	settings["MEDIA_SEPARATE_USERS"] = "on"
	if err := p.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	ss, err := h.svc.open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	read := ss.privateMediaConfigReader("sonarr", "sonarr")
	if read == nil {
		t.Fatal("private media configuration has no container reader")
	}
	h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
		line := systemtest.Line(cmd)
		if !strings.Contains(line, "sonarr s6-setuidgid abc cat /config/config.xml") {
			t.Fatal("configuration read exceeded the selected App and fixed path")
		}
		return system.Result{Stdout: []byte("fixture-config")}
	})
	data, err := read(context.Background(), "config/config.xml")
	if err != nil || string(data) != "fixture-config" {
		t.Fatal("private App configuration was not read")
	}
	before := len(h.runner.Cmds())
	if _, err := read(context.Background(), "../../.env"); err == nil || len(h.runner.Cmds()) != before {
		t.Fatal("unexpected paths must fail before container execution")
	}
	if ss.privateMediaConfigReader("ntfy", "ntfy") != nil {
		t.Fatal("unrelated App received a private media reader")
	}
	h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
		if !strings.Contains(systemtest.Line(cmd), "bazarr s6-setuidgid abc cat /config/config/config.yaml") {
			t.Fatal("subtitle App configuration read used the wrong container path")
		}
		return system.Result{Stdout: []byte("fixture-config")}
	})
	read = ss.privateMediaConfigReader("bazarr", "bazarr")
	if read == nil {
		t.Fatal("subtitle App has no private configuration reader")
	}
	if _, err := read(context.Background(), "config/config/config.yaml"); err != nil {
		t.Fatal(err)
	}
	ss.settings["MEDIA_SEPARATE_USERS"] = "off"
	if ss.privateMediaConfigReader("sonarr", "sonarr") != nil {
		t.Fatal("ordinary media configuration should retain its local reader")
	}
}
