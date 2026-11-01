package service

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
)

type migrationPrivateApp struct{ *testApp }

func (migrationPrivateApp) Inputs() []app.Input {
	return []app.Input{{Key: "token", Private: true}}
}

func TestAmbiguousComposeMigrationDoesNotRewritePrivateInputs(t *testing.T) {
	h := newHarness(t, migrationPrivateApp{&testApp{id: "one", done: map[string]bool{}}})
	if err := h.install("one"); err != nil {
		t.Fatal(err)
	}
	p, err := h.svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	state, err := p.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	state.Apps["one"].Inputs = map[string]string{"token": "synthetic-private-input"}
	if err := p.SaveState(state); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteCompose([]byte("include: [unsupported/compose.yml]\n")); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, name := range []string{".env", "compose.yml", "state.json"} {
		data, err := os.ReadFile(p.Path(name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = string(data)
	}
	calls := len(h.runner.Calls())
	if err := h.svc.Start(context.Background(), []string{"one"}); err == nil || !strings.Contains(err.Error(), "explicit migration") {
		t.Fatalf("ambiguous migration was not rejected: %v", err)
	}
	for name, original := range before {
		data, err := os.ReadFile(p.Path(name))
		if err != nil || string(data) != original {
			t.Errorf("rejected migration changed %s: %v", name, err)
		}
	}
	if len(h.runner.Calls()) != calls {
		t.Fatal("rejected migration invoked Docker")
	}
}
