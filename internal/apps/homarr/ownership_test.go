package homarr_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
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

func TestExternalConnectionMalformedConfigurationKeepsHomarrOwnSetupReady(t *testing.T) {
	for _, scenario := range []string{"malformed-json", "external-widget-ownership"} {
		t.Run(scenario, func(t *testing.T) {
			fake := newFake()
			fake.version = "2.10.1"
			srv := httptest.NewServer(fake.handler())
			t.Cleanup(srv.Close)
			deps := systemtest.NewDeps(t)
			cat := catalog.New([]app.App{homarr.App{}}, nil)
			svc := service.New(deps, events.Discard(), cat)
			if err := svc.Install(t.Context(), service.InstallOptions{Targets: []string{"homarr"}, Sets: []string{"homarr.port=" + clitest.Port(srv)}}); err != nil {
				t.Fatal(err)
			}
			p, _ := svc.Project()
			secrets, _ := p.LoadSecrets()
			raw := `{"version":1,"connections":`
			stepID := "link-external-connections"
			if scenario == "external-widget-ownership" {
				raw = `{"version":1,"connections":[{"id":"0123456789abcdef0123456789abcdef","kind":"unknown"}]}`
				stepID = "link-external-0123456789abcdef0123456789abcdef"
			}
			secrets["HOMARR_EXTERNAL_CONNECTIONS"] = raw
			secrets["HOMARR_LINKED_EXTERNAL_0123456789ABCDEF0123456789ABCDEF"] = "external-preserved"
			secrets["HOMARR_WIDGETS_EXTERNAL_PRESERVED"] = "null"
			if err := p.SaveSecrets(secrets); err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			before, _ := json.Marshal([]any{fake.apps, fake.items, fake.integrations})
			fake.mu.Unlock()
			later := service.New(deps, events.Discard(), cat)
			if err := later.Setup(t.Context(), []string{"homarr"}); !errors.Is(err, service.ErrManual) {
				t.Fatal("external recovery did not remain Manual")
			}
			state, _ := p.LoadState()
			st := state.Apps["homarr"]
			if st.Steps[stepID].Status != stepstatus.StepManual || st.Steps["check"].Status != stepstatus.StepOK {
				t.Fatal("external recovery blocked Homarr's own Check or disappeared")
			}
			for id, step := range st.Steps {
				if !strings.HasPrefix(id, "link-") && step.Status.NeedsAttention() {
					t.Fatal("external configuration made Homarr's own Setup unready")
				}
			}
			status, err := later.Status(t.Context())
			if err != nil || len(status.Apps) != 1 {
				t.Fatal("external recovery blocked ordinary status")
			}
			fake.mu.Lock()
			after, _ := json.Marshal([]any{fake.apps, fake.items, fake.integrations})
			fake.mu.Unlock()
			stored, _ := p.LoadSecrets()
			if string(before) != string(after) || stored["HOMARR_EXTERNAL_CONNECTIONS"] != raw || stored["HOMARR_WIDGETS_EXTERNAL_PRESERVED"] != "null" {
				t.Fatal("unknown external configuration authorized resource or ownership changes")
			}
		})
	}
}

type homarrWithoutEnumeration struct{ homarr.App }

func (a homarrWithoutEnumeration) Setup(ctx context.Context, h *helpers.H) error {
	h.InstalledApps = nil
	return a.App.Setup(ctx, h)
}

