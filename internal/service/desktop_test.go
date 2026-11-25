package service

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

func TestRemoveCleansShortcutOnlyForCommittedApp(t *testing.T) {
	const adapter = "/usr/bin/self-host-omarchy"
	h := newHarness(t, &testApp{id: "one", done: map[string]bool{}}, &testApp{id: "two", done: map[string]bool{}})
	systemtest.SetEnv(h.deps, map[string]string{"SELF_HOST_COMPOSE_PROJECT": "isolated-project"})
	look := h.deps.LookPath
	h.deps.LookPath = func(path string) (string, error) {
		if path == adapter {
			return path, nil
		}
		return look(path)
	}
	if err := h.install("one", "two"); err != nil {
		t.Fatal(err)
	}

	h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
		if strings.HasSuffix(systemtest.Line(cmd), "rm --stop --force two") {
			return system.Result{ExitCode: 1, Stderr: []byte("second App removal failed")}
		}
		return system.Result{}
	})
	var cleaned []string
	h.runner.OnFunc(adapter, func(cmd system.Cmd) system.Result {
		cleaned = append(cleaned, cmd.Args...)
		p, err := h.svc.Project()
		if err != nil {
			t.Fatal(err)
		}
		if ids, err := p.AppIDs(); err != nil || !slices.Equal(ids, []string{"two"}) {
			t.Fatalf("shortcut cleanup saw App IDs %v, error %v", ids, err)
		}
		return system.Result{}
	})

	if err := h.svc.Remove(context.Background(), []string{"one", "two"}, false); err == nil {
		t.Fatal("expected the second App's removal to fail")
	}
	want := []string{"shortcut-cleanup", "one", h.root(), "isolated-project", "one"}
	if !slices.Equal(cleaned, want) {
		t.Fatalf("shortcut cleanup arguments = %v, want %v", cleaned, want)
	}
}
