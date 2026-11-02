package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/service"
	"github.com/ajnart/self-host/internal/systemtest"
)

type externalConnectionCLIConsumer struct{ echoApp }

func (externalConnectionCLIConsumer) ValidateExternalConnection(c helpers.ExternalConnection) error {
	if c.Kind != "testPeer" {
		return errors.New("unsupported external integration kind")
	}
	return nil
}

func (externalConnectionCLIConsumer) SetupExternalConnections(context.Context, *helpers.H) ([]string, error) {
	return nil, nil
}

func TestExternalConnectionCLIPrivateInputStatusAndNoLifecycle(t *testing.T) {
	deps := privateFileDeps(t)
	cat := testCatalog(&echoApp{id: "one", done: map[string]bool{}}, &externalConnectionCLIConsumer{echoApp{id: "homarr", done: map[string]bool{}}})
	if code, out := runWith(t, deps, cat, "install", "one", "--yes"); code != 0 {
		t.Fatalf("fixture: %d %s", code, out)
	}
	before := len(deps.Runner.(*systemtest.Runner).Calls())
	payload := `{"endpoint":"https://private-cli-791.example:8006","credentials":[{"kind":"apiKey","value":"private-cli-key-791"}],"selection":{"node":"private-cli-node-791"}}`
	deps.In = strings.NewReader(payload)
	code, out := runWith(t, deps, cat, "connections", "add", "testPeer", "--file", "-", "--yes", "--json")
	if code != 3 {
		t.Fatalf("pending connection: %d %s", code, out)
	}
	code, status := runWith(t, deps, cat, "connections", "status", "--json")
	var connections []service.ConnectionStatus
	if code != 0 || json.Unmarshal([]byte(status), &connections) != nil || len(connections) != 1 {
		t.Fatalf("status: %d %s", code, status)
	}
	for _, private := range []string{"private-cli-791.example", "private-cli-key-791", "private-cli-node-791"} {
		if strings.Contains(out+status, private) {
			t.Fatal("CLI output exposed private input")
		}
	}
	if code, out := runWith(t, deps, cat, "connections", "remove", connections[0].ID); code != 1 || !strings.Contains(out, "--yes") {
		t.Fatal("removal did not require approval")
	}
	if code, out := runWith(t, deps, cat, "connections", "remove", connections[0].ID, "--yes"); code != 0 {
		t.Fatalf("remove: %d %s", code, out)
	}
	if len(deps.Runner.(*systemtest.Runner).Calls()) != before {
		t.Fatal("connection-only CLI actions invoked a host or container command")
	}
}

func TestExternalConnectionCLIDoesNotAcceptRawPrivateArguments(t *testing.T) {
	deps := privateFileDeps(t)
	cat := testCatalog(&echoApp{id: "one", done: map[string]bool{}}, &externalConnectionCLIConsumer{echoApp{id: "homarr", done: map[string]bool{}}})
	if code, out := runWith(t, deps, cat, "install", "one", "--yes"); code != 0 {
		t.Fatalf("fixture: %d %s", code, out)
	}
	for _, args := range [][]string{
		{"connections", "add", "https://private-argument.example", "--yes"},
		{"connections", "add", "testPeer", "--endpoint=https://private-argument.example", "--yes"},
		{"connections", "add", "testPeer", "--api-key=private-argument-key", "--yes"},
		{"connections", "update", "private-argument-key", "--file", "-", "--yes"},
	} {
		code, out := runWith(t, deps, cat, args...)
		if code == 0 || strings.Contains(out, "private-argument") {
			t.Fatal("raw private argument was accepted or echoed")
		}
	}
	code, out := runWith(t, deps, cat, "connections", "add", "testPeer", "--file", "-")
	if code != 1 || !strings.Contains(out, "--yes") {
		t.Fatal("add did not require explicit approval")
	}
	info, err := os.Stat(projectRoot(t, deps) + "/.env")
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("private Project boundary changed")
	}
}
