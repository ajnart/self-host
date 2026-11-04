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
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/service"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

type icalTransport struct {
	inner          http.RoundTripper
	feedRequests   int
	nativeRequests int
}

func (p *icalTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() != "127.0.0.1" {
		p.feedRequests++
		return nil, errors.New("the held iCal path attempted an external request")
	}
	for _, procedure := range []string{"integration.create", "integration.update", "widget.calendar"} {
		if strings.HasPrefix(r.URL.Path, "/api/trpc/"+procedure) {
			p.nativeRequests++
		}
	}
	return p.inner.RoundTrip(r)
}

func icalConnection(endpoint, feed string) helpers.ExternalConnection {
	return helpers.ExternalConnection{ID: "c1", Kind: "ical", Endpoint: endpoint, Credentials: []helpers.ExternalCredential{{Kind: "url", Value: feed}}}
}

func TestICalExternalValidation(t *testing.T) {
	valid := icalConnection("https://calendar.example.invalid", "https://calendar.example.invalid/private/token/feed.ics?key=abc")
	if err := (homarr.App{}).ValidateExternalConnection(valid); err != nil {
		t.Fatal(err)
	}
	cases := map[string]helpers.ExternalConnection{
		"http origin":      icalConnection("http://calendar.example.invalid", "https://calendar.example.invalid/f.ics"),
		"origin with path": icalConnection("https://calendar.example.invalid/private", "https://calendar.example.invalid/private/f.ics"),
		"other host":       icalConnection("https://calendar.example.invalid", "https://other.example.invalid/f.ics"),
		"userinfo":         icalConnection("https://calendar.example.invalid", "https://user:pw@calendar.example.invalid/f.ics"),
		"fragment":         icalConnection("https://calendar.example.invalid", "https://calendar.example.invalid/f.ics#x"),
		"empty":            icalConnection("https://calendar.example.invalid", ""),
	}
	extra := valid
	extra.Credentials = append(append([]helpers.ExternalCredential{}, valid.Credentials...), helpers.ExternalCredential{Kind: "apiKey", Value: "x"})
	cases["extra credential"] = extra
	selected := valid
	selected.Selection = map[string]string{"calendar": "x"}
	cases["selection"] = selected
	for name, c := range cases {
		if err := (homarr.App{}).ValidateExternalConnection(c); err == nil {
			t.Fatalf("%s accepted", name)
		} else if strings.Contains(err.Error(), "private") && strings.Contains(err.Error(), "token") {
			t.Fatalf("%s error leaked the feed: %v", name, err)
		}
	}
}

func TestICalExternalServicePersistsHoldWithoutForwardingFeed(t *testing.T) {
	fake := newFake()
	fake.version = "2.10.1"
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	deps := systemtest.NewDeps(t)
	owner, _ := deps.CurrentUser()
	owner.UID, owner.GID = os.Getuid(), os.Getgid()
	deps.CurrentUser = func() (system.User, error) { return owner, nil }
	transport := &icalTransport{inner: http.DefaultTransport}
	deps.HTTP = &http.Client{Transport: transport}
	cat := catalog.New([]app.App{homarr.App{}}, nil)
	svc := service.New(deps, events.Discard(), cat)
	if err := svc.Install(t.Context(), service.InstallOptions{Targets: []string{"homarr"}, Sets: []string{"homarr.port=" + clitest.Port(srv)}}); err != nil {
		t.Fatal(err)
	}
	input := `{"endpoint":"https://private-cal-157.example.invalid","credentials":[{"kind":"url","value":"https://private-cal-157.example.invalid/secret-path-157/feed.ics?token=secret-token-157"}]}`
	file := filepath.Join(t.TempDir(), "private-input.json")
	if err := os.WriteFile(file, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := svc.AddConnection(t.Context(), "ical", service.ConnectionOptions{File: file, Approved: true})
	if !errors.Is(err, service.ErrManual) || id == "" {
		t.Fatalf("iCal safety hold: %v", err)
	}
	later := service.New(deps, events.Discard(), cat)
	connections, err := later.Connections(t.Context(), id)
	if err != nil || len(connections) != 1 || connections[0].Setup != stepstatus.SetupManual || !strings.Contains(connections[0].Hint, "cannot yet guarantee private feed addresses") {
		t.Fatal("later connection status lost the iCal safety hold")
	}
	debug, err := later.Debug(t.Context(), "homarr")
	if err != nil || len(debug.FailedSteps) == 0 || debug.FailedSteps[0].Hint != connections[0].Hint {
		t.Fatal("later Debug did not retain the iCal recovery hint")
	}
	serialized, _ := json.Marshal([]any{connections, debug})
	for _, private := range []string{"private-cal-157", "secret-path-157", "secret-token-157"} {
		if strings.Contains(string(serialized), private) {
			t.Fatal("the private feed reached a later public report")
		}
	}
	if transport.feedRequests != 0 || transport.nativeRequests != 0 {
		t.Fatal("held iCal Setup requested the feed or mutated Homarr")
	}
	if err := later.RemoveConnection(t.Context(), id, true); err != nil {
		t.Fatal(err)
	}
	connections, err = later.Connections(t.Context(), "")
	if err != nil || len(connections) != 0 || transport.feedRequests != 0 || transport.nativeRequests != 0 {
		t.Fatal("disconnect required a feed or native request")
	}
}
