package cli_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/project"
	"github.com/ajnart/self-host/internal/systemtest"
)

func TestDesktopStatusDistinguishesMissingEmptyAndUnreadableProjects(t *testing.T) {
	deps := systemtest.NewDeps(t)
	cat := testCatalog(&echoApp{id: "one"})
	if code, out := runWith(t, deps, cat, "desktop", "status"); code != 0 || out != "null\n" {
		t.Fatalf("missing Project: exit %d, output %q", code, out)
	}
	owner, err := deps.CurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	p := project.Open(owner)
	if err := p.SaveSettings(map[string]string{"EXPOSURE": "local"}); err != nil {
		t.Fatal(err)
	}
	code, out := runWith(t, deps, cat, "desktop", "status")
	var status struct {
		Home string
		Apps []json.RawMessage
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil || code != 0 || status.Home != p.Root || status.Apps == nil {
		t.Fatalf("empty Project: exit %d, output %q, decode error %v", code, out, err)
	}
	if err := os.Remove(p.Path(".env")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p.Path(".env"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code, out := runWith(t, deps, cat, "desktop", "status"); code == 0 || out == "null\n" {
		t.Fatalf("unreadable Project: exit %d, output %q", code, out)
	}
}

func TestDesktopActionNeedsApproval(t *testing.T) {
	deps := systemtest.NewDeps(t)
	code, out := runWith(t, deps, testCatalog(&echoApp{id: "one"}), "desktop", "action", "restart", "one", "unknown")
	if code == 0 || !strings.Contains(out, "review the Action first") {
		t.Fatalf("Action without approval: exit %d, output %q", code, out)
	}
}
