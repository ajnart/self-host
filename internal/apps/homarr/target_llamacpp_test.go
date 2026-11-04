package homarr_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
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

func TestLlamacppExternalValidationRejectsUnsafeShapes(t *testing.T) {
	cases := map[string]helpers.ExternalConnection{
		"wrong kind":      {Kind: "llama", Endpoint: "http://llama.invalid:8080"},
		"embedded auth":   {Kind: "llamacpp", Endpoint: "http://user:pass@llama.invalid:8080"},
		"query":           {Kind: "llamacpp", Endpoint: "http://llama.invalid:8080/?model=x"},
		"api path":        {Kind: "llamacpp", Endpoint: "http://llama.invalid:8080/v1"},
		"fragment":        {Kind: "llamacpp", Endpoint: "http://llama.invalid:8080/#x"},
		"scheme":          {Kind: "llamacpp", Endpoint: "ftp://llama.invalid:8080"},
		"bad port":        {Kind: "llamacpp", Endpoint: "http://llama.invalid:99999"},
		"api key":         {Kind: "llamacpp", Endpoint: "http://llama.invalid:8080", Credentials: []helpers.ExternalCredential{{Kind: "apiKey", Value: "private-key-163"}}},
		"selection given": {Kind: "llamacpp", Endpoint: "http://llama.invalid:8080", Selection: map[string]string{"model": "x"}},
	}
	for name, c := range cases {
		err := homarr.App{}.ValidateExternalConnection(c)
		if err == nil {
			t.Fatalf("%s was accepted", name)
		}
		if strings.Contains(err.Error(), "private-key-163") {
			t.Fatalf("%s leaked a credential", name)
		}
	}
	if err := (homarr.App{}).ValidateExternalConnection(helpers.ExternalConnection{Kind: "llamacpp", Endpoint: "http://llama.invalid:8080"}); err != nil {
		t.Fatalf("plain endpoint rejected: %v", err)
	}
}

type llamacppTransport struct {
	inner    http.RoundTripper
	external int
	native   int
}

func (p *llamacppTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() != "127.0.0.1" {
		p.external++
		return nil, errors.New("the held llama.cpp path attempted an external request")
	}
	for _, procedure := range []string{"integration.create", "integration.update", "widget.llamacpp", "widget.stats"} {
		if strings.HasPrefix(r.URL.Path, "/api/trpc/"+procedure) {
			p.native++
		}
	}
	return p.inner.RoundTrip(r)
}

func TestLlamacppExternalServicePersistsManualHoldWithoutTouchingPeer(t *testing.T) {
	fake := newFake()
	fake.version = "2.10.1"
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	deps := systemtest.NewDeps(t)
	owner, _ := deps.CurrentUser()
	owner.UID, owner.GID = os.Getuid(), os.Getgid()
	deps.CurrentUser = func() (system.User, error) { return owner, nil }
	transport := &llamacppTransport{inner: http.DefaultTransport}
	deps.HTTP = &http.Client{Transport: transport}
	cat := catalog.New([]app.App{homarr.App{}}, nil)
	svc := service.New(deps, events.Discard(), cat)
	if err := svc.Install(t.Context(), service.InstallOptions{Targets: []string{"homarr"}, Sets: []string{"homarr.port=" + clitest.Port(srv)}}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "private-input.json")
	if err := os.WriteFile(file, []byte(`{"endpoint":"http://private-llama-163.example.invalid:8080"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := svc.AddConnection(t.Context(), "llamacpp", service.ConnectionOptions{File: file, Approved: true})
	if !errors.Is(err, service.ErrManual) || id == "" {
		t.Fatalf("llama.cpp hold: %v", err)
	}
	later := service.New(deps, events.Discard(), cat)
	connections, err := later.Connections(t.Context(), id)
	if err != nil || len(connections) != 1 || connections[0].Setup != stepstatus.SetupManual || !strings.Contains(connections[0].Hint, "wake a sleeping model") {
		t.Fatal("later connection status lost the llama.cpp hold hint")
	}
	if strings.Contains(connections[0].Hint, "private-llama-163") {
		t.Fatal("endpoint reached the public hint")
	}
	if transport.external != 0 || transport.native != 0 {
		t.Fatal("held Setup contacted the peer or created a native integration")
	}
	if err := later.RemoveConnection(t.Context(), id, true); err != nil {
		t.Fatal(err)
	}
	if transport.external != 0 || transport.native != 0 {
		t.Fatal("disconnect contacted the peer")
	}
}

func TestLlamacppExternalServiceRejectsSuppliedCredentialBeforeStoring(t *testing.T) {
	fake := newFake()
	fake.version = "2.10.1"
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	deps := systemtest.NewDeps(t)
	owner, _ := deps.CurrentUser()
	owner.UID, owner.GID = os.Getuid(), os.Getgid()
	deps.CurrentUser = func() (system.User, error) { return owner, nil }
	cat := catalog.New([]app.App{homarr.App{}}, nil)
	svc := service.New(deps, events.Discard(), cat)
	if err := svc.Install(t.Context(), service.InstallOptions{Targets: []string{"homarr"}, Sets: []string{"homarr.port=" + clitest.Port(srv)}}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "private-input.json")
	if err := os.WriteFile(file, []byte(`{"endpoint":"http://llama.invalid:8080","credentials":[{"kind":"apiKey","value":"private-key-163"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := svc.AddConnection(t.Context(), "llamacpp", service.ConnectionOptions{File: file, Approved: true})
	if err == nil || id != "" || strings.Contains(err.Error(), "private-key-163") {
		t.Fatalf("authenticated llama.cpp was not rejected cleanly: %v", err)
	}
	connections, err := svc.Connections(t.Context(), "")
	if err != nil || len(connections) != 0 {
		t.Fatalf("rejected connection was stored: %v %d", err, len(connections))
	}
}
