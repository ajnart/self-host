package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/homarr"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
)

type externalConnectionTestConsumer struct {
	*testApp
	hold         bool
	removeHold   bool
	validations  int
	nativeLinks  int
	externalRuns int
	removed      int
}

func (a *externalConnectionTestConsumer) ValidateExternalConnection(c helpers.ExternalConnection) error {
	a.validations++
	if c.Kind != "testPeer" {
		return errors.New("unsupported external kind")
	}
	return nil
}

func (a *externalConnectionTestConsumer) Links() []app.Link {
	return []app.Link{app.NewLink("link-peer", "peer", "native peer", app.LinkStep{
		Message: "native peer", Do: func(context.Context, *helpers.H, *helpers.H) error { a.nativeLinks++; return nil },
	}, nil)}
}

func (a *externalConnectionTestConsumer) SetupExternalConnections(ctx context.Context, h *helpers.H) ([]string, error) {
	a.externalRuns++
	if a.nativeLinks == 0 {
		return nil, errors.New("external hook ran before normal Links")
	}
	connections, err := h.ExternalConnections()
	if err != nil {
		return nil, err
	}
	var failures []error
	removed := []string{}
	for _, c := range connections {
		step := "link-external-" + c.ID
		if c.Removing {
			step = "un" + step
		}
		err := h.Step(ctx, step, "configure external connection", helpers.StepSpec{Do: func(context.Context) error {
			if c.Removing {
				if a.removeHold {
					return &helpers.ManualError{Hint: "Restore only Homarr's API and retry self-host setup homarr."}
				}
				a.removed++
				return h.SetSecret(h.Key("linked_external-"+c.ID), "")
			}
			if a.hold {
				return &helpers.ManualError{Hint: "The native adapter is held before credential forwarding; retry after an audited transport repair."}
			}
			return h.SetSecret(h.Key("linked_external-"+c.ID), "owned-"+c.ID)
		}})
		if err == nil && c.Removing {
			removed = append(removed, c.ID)
		}
		failures = append(failures, err)
	}
	return removed, errors.Join(failures...)
}

func externalConnectionHarness(t *testing.T, homarrInstalled bool) (*harness, *externalConnectionTestConsumer) {
	t.Helper()
	consumer := &externalConnectionTestConsumer{testApp: &testApp{id: "homarr", done: map[string]bool{}}}
	h := newHarness(t, consumer, &testApp{id: "peer", done: map[string]bool{}})
	owner, _ := h.deps.CurrentUser()
	owner.UID, owner.GID = os.Getuid(), os.Getgid()
	h.deps.CurrentUser = func() (system.User, error) { return owner, nil }
	ids := []string{"peer"}
	if homarrInstalled {
		ids = append(ids, "homarr")
	}
	if err := h.install(ids...); err != nil {
		t.Fatal(err)
	}
	return h, consumer
}

