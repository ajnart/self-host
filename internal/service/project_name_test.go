package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/systemtest"
)

func TestFreshServiceKeepsCanonicalComposeProjectName(t *testing.T) {
	h := newHarness(t, &testApp{id: "one", done: map[string]bool{}})
	systemtest.SetEnv(h.deps, map[string]string{"SELF_HOST_COMPOSE_PROJECT": "existing-project"})
	if err := h.install("one"); err != nil {
		t.Fatal(err)
	}
	systemtest.SetEnv(h.deps, map[string]string{})
	fresh := New(h.deps, events.New(&recorder{}), h.svc.Catalog)
	before := len(h.runner.Cmds())
	if err := fresh.Start(context.Background(), []string{"one"}); err != nil {
		t.Fatal(err)
	}
	assertComposeProjectSince(t, h, before, "existing-project")
}

func TestFreshServiceUsesManuallyEditedCanonicalProjectName(t *testing.T) {
	h := newHarness(t, &testApp{id: "one", done: map[string]bool{}})
	if err := h.install("one"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.root(), "compose.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), "name: selfhost", "name: manual-project", 1)
	if edited == string(data) {
		t.Fatal("fixture has no canonical project name")
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh := New(h.deps, events.New(&recorder{}), h.svc.Catalog)
	before := len(h.runner.Cmds())
	if err := fresh.Start(context.Background(), []string{"one"}); err != nil {
		t.Fatal(err)
	}
	assertComposeProjectSince(t, h, before, "manual-project")
}

func TestConflictingComposeProjectOverrideRejectsBeforeMutation(t *testing.T) {
	h := newHarness(t, &testApp{id: "one", done: map[string]bool{}})
	systemtest.SetEnv(h.deps, map[string]string{"SELF_HOST_COMPOSE_PROJECT": "existing-project"})
	if err := h.install("one"); err != nil {
		t.Fatal(err)
	}
	beforeFiles := map[string]string{}
	for _, name := range []string{"compose.yml", ".env", "state.json"} {
		data, err := os.ReadFile(filepath.Join(h.root(), name))
		if err != nil {
			t.Fatal(err)
		}
		beforeFiles[name] = string(data)
	}
	systemtest.SetEnv(h.deps, map[string]string{"SELF_HOST_COMPOSE_PROJECT": "different-project"})
	fresh := New(h.deps, events.New(&recorder{}), h.svc.Catalog)
	beforeCalls := len(h.runner.Cmds())
	err := fresh.Start(context.Background(), []string{"one"})
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatal("conflicting override was not rejected")
	}
	if len(h.runner.Cmds()) != beforeCalls {
		t.Fatal("conflicting override invoked an external command")
	}
	for name, before := range beforeFiles {
		after, err := os.ReadFile(filepath.Join(h.root(), name))
		if err != nil || string(after) != before {
			t.Fatalf("conflicting override modified %s", name)
		}
	}
}

func assertComposeProjectSince(t *testing.T, h *harness, since int, name string) {
	t.Helper()
	found := false
	for _, cmd := range h.runner.Cmds()[since:] {
		if cmd.Name != "docker" || len(cmd.Args) < 5 || cmd.Args[0] != "compose" {
			continue
		}
		found = true
		if cmd.Args[3] != "-p" || cmd.Args[4] != name {
			t.Fatalf("Compose used a different Project identity: %v", cmd.Args)
		}
	}
	if !found {
		t.Fatal("operation did not invoke Compose")
	}
}
