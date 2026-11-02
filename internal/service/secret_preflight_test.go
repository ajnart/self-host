package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type secretPrerequisiteApp struct {
	testApp
	requireExisting bool
	pureChecks      int
	credentialReads []string
}

func (a *secretPrerequisiteApp) Inputs() []app.Input {
	return []app.Input{{Key: "persistent_key", Private: true}, {Key: "mode", Default: "selected"}}
}

func (a *secretPrerequisiteApp) Secrets() []app.SecretSpec {
	return []app.SecretSpec{{Name: "persistent_key", Length: 48}}
}

func (a *secretPrerequisiteApp) Preflight(_ context.Context, h *helpers.H) error {
	if h.Secrets == nil {
		a.pureChecks++
		return nil
	}
	key := h.Secret(h.Key("persistent_key"))
	a.credentialReads = append(a.credentialReads, key)
	if h.Port != 19001 || h.Inputs["mode"] != "selected" || h.Inputs["persistent_key"] != key || h.Settings["TZ"] == "" {
		return errors.New("prerequisite did not receive authoritative credentials and effective context")
	}
	if a.requireExisting && key == "" {
		return &helpers.ManualError{Hint: "restore the existing persistent key before generation or startup"}
	}
	return nil
}

func newSecretPrerequisiteApp() *secretPrerequisiteApp {
	return &secretPrerequisiteApp{testApp: testApp{id: "prerequisite", done: map[string]bool{}}, requireExisting: true}
}

func TestSecretPrerequisiteInstallBeforeGeneration(t *testing.T) {
	a := newSecretPrerequisiteApp()
	h := newHarness(t, a)
	err := h.svc.Install(t.Context(), InstallOptions{Targets: []string{"prerequisite"}, Sets: []string{"prerequisite.port=19001"}})
	if !errors.Is(err, helpers.ErrManual) || a.pureChecks != 1 || len(a.credentialReads) != 1 || a.credentialReads[0] != "" {
		t.Fatalf("missing prerequisite was not held before generation: %v", err)
	}
	p, err := h.svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	values, err := p.LoadSecrets()
	if err != nil || values["PREREQUISITE_PERSISTENT_KEY"] != "" {
		t.Fatal("a rejected prerequisite generated a replacement key")
	}
	if len(h.composeCalls()) != 0 {
		t.Fatal("a rejected prerequisite pulled or started the App")
	}
}

func TestSecretPrerequisiteReceivesPrivateInputsAndPreservesStartupChecks(t *testing.T) {
	a := newSecretPrerequisiteApp()
	h := newHarness(t, a)
	key := "synthetic-preserved-key"
	opts := InstallOptions{
		Targets: []string{"prerequisite"}, Sets: []string{"prerequisite.port=19001"},
		PrivateInputs: map[string]map[string]string{"prerequisite": {"persistent_key": key}},
	}
	if err := h.svc.Install(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	if a.pureChecks != 1 || len(a.credentialReads) != 2 || a.credentialReads[0] != key || a.credentialReads[1] != key {
		t.Fatal("Install did not preserve pure admission and the credential-aware pre-generation/startup checks")
	}
	opts.PrivateInputs = nil
	if err := h.svc.Install(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	p, err := h.svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	values, err := p.LoadSecrets()
	if err != nil || values["PREREQUISITE_PERSISTENT_KEY"] != key {
		t.Fatal("rerunning Install did not reuse the authoritative saved key")
	}
}

func TestSecretPrerequisiteDoesNotPreventOtherApprovedGeneration(t *testing.T) {
	a := newSecretPrerequisiteApp()
	a.requireExisting = false
	h := newHarness(t, a)
	if err := h.svc.Install(t.Context(), InstallOptions{Targets: []string{"prerequisite"}, Sets: []string{"prerequisite.port=19001"}}); err != nil {
		t.Fatal(err)
	}
	if a.pureChecks != 1 || len(a.credentialReads) != 2 || a.credentialReads[0] != "" || len(a.credentialReads[1]) != 48 {
		t.Fatal("existing generation or necessary post-generation startup validation changed")
	}
}

func TestSecretPrerequisiteRefreshBeforeGenerationAndFragmentWrite(t *testing.T) {
	a := newSecretPrerequisiteApp()
	h := newHarness(t, a)
	if err := h.svc.Install(t.Context(), InstallOptions{
		Targets: []string{"prerequisite"}, Sets: []string{"prerequisite.port=19001"},
		PrivateInputs: map[string]map[string]string{"prerequisite": {"persistent_key": "synthetic-preserved-key"}},
	}); err != nil {
		t.Fatal(err)
	}
	p, err := h.svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := p.ReadFragment("prerequisite")
	if err != nil {
		t.Fatal(err)
	}
	values, err := p.LoadSecrets()
	if err != nil {
		t.Fatal(err)
	}
	delete(values, "PREREQUISITE_PERSISTENT_KEY")
	if err := p.SaveSecrets(values); err != nil {
		t.Fatal(err)
	}
	before := len(h.composeCalls())
	a.credentialReads = nil
	a.image = "example/test:updated"
	if err := h.svc.Update(t.Context(), []string{"prerequisite"}); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("refresh bypassed the missing-key prerequisite: %v", err)
	}
	after, err := p.ReadFragment("prerequisite")
	if err != nil || string(fragment) != string(after) || strings.Contains(string(after), "example/test:updated") {
		t.Fatal("refresh changed the fragment before its prerequisite passed")
	}
	values, err = p.LoadSecrets()
	if err != nil || values["PREREQUISITE_PERSISTENT_KEY"] != "" || len(a.credentialReads) != 1 || a.credentialReads[0] != "" {
		t.Fatal("refresh regenerated the lost persistent key before refusal")
	}
	if len(h.composeCalls()) != before {
		t.Fatal("held refresh pulled or started the App")
	}
}