func externalConnectionInputFile(t *testing.T, endpoint, key string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "private-input.json")
	raw := fmt.Sprintf("{\n  \"endpoint\": %q,\n  \"credentials\": [{\"kind\": \"apiKey\", \"value\": %q}],\n  \"selection\": {\"node\": \"private-node-791\"}\n}\n", endpoint, key)
	if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestExternalConnectionPrivateIdentityUpdateAndRemoval(t *testing.T) {
	h, consumer := externalConnectionHarness(t, true)
	before := len(h.runner.Calls())
	endpoint := "https://private-peer-791.example:8006"
	file := externalConnectionInputFile(t, endpoint, "private-api-key-791")
	first, err := h.svc.AddConnection(t.Context(), "testPeer", ConnectionOptions{File: file, Approved: true})
	if err != nil || !externalConnectionID.MatchString(first) {
		t.Fatalf("add: %v", err)
	}
	second, err := h.svc.AddConnection(t.Context(), "testPeer", ConnectionOptions{File: file, Approved: true})
	if err != nil || first == second {
		t.Fatal("same-kind connections did not receive independent opaque identities")
	}
	if err := h.svc.UpdateConnection(t.Context(), first, ConnectionOptions{File: file, Approved: true}); err != nil {
		t.Fatal(err)
	}
	p, _ := h.svc.Project()
	secrets, err := p.LoadSecrets()
	if err != nil || secrets[SecretKey("homarr", "linked_external-"+first)] != "owned-"+first {
		t.Fatal("rerun replaced ownership identity")
	}
	stat, _ := os.Stat(p.Path(".env"))
	if stat.Mode().Perm() != 0o600 {
		t.Fatal("external inputs were not stored privately")
	}
	settings, _ := p.LoadSettings()
	if _, public := settings[externalConnectionsKey]; public {
		t.Fatal("external inputs became public Host settings")
	}
	statuses, err := h.svc.Connections(t.Context(), "")
	if err != nil || len(statuses) != 2 || statuses[0].Setup != stepstatus.SetupOK {
		t.Fatalf("connection status: %v", err)
	}
	state, _ := p.LoadState()
	serialized, _ := json.Marshal([]any{settings, statuses, state, h.rec.events})
	for _, private := range []string{endpoint, "private-peer-791.example", "private-api-key-791", "private-node-791"} {
		if strings.Contains(string(serialized), private) {
			t.Fatal("external private input leaked into public state or events")
		}
	}
	changed := externalConnectionInputFile(t, "https://different-private-peer.example:8006", "new-private-api-key")
	if err := h.svc.UpdateConnection(t.Context(), first, ConnectionOptions{File: changed, Approved: true}); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatal("endpoint replacement was allowed to transfer ownership")
	}
	nativeBeforeRemoval := consumer.nativeLinks
	if err := h.svc.RemoveConnection(t.Context(), first, true); err != nil {
		t.Fatal(err)
	}
	if consumer.nativeLinks != nativeBeforeRemoval {
		t.Fatal("external removal reran unrelated native peer Links")
	}
	statuses, err = h.svc.Connections(t.Context(), "")
	if err != nil || len(statuses) != 1 || statuses[0].ID != second || consumer.removed != 1 {
		t.Fatal("removal did not stay scoped to one connection")
	}
	if len(h.runner.Calls()) != before {
		t.Fatal("connection-only actions invoked container or host commands")
	}
	if len(state.Apps) != 2 || !consumer.done["homarr"] || consumer.nativeLinks < 3 {
		t.Fatal("external connections changed normal App identity or Links")
	}
}

func TestExternalConnectionManualRecoveryAndDurableRemovalWithoutCredentials(t *testing.T) {
	h, consumer := externalConnectionHarness(t, true)
	consumer.hold = true
	file := externalConnectionInputFile(t, "https://private-held.example:8006", "held-private-key")
	id, err := h.svc.AddConnection(t.Context(), "testPeer", ConnectionOptions{File: file, Approved: true})
	if !errors.Is(err, ErrManual) || id == "" {
		t.Fatalf("held add: %v", err)
	}
	later := New(h.deps, h.svc.Log, h.svc.Catalog)
	statuses, err := later.Connections(t.Context(), id)
	if err != nil || statuses[0].Setup != stepstatus.SetupManual || !strings.Contains(statuses[0].Hint, "before credential forwarding") {
		t.Fatal("specific Manual recovery was not persisted")
	}
	consumer.hold = false
	if err := later.Setup(t.Context(), []string{"homarr"}); err != nil {
		t.Fatal(err)
	}
	ss, _ := later.open(t.Context())
	records, _ := ss.externalConnectionRecords()
	records.Connections[0].Endpoint = "malformed private endpoint"
	records.Connections[0].Credentials = nil
	if err := ss.saveExternalConnections(records); err != nil {
		t.Fatal(err)
	}
	validations := consumer.validations
	consumer.removeHold = true
	if err := later.RemoveConnection(t.Context(), id, true); !errors.Is(err, ErrManual) {
		t.Fatalf("remove should remain pending: %v", err)
	}
	statuses, err = later.Connections(t.Context(), id)
	if err != nil || !statuses[0].Removing || statuses[0].Setup != stepstatus.SetupManual || !strings.Contains(statuses[0].Hint, "Restore only Homarr") {
		t.Fatal("removal intent or recovery was lost")
	}
	if consumer.validations != validations {
		t.Fatal("removal required endpoint or credential validation")
	}
	consumer.removeHold = false
	if err := later.Setup(t.Context(), []string{"homarr"}); err != nil {
		t.Fatal(err)
	}
	statuses, err = later.Connections(t.Context(), "")
	if err != nil || len(statuses) != 0 || consumer.removed != 1 {
		t.Fatal("normal Homarr Setup did not finish durable connection removal")
	}
}

