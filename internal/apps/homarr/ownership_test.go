package homarr_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/arr"
	"github.com/ajnart/self-host/internal/apps/homarr"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/clitest"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/service"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

type ownershipPeer struct{ arr.App }

func (ownershipPeer) Setup(_ context.Context, h *helpers.H) error {
	return h.SetSecret(h.Key("api_key"), "synthetic-ownership-api-key")
}

func (ownershipPeer) Check(context.Context, *helpers.H) error { return nil }
func (ownershipPeer) Links() []app.Link                       { return nil }

func TestOwnershipManualHintsSurviveLaterProjectReads(t *testing.T) {
	for _, operation := range []string{"app", "widget-link", "widget-unlink"} {
		t.Run(operation, func(t *testing.T) {
			fake := newFake()
			if operation == "app" {
				fake.apps = []map[string]any{{"id": "user-app", "name": "Prowlarr", "description": "", "href": "http://user.example", "pingUrl": "http://prowlarr:9696"}}
				fake.items = []map[string]any{{"id": "user-tile", "kind": "app", "options": map[string]any{"appId": "user-app"}}}
			}
			srv := serveLinkedApps(t, fake)
			deps := systemtest.NewDeps(t)
			deps.Runner.(*systemtest.Runner).OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
				if strings.Contains(systemtest.Line(cmd), " ps ") {
					return system.Result{Stdout: []byte(`[{"Service":"prowlarr","State":"running"},{"Service":"homarr","State":"running"}]`)}
				}
				return system.Result{}
			})
			cat := catalog.New([]app.App{ownershipPeer{arr.Prowlarr()}, homarr.App{}}, nil)
			svc := service.New(deps, events.Discard(), cat)
			actionErr := svc.Install(t.Context(), service.InstallOptions{Targets: []string{"prowlarr", "homarr"}, Sets: []string{"homarr.port=" + clitest.Port(srv)}})
			stepID := "app-tiles"
			want := []string{"HOMARR_APP_IDS", "preserve all existing Apps", "self-host setup homarr"}
			if operation != "app" {
				if actionErr != nil {
					t.Fatal(actionErr)
				}
				p, err := svc.Project()
				if err != nil {
					t.Fatal(err)
				}
				secrets, err := p.LoadSecrets()
				if err != nil {
					t.Fatal(err)
				}
				key := ""
				for name := range secrets {
					if strings.HasPrefix(name, "HOMARR_WIDGETS_") {
						key = name
						delete(secrets, name)
					}
				}
				if key == "" {
					t.Fatal("fixture did not record widget ownership")
				}
				if err := p.SaveSecrets(secrets); err != nil {
					t.Fatal(err)
				}
				fake.mu.Lock()
				fake.items = append(fake.items, map[string]any{"id": "user-indexer", "kind": "indexerManager", "integrationIds": []any{fake.integrations[0]["id"]}, "options": map[string]any{}})
				fake.mu.Unlock()
				stepID = "link-prowlarr"
				want = []string{key, "empty JSON object to preserve all existing widgets", "self-host setup homarr"}
				if operation == "widget-unlink" {
					stepID = "unlink-prowlarr"
					actionErr = svc.Remove(t.Context(), []string{"prowlarr"}, false)
				} else {
					actionErr = svc.Setup(t.Context(), []string{"homarr"})
				}
			}
			if !errors.Is(actionErr, service.ErrManual) {
				t.Fatalf("ownership ambiguity should require Manual recovery: %v", actionErr)
			}
			later := service.New(deps, events.Discard(), cat)
			p, err := later.Project()
			if err != nil {
				t.Fatal(err)
			}
			state, err := p.LoadState()
			if err != nil {
				t.Fatal(err)
			}
			stored := state.Apps["homarr"].Steps[stepID]
			if stored.Status != stepstatus.StepManual || stored.Error != "" {
				t.Fatal("persisted step is not Manual")
			}
			for _, instruction := range want {
				if !strings.Contains(stored.Hint, instruction) {
					t.Errorf("persisted recovery hint omitted %q", instruction)
				}
			}
			status, err := later.Status(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var steps []service.StepStatus
			for _, installed := range status.Apps {
				if installed.ID == "homarr" {
					steps = installed.Steps
				}
			}
			debug, err := later.Debug(t.Context(), "homarr")
			if err != nil {
				t.Fatal(err)
			}
			for _, reported := range [][]service.StepStatus{steps, debug.FailedSteps} {
				found := false
				for _, step := range reported {
					if step.ID == stepID {
						found = true
						if step.Status != stepstatus.StepManual || step.Hint != stored.Hint || step.Error != "" {
							t.Fatal("later Status/Debug did not preserve the recorded Manual hint")
						}
					}
				}
				if !found {
					t.Fatal("later Status/Debug omitted the ownership Manual step")
				}
			}
			serialized, err := json.Marshal([]any{state, status, debug})
			if err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			password := fake.adminPassword
			fake.mu.Unlock()
			for _, secret := range []string{password, "keyid.secret", "synthetic-ownership-api-key"} {
				if secret != "" && strings.Contains(string(serialized), secret) {
					t.Fatal("persisted ownership recovery exposed a secret")
				}
			}
		})
	}
}