func TestExternalConnectionCorruptHomeBindingKeepsOwnSetupAndSafeLinksReady(t *testing.T) {
	for _, enumeration := range []bool{true, false} {
		name := "installed-apps"
		if !enumeration {
			name = "no-enumeration"
		}
		t.Run(name, func(t *testing.T) {
			fake := newFake()
			srv := serveLinkedApps(t, fake)
			deps := systemtest.NewDeps(t)
			var dashboard app.App = homarr.App{}
			if !enumeration {
				dashboard = homarrWithoutEnumeration{}
			}
			cat := catalog.New([]app.App{dashboard, navigationTarget{}, ownershipPeer{arr.Prowlarr()}}, nil)
			svc := service.New(deps, events.Discard(), cat)
			if err := svc.Install(t.Context(), service.InstallOptions{Targets: []string{"prowlarr", "vaultwarden", "homarr"}, Sets: []string{"homarr.port=" + clitest.Port(srv)}}); err != nil {
				t.Fatal(err)
			}
			p, _ := svc.Project()
			secrets, _ := p.LoadSecrets()
			secrets["HOMARR_EXTERNAL_CONNECTIONS"] = `{"version":1,"connections":[{"id":"0123456789abcdef0123456789abcdef","kind":"unknown"}]}`
			secrets["HOMARR_LINKED_EXTERNAL_0123456789ABCDEF0123456789ABCDEF"] = "external-preserved"
			secrets["HOMARR_WIDGETS_EXTERNAL_PRESERVED"] = "null"
			if err := p.SaveSecrets(secrets); err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			protected := []map[string]any{
				{"id": "unknown-home", "kind": "healthMonitoring", "integrationIds": []any{"external-preserved"}, "options": map[string]any{"custom": true}},
				{"id": "user-cross-kind", "kind": "calendar", "integrationIds": []any{"user-survivor"}, "options": map[string]any{"custom": "unchanged"}},
			}
			fake.integrations = append(fake.integrations, map[string]any{"id": "external-preserved", "kind": "unknown"}, map[string]any{"id": "user-survivor", "kind": "sonarr"})
			fake.items = append(fake.items, protected...)
			for _, item := range fake.items {
				if item["kind"] == "stats" {
					item["integrationIds"] = append(item["integrationIds"].([]any), "external-preserved")
					protected = append(protected, item)
				}
				if item["kind"] == "notebook" {
					item["size"] = map[string]any{"width": 1, "height": 1}
				}
			}
			for _, record := range fake.apps {
				if record["name"] == "Vaultwarden" {
					record["href"] = "https://outdated.invalid"
				}
			}
			before, _ := json.Marshal([]any{protected, fake.integrations})
			fake.mu.Unlock()
			if err := svc.Setup(t.Context(), []string{"homarr"}); !errors.Is(err, service.ErrManual) {
				t.Fatalf("connection recovery: %v", err)
			}
			state, _ := p.LoadState()
			st := state.Apps["homarr"]
			safeLink := st.Steps["link-vaultwarden"].Status
			if st.Steps["check"].Status != stepstatus.StepOK || safeLink != stepstatus.StepOK && safeLink != stepstatus.StepSkip {
				t.Fatal("bound external ownership blocked own Check or an ordinary safe Catalog Link")
			}
			for id, step := range st.Steps {
				if !strings.HasPrefix(id, "link-") && step.Status.NeedsAttention() {
					t.Fatal("bound external ownership made own Setup unready")
				}
			}
			if st.Steps["link-prowlarr"].Status != stepstatus.StepManual || st.Steps["link-external-0123456789abcdef0123456789abcdef"].Status != stepstatus.StepManual {
				t.Fatal("ownership-sensitive shared reconciliation or external recovery was not held at its Link boundary")
			}
			if !strings.Contains(st.Steps["link-external-0123456789abcdef0123456789abcdef"].Hint, "HOMARR_WIDGETS_EXTERNAL_PRESERVED") {
				t.Fatal("external Manual recovery did not identify the corrupt private ownership record")
			}
			fake.mu.Lock()
			afterProtected := []map[string]any{}
			for _, expected := range protected {
				for _, item := range fake.items {
					if item["id"] == expected["id"] {
						afterProtected = append(afterProtected, item)
					}
				}
			}
			after, _ := json.Marshal([]any{afterProtected, fake.integrations})
			fake.mu.Unlock()
			stored, _ := p.LoadSecrets()
			if string(before) != string(after) || stored["HOMARR_WIDGETS_EXTERNAL_PRESERVED"] != "null" || stored["HOMARR_EXTERNAL_CONNECTIONS"] != secrets["HOMARR_EXTERNAL_CONNECTIONS"] {
				t.Fatal("unknown bindings, cross-kind survivors or private raw ownership changed")
			}
		})
	}
}
