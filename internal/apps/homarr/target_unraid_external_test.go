package homarr_test

import (
	"encoding/json"
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
	"github.com/ajnart/self-host/internal/service"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

type unraidExternalTransport struct {
	inner          http.RoundTripper
	providerReads  int
	nativeRequests int
}

func (u *unraidExternalTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() != "127.0.0.1" {
		u.providerReads++
		return nil, errors.New("the held Unraid source path attempted an external request")
	}
	for _, procedure := range []string{"integration.create", "integration.update", "widget.healthMonitoring", "widget.stats"} {
		if strings.HasPrefix(r.URL.Path, "/api/trpc/"+procedure) {
			u.nativeRequests++
		}
	}
	return u.inner.RoundTrip(r)
}

func TestUnraidExternalServicePersistsHoldAndRemovesOnlyLocalConfiguration(t *testing.T) {
	fake := newFake()
	fake.version = "2.10.1"
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	deps := systemtest.NewDeps(t)
	owner, _ := deps.CurrentUser()
	owner.UID, owner.GID = os.Getuid(), os.Getgid()
	deps.CurrentUser = func() (system.User, error) { return owner, nil }
	transport := &unraidExternalTransport{inner: http.DefaultTransport}
	deps.HTTP = &http.Client{Transport: transport}
	cat := catalog.New([]app.App{homarr.App{}}, nil)
	svc := service.New(deps, events.Discard(), cat)
	if err := svc.Install(t.Context(), service.InstallOptions{Targets: []string{"homarr"}, Sets: []string{"homarr.port=" + clitest.Port(srv)}}); err != nil {
		t.Fatal(err)
	}
	input := `{"endpoint":"https://private-unraid-187.example.invalid","credentials":[{"kind":"apiKey","value":"private-unraid-key-187"}]}`
	file := filepath.Join(t.TempDir(), "private-input.json")
	if err := os.WriteFile(file, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := svc.AddConnection(t.Context(), "unraid", service.ConnectionOptions{File: file, Approved: true})
	if !errors.Is(err, service.ErrManual) || id == "" {
		t.Fatalf("Unraid safety hold: %v", err)
	}
	later := service.New(deps, events.Discard(), cat)
	connections, err := later.Connections(t.Context(), id)
	if err != nil || len(connections) != 1 || connections[0].Setup != stepstatus.SetupManual || !strings.Contains(connections[0].Hint, "before credential forwarding") {
		t.Fatal("later connection status lost the specific safety hold")
	}
	serialized, _ := json.Marshal(connections)
	for _, private := range []string{"private-unraid-187", "private-unraid-key-187"} {
		if strings.Contains(string(serialized), private) {
			t.Fatal("external private input reached a later public report")
		}
	}
	if transport.providerReads != 0 || transport.nativeRequests != 0 {
		t.Fatal("Manual Setup forwarded credentials or made provider/native requests")
	}
	if err := later.RemoveConnection(t.Context(), id, true); err != nil {
		t.Fatal(err)
	}
	connections, err = later.Connections(t.Context(), "")
	if err != nil || len(connections) != 0 || transport.providerReads != 0 || transport.nativeRequests != 0 {
		t.Fatal("disconnect required a provider or native request")
	}
}