func TestExternalConnectionBeforeHomarrDoesNotInstallOrBlockLocalRemoval(t *testing.T) {
	h, consumer := externalConnectionHarness(t, false)
	before := len(h.runner.Calls())
	file := externalConnectionInputFile(t, "https://private-pending.example", "private-pending-key")
	id, err := h.svc.AddConnection(t.Context(), "testPeer", ConnectionOptions{File: file, Approved: true})
	if !errors.Is(err, ErrManual) || id == "" || consumer.externalRuns != 0 {
		t.Fatalf("pending connection: %v", err)
	}
	if err := h.svc.RemoveConnection(t.Context(), id, true); err != nil {
		t.Fatal(err)
	}
	if len(h.runner.Calls()) != before {
		t.Fatal("a pending external connection installed or managed a container")
	}
}

func TestExternalConnectionHeldLocalRemovalClearsHomarrNeedsAttention(t *testing.T) {
	h, consumer := externalConnectionHarness(t, true)
	consumer.hold = true
	file := externalConnectionInputFile(t, "https://private-unforwarded.example", "private-unforwarded-key")
	id, err := h.svc.AddConnection(t.Context(), "testPeer", ConnectionOptions{File: file, Approved: true})
	if !errors.Is(err, ErrManual) {
		t.Fatal("fixture did not establish an unforwarded Manual connection")
	}
	if err := h.svc.RemoveConnection(t.Context(), id, true); err != nil {
		t.Fatal(err)
	}
	p, _ := h.svc.Project()
	state, err := p.LoadState()
	if err != nil || state.Apps[dashboardAppID].Setup != stepstatus.SetupOK {
		t.Fatal("removing the held local connection left stale Homarr Needs attention")
	}
}

func TestExternalConnectionLocalRemovalDoesNotCompletePendingOwnSetup(t *testing.T) {
	h, _ := externalConnectionHarness(t, true)
	p, _ := h.svc.Project()
	state, _ := p.LoadState()
	state.Apps[dashboardAppID].Setup = stepstatus.SetupPending
	state.Apps[dashboardAppID].Steps = nil
	state.Apps[dashboardAppID].StepOrder = nil
	if err := p.SaveState(state); err != nil {
		t.Fatal(err)
	}
	file := externalConnectionInputFile(t, "https://private-pending-own-setup.example", "private-pending-own-key")
	id, err := h.svc.AddConnection(t.Context(), "testPeer", ConnectionOptions{File: file, Approved: true})
	if !errors.Is(err, ErrManual) {
		t.Fatal("fixture did not retain pending own Setup")
	}
	if err := h.svc.RemoveConnection(t.Context(), id, true); err != nil {
		t.Fatal(err)
	}
	state, err = p.LoadState()
	if err != nil || state.Apps[dashboardAppID].Setup != stepstatus.SetupPending {
		t.Fatal("local connection removal claimed unfinished Homarr Setup was ready")
	}
}

func TestExternalConnectionPrivateInputBoundaries(t *testing.T) {
	h, _ := externalConnectionHarness(t, false)
	for _, raw := range []string{
		`null`, `[]`, `{}`, `{"endpoint":"https://one.example","endpoint":"https://two.example"}`,
		`{"Endpoint":"https://private.example"}`,
		`{"endpoint":"https://one.example","Endpoint":"https://two.example"}`,
		`{"Endpoint":"https://one.example","endpoint":"https://two.example"}`,
		`{"endpoint":"https://user:password@private.example"}`, `{"endpoint":"https://private.example?token=private"}`,
		`{"endpoint":"https://private.example#private"}`, `{"endpoint":"https://private.example","kind":"testPeer"}`,
		`{"endpoint":"https://private.example","credentials":[{"kind":"apiKey","value":"one"},{"kind":"apiKey","value":"two"}]}`,
		strings.Repeat("x", maxPrivateInputBytes+1),
	} {
		h.deps.In = strings.NewReader(raw)
		if id, err := h.svc.AddConnection(t.Context(), "testPeer", ConnectionOptions{File: "-", Approved: true}); err == nil || id != "" {
			t.Fatal("invalid private input was saved")
		}
	}
	file := externalConnectionInputFile(t, "https://private-file.example", "file-private-key")
	for _, mode := range []os.FileMode{0o644, 0o666} {
		if err := os.Chmod(file, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := h.svc.AddConnection(t.Context(), "testPeer", ConnectionOptions{File: file, Approved: true}); err == nil {
			t.Fatal("public input file was accepted")
		}
	}
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "input-link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.AddConnection(t.Context(), "testPeer", ConnectionOptions{File: link, Approved: true}); err == nil {
		t.Fatal("symlink input was accepted")
	}
	if _, err := h.svc.AddConnection(t.Context(), "testPeer", ConnectionOptions{File: file}); err == nil {
		t.Fatal("unapproved private input was accepted")
	}
	statuses, err := h.svc.Connections(t.Context(), "")
	if err != nil || len(statuses) != 0 {
		t.Fatal("invalid input changed stored connections")
	}
}

func TestExternalConnectionRegistersPrivateFieldsOnLaterRead(t *testing.T) {
	h, _ := externalConnectionHarness(t, false)
	file := externalConnectionInputFile(t, "https://private-redaction.example:8006", "private-redaction-key")
	if _, err := h.svc.AddConnection(t.Context(), "testPeer", ConnectionOptions{File: file, Approved: true}); !errors.Is(err, ErrManual) {
		t.Fatal(err)
	}
	later := New(h.deps, events.Discard(), h.svc.Catalog)
	if _, err := later.Connections(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"https://private-redaction.example:8006", "private-redaction.example", "private-redaction-key", "private-node-791"} {
		if got := later.Log.Redact("failure " + value); strings.Contains(got, value) {
			t.Fatal("a JSON constituent was not registered for redaction")
		}
	}
}

func TestExternalConnectionMalformedRecordDoesNotBlockOrdinaryOperations(t *testing.T) {
	record := `{"id":"0123456789abcdef0123456789abcdef","kind":"testPeer","removing":false,"endpoint":"https://private-malformed.example:8006","credentials":[{"kind":"apiKey","value":"private-malformed-key"}],"selection":{"node":"private-malformed-node"}}`
	valid := `{"version":1,"connections":[` + record + `]}`
	scenarios := []struct {
		name string
		raw  string
	}{
		{"empty-object", `{}`},
		{"missing-connections", `{"version":1}`},
		{"missing-version", `{"connections":[]}`},
		{"missing-version-with-record", `{"connections":[` + record + `]}`},
		{"null-version", `{"version":null,"connections":[]}`},
		{"null-version-with-record", `{"version":null,"connections":[` + record + `]}`},
		{"null-connections", `{"version":1,"connections":null}`},
		{"wrong-version-type", `{"version":"1","connections":[]}`},
		{"wrong-connections-type", `{"version":1,"connections":{}}`},
		{"null-record", `{"version":1,"connections":[null]}`},
		{"empty-record", `{"version":1,"connections":[{}]}`},
		{"unknown-version", strings.Replace(valid, `"version":1`, `"version":2`, 1)},
		{"duplicate-version", strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1)},
		{"duplicate-connections", strings.Replace(valid, `"connections":[`, `"connections":[],"connections":[`, 1)},
		{"unknown-field", strings.TrimSuffix(valid, `}`) + `,"unexpected":true}`},
		{"trailing-json", valid + `{}`},
		{"truncated", strings.TrimSuffix(valid, `]}`)},
		{"null", `null`},
		{"array", `[]`},
		{"blank", ""},
		{"whitespace", " \n\t"},
		{"reported-proxmox-inventory-overwritten-by-alias", `{"version":1,"connections":[{"id":"0123456789abcdef0123456789abcdef","kind":"proxmox"}],"Connections":[]}`},
		{"uppercase-envelope", `{"VERSION":1,"CONNECTIONS":[]}`},
		{"mixed-case-duplicates", `{"version":1,"Version":1,"connections":[],"Connections":[]}`},
		{"unicode-folded-version", `{"ver\u017fion":1,"connections":[]}`},
		{"unicode-folded-connections", `{"version":1,"connections":[` + record + `],"connection\u017f":[]}`},
		{"unicode-folded-credential-kind", strings.Replace(valid, `"kind":"apiKey"`, `"\u212aind":"apiKey"`, 1)},
	}
	for _, field := range []struct {
		name      string
		canonical string
		alias     string
	}{
		{"version", `"version":1`, `"Version":1`},
		{"connections", `"connections":[` + record + `]`, `"Connections":[]`},
		{"id", `"id":"0123456789abcdef0123456789abcdef"`, `"ID":"fedcba9876543210fedcba9876543210"`},
		{"kind", `"kind":"testPeer"`, `"Kind":"proxmox"`},
		{"removing", `"removing":false`, `"Removing":true`},
		{"endpoint", `"endpoint":"https://private-malformed.example:8006"`, `"Endpoint":"https://private-malformed.example:8006"`},
		{"credentials", `"credentials":[{"kind":"apiKey","value":"private-malformed-key"}]`, `"Credentials":[]`},
		{"selection", `"selection":{"node":"private-malformed-node"}`, `"Selection":{"node":"private-malformed-node"}`},
		{"credential-kind", `"kind":"apiKey"`, `"Kind":"apiKey"`},
		{"credential-value", `"value":"private-malformed-key"`, `"Value":"private-malformed-key"`},
	} {
		scenarios = append(scenarios,
			struct{ name, raw string }{field.name + "-alias", strings.Replace(valid, field.canonical, field.alias, 1)},
			struct{ name, raw string }{field.name + "-canonical-first", strings.Replace(valid, field.canonical, field.canonical+","+field.alias, 1)},
			struct{ name, raw string }{field.name + "-alias-first", strings.Replace(valid, field.canonical, field.alias+","+field.canonical, 1)},
		)
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			h, consumer := externalConnectionHarness(t, true)
			p, _ := h.svc.Project()
			secrets, _ := p.LoadSecrets()
			raw := scenario.raw
			secrets[externalConnectionsKey] = raw
			secrets[SecretKey("homarr", "linked_external-0123456789abcdef0123456789abcdef")] = "owned-preserved"
			secrets[SecretKey("homarr", "widgets_owned-preserved")] = `{"external-shared-widget":true}`
			if err := p.SaveSecrets(secrets); err != nil {
				t.Fatal(err)
			}
			later := New(h.deps, events.New(h.rec), h.svc.Catalog)
			requests := 0
			h.deps.HTTP = &http.Client{Transport: tailscaleHTTP(func(*http.Request) (*http.Response, error) {
				requests++
				return nil, errors.New("unexpected request while external inventory is malformed")
			})}
			status, err := later.Status(t.Context())
			if err != nil {
				t.Fatal("malformed external record blocked ordinary status")
			}
			if err := later.Setup(t.Context(), []string{"peer"}); err != nil {
				t.Fatal("malformed external record blocked unrelated App Setup")
			}
			for _, id := range []string{"", "0123456789abcdef0123456789abcdef"} {
				connections, err := later.Connections(t.Context(), id)
				if err != nil || len(connections) != 1 || connections[0].Setup != stepstatus.SetupManual || len(connections[0].Steps) != 1 || connections[0].Steps[0].ID != "link-external-connections" || connections[0].Hint != externalConnectionsRecovery {
					t.Fatal("malformed record status omitted fixed Manual recovery")
				}
			}
			if len(status.Connections) != 1 || status.Connections[0].Setup != stepstatus.SetupManual {
				t.Fatal("ordinary status omitted the isolated connection recovery")
			}
			ss, err := later.open(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			reader := ss.helpers(consumer).ExternalConnections
			if connections, err := reader(); !errors.Is(err, ErrManual) || !errors.Is(err, helpers.ErrManual) || connections != nil {
				t.Fatal("ownership inventory reader interpreted malformed data as an empty desired set")
			}
			linksBefore, runsBefore := consumer.nativeLinks, consumer.externalRuns
			if _, err := ss.runSetup(t.Context(), consumer); err != nil {
				t.Fatal("malformed external inventory blocked core Setup")
			}
			if consumer.nativeLinks <= linksBefore || consumer.externalRuns != runsBefore+1 || !ss.ready(dashboardAppID) {
				t.Fatal("external inventory disrupted ordinary Catalog Links or own readiness")
			}
			removed, err := (homarr.App{}).SetupExternalConnections(t.Context(), ss.helpers(consumer))
			if !errors.Is(err, helpers.ErrManual) || len(removed) != 0 || ss.state.Apps[dashboardAppID].Steps["link-external-connections"].Status != stepstatus.StepManual || !ss.ready(dashboardAppID) {
				t.Fatal("real Homarr consumer did not retain isolated Manual inventory recovery")
			}
			file := externalConnectionInputFile(t, "https://private-input.example", "private-input-key")
			before := len(h.runner.Calls())
			if id, err := later.AddConnection(t.Context(), "testPeer", ConnectionOptions{File: file, Approved: true}); !errors.Is(err, ErrManual) || id != "" {
				t.Fatal("malformed inventory authorized add or overwrote the private record")
			}
			if err := later.UpdateConnection(t.Context(), "0123456789abcdef0123456789abcdef", ConnectionOptions{File: file, Approved: true}); !errors.Is(err, ErrManual) {
				t.Fatal("malformed inventory became a not-found record during update")
			}
			if err := later.RemoveConnection(t.Context(), "0123456789abcdef0123456789abcdef", true); !errors.Is(err, ErrManual) {
				t.Fatal("malformed record authorized removal")
			}
			if len(h.runner.Calls()) != before || consumer.externalRuns != runsBefore+1 || consumer.removed != 0 || requests != 0 {
				t.Fatal("malformed inventory actions invoked reconciliation or lifecycle work")
			}
			serialized, err := json.Marshal([]any{status, ss.state, h.rec.events})
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{"https://private-malformed.example:8006", "private-malformed.example", "private-malformed-key", "private-malformed-node", "https://private-input.example", "private-input-key", "private-node-791"} {
				if strings.Contains(string(serialized), value) {
					t.Fatal("private inventory leaked through status, state or events")
				}
			}
			if strings.Contains(raw, "private-malformed-key") {
				for _, value := range []string{"https://private-malformed.example:8006", "private-malformed.example", "private-malformed-key", "private-malformed-node"} {
					if strings.Contains(later.Log.Redact(value), value) {
						t.Fatal("malformed record skipped early private-field masking")
					}
				}
			}
			stored, err := p.LoadSecrets()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored, secrets) {
				t.Fatal("unknown configuration discarded private records or binding ownership")
			}
		})
	}
}

func TestExternalConnectionAbsentAndExplicitEmptyInventoriesRemainValid(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprintf("present-%t", present), func(t *testing.T) {
			h, consumer := externalConnectionHarness(t, true)
			p, _ := h.svc.Project()
			if present {
				secrets, err := p.LoadSecrets()
				if err != nil {
					t.Fatal(err)
				}
				secrets[externalConnectionsKey] = `{"version":1,"connections":[]}`
				if err := p.SaveSecrets(secrets); err != nil {
					t.Fatal(err)
				}
			}
			ss, err := h.svc.open(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			records, err := ss.externalConnectionRecords()
			if err != nil || records.Version != 1 || records.Connections == nil || len(records.Connections) != 0 {
				t.Fatal("absent or explicitly empty inventory became invalid")
			}
			if statuses, err := h.svc.Connections(t.Context(), ""); err != nil || len(statuses) != 0 {
				t.Fatal("empty inventory produced a Manual status")
			}
			reader := ss.helpers(consumer).ExternalConnections
			if connections, err := reader(); err != nil || connections == nil || len(connections) != 0 {
				t.Fatal("empty inventory reader failed")
			}
			id := "0123456789abcdef0123456789abcdef"
			if _, err := h.svc.Connections(t.Context(), id); err == nil || errors.Is(err, ErrManual) {
				t.Fatal("missing identity did not retain its distinct not-found status")
			}
			file := externalConnectionInputFile(t, "https://private-empty.example", "private-empty-key")
			if err := h.svc.UpdateConnection(t.Context(), id, ConnectionOptions{File: file, Approved: true}); err == nil || errors.Is(err, ErrManual) {
				t.Fatal("missing identity did not retain its distinct not-found update error")
			}
			if err := h.svc.RemoveConnection(t.Context(), id, true); err == nil || errors.Is(err, ErrManual) {
				t.Fatal("missing identity did not retain its distinct not-found removal error")
			}
			id, err = h.svc.AddConnection(t.Context(), "testPeer", ConnectionOptions{File: file, Approved: true})
			if err != nil || id == "" {
				t.Fatal("valid empty inventory rejected add")
			}
			if err := h.svc.UpdateConnection(t.Context(), id, ConnectionOptions{File: file, Approved: true}); err != nil {
				t.Fatal(err)
			}
			if err := h.svc.RemoveConnection(t.Context(), id, true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExternalConnectionCanonicalFieldsPreserveOpaqueKeys(t *testing.T) {
	h, _ := externalConnectionHarness(t, true)
	input := externalConnectionInput{
		Endpoint: "https://private-opaque.example",
		Credentials: []externalCredentialRecord{
			{Kind: "tokenId", Value: "private-token-id"},
			{Kind: "opnsenseApiKey", Value: "private-opnsense-key"},
			{Kind: "apiKey", Value: "private-lowercase-key"},
			{Kind: "ApiKey", Value: "private-uppercase-key"},
		},
		Selection: map[string]string{
			"node": "private-node-lowercase", "Node": "private-node-uppercase",
			"Connections": "private-connections", "ſelection": "private-selection",
		},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	h.deps.In = strings.NewReader(string(raw))
	id, err := h.svc.AddConnection(t.Context(), "testPeer", ConnectionOptions{File: "-", Approved: true})
	if err != nil || id == "" {
		t.Fatal("canonical input rejected opaque credential kinds or selection keys")
	}
	ss, err := h.svc.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	records, err := ss.externalConnectionRecords()
	if err != nil || len(records.Connections) != 1 || !reflect.DeepEqual(records.Connections[0].externalConnectionInput, input) {
		t.Fatal("canonical inventory normalized or discarded opaque private keys or values")
	}
	h.deps.In = strings.NewReader(string(raw))
	if err := h.svc.UpdateConnection(t.Context(), id, ConnectionOptions{File: "-", Approved: true}); err != nil {
		t.Fatal("canonical update rejected opaque private keys")
	}
	if err := h.svc.RemoveConnection(t.Context(), id, true); err != nil {
		t.Fatal(err)
	}
}

func TestExternalConnectionStorageFailureIsNotInventoryRecovery(t *testing.T) {
	h, _ := externalConnectionHarness(t, true)
	p, err := h.svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p.Path(".env"), p.Path("private-storage-backup")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p.Path(".env"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Connections(t.Context(), ""); err == nil || errors.Is(err, ErrManual) || errors.Is(err, helpers.ErrManual) {
		t.Fatal("storage failure was converted to an empty inventory or Manual inventory recovery")
	}
}
