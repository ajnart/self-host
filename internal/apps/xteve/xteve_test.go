package xteve_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/xteve"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
)

type fakeSecrets map[string]string

func (s fakeSecrets) Get(key string) string       { return s[key] }
func (s fakeSecrets) Set(key, value string) error { s[key] = value; return nil }

type fakeXteve struct {
	mu          sync.Mutex
	auth        bool
	apiDisabled bool
	identity    string
	streams     any
	rawStatus   map[string]any
	tokens      map[string]bool
	issued      int
	logins      int
	statuses    int
	statusReply int
}

func newFake() *fakeXteve {
	return &fakeXteve{auth: true, identity: "xteve", streams: map[string]any{"all": 1, "active": 1}, tokens: map[string]bool{}}
}

func (f *fakeXteve) issue() string {
	f.issued++
	token := fmt.Sprintf("token-%d", f.issued)
	f.tokens[token] = true
	return token
}

func (f *fakeXteve) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/discover.json" {
		json.NewEncoder(w).Encode(map[string]any{"DeviceAuth": f.identity, "DeviceID": "DEVICE1"})
		return
	}
	if r.URL.Path != "/api/" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if f.apiDisabled {
		w.WriteHeader(http.StatusLocked)
		return
	}
	var body map[string]string
	json.NewDecoder(r.Body).Decode(&body)
	switch body["cmd"] {
	case "login":
		f.logins++
		switch {
		case !f.auth:
			json.NewEncoder(w).Encode(map[string]any{"status": true})
		case body["username"] == "api" && body["password"] == "pw":
			json.NewEncoder(w).Encode(map[string]any{"status": true, "token": f.issue()})
		default:
			json.NewEncoder(w).Encode(map[string]any{"status": false, "err": "login incorrect"})
		}
	case "status":
		f.statuses++
		if f.statusReply != 0 {
			w.WriteHeader(f.statusReply)
			return
		}
		reply := map[string]any{"status": true, "version": map[string]any{"api": "1.1.0", "xteve": "2.2.0.0200"}, "epg": map[string]any{"source": "XEPG"}, "streams": f.streams}
		if f.auth {
			if !f.tokens[body["token"]] {
				json.NewEncoder(w).Encode(map[string]any{"status": false, "err": "token rejected"})
				return
			}
			delete(f.tokens, body["token"])
			reply["token"] = f.issue()
		}
		for key, value := range f.rawStatus {
			if value == nil {
				delete(reply, key)
				continue
			}
			reply[key] = value
		}
		json.NewEncoder(w).Encode(reply)
	}
}

func newH(t *testing.T, srv *httptest.Server, secrets fakeSecrets, results *[]helpers.StepResult) *helpers.H {
	t.Helper()
	deps := systemtest.NewDeps(t)
	return helpers.New(helpers.Config{
		App: "xteve", Service: "xteve", BaseURL: srv.URL, URL: srv.URL,
		HTTP: srv.Client(), Secrets: secrets, Log: events.Discard(),
		Record: func(r helpers.StepResult) {
			if results != nil {
				*results = append(*results, r)
			}
		},
		Now: deps.Now, Sleep: deps.Sleep,
	})
}

func credentials() fakeSecrets { return fakeSecrets{"XTEVE_USERNAME": "api", "XTEVE_PASSWORD": "pw"} }

func TestVerifyAcceptsCredentialsWithOneUseTokensAndStoresNone(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	secrets := credentials()
	h := newH(t, srv, secrets, nil)
	for range 3 {
		if err := xteve.VerifyHomarr(t.Context(), h); err != nil {
			t.Fatal(err)
		}
	}
	if fake.logins != 3 || fake.statuses != 3 || len(secrets) != 2 {
		t.Fatalf("logins=%d statuses=%d secrets=%v", fake.logins, fake.statuses, secrets)
	}
}

func TestVerifyAcceptsExplicitUnauthenticatedMode(t *testing.T) {
	fake := newFake()
	fake.auth = false
	srv := httptest.NewServer(fake)
	defer srv.Close()
	if err := xteve.VerifyHomarr(t.Context(), newH(t, srv, fakeSecrets{"XTEVE_AUTH": "none"}, nil)); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAcceptsGenuineStatusWithOmittedZeroCounters(t *testing.T) {
	fake := newFake()
	fake.streams = map[string]any{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	if err := xteve.VerifyHomarr(t.Context(), newH(t, srv, credentials(), nil)); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsStatusThatIsNotAGenuineCount(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*fakeXteve)
	}{
		{"null streams", func(f *fakeXteve) { f.rawStatus = map[string]any{"streams": nil} }},
		{"null counter", func(f *fakeXteve) { f.streams = map[string]any{"all": nil} }},
		{"wrong type", func(f *fakeXteve) { f.streams = map[string]any{"all": "1"} }},
		{"fractional", func(f *fakeXteve) { f.streams = map[string]any{"all": 1.5} }},
		{"negative", func(f *fakeXteve) { f.streams = map[string]any{"all": -1} }},
		{"active above all", func(f *fakeXteve) { f.streams = map[string]any{"all": 1, "active": 2} }},
		{"missing api version", func(f *fakeXteve) { f.rawStatus = map[string]any{"version": map[string]any{"xteve": "2"}} }},
		{"missing epg source", func(f *fakeXteve) { f.rawStatus = map[string]any{"epg": map[string]any{}} }},
		{"status false", func(f *fakeXteve) { f.rawStatus = map[string]any{"status": false} }},
		{"error text", func(f *fakeXteve) { f.rawStatus = map[string]any{"err": "boom"} }},
		{"foreign service", func(f *fakeXteve) { f.identity = "other" }},
		{"server error", func(f *fakeXteve) { f.statusReply = http.StatusInternalServerError }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFake()
			tc.edit(fake)
			srv := httptest.NewServer(fake)
			defer srv.Close()
			err := xteve.VerifyHomarr(t.Context(), newH(t, srv, credentials(), nil))
			if err == nil {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestVerifyStopsAsManualWithoutFallingBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func(*fakeXteve)
		secrets fakeSecrets
	}{
		{"partial pair", func(*fakeXteve) {}, fakeSecrets{"XTEVE_USERNAME": "api"}},
		{"missing credentials", func(*fakeXteve) {}, fakeSecrets{}},
		{"rejected credentials", func(*fakeXteve) {}, fakeSecrets{"XTEVE_USERNAME": "api", "XTEVE_PASSWORD": "wrong"}},
		{"unauthenticated mode against auth", func(*fakeXteve) {}, fakeSecrets{"XTEVE_AUTH": "none"}},
		{"credentials against disabled auth", func(f *fakeXteve) { f.auth = false }, credentials()},
		{"api disabled", func(f *fakeXteve) { f.apiDisabled = true }, credentials()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFake()
			tc.edit(fake)
			srv := httptest.NewServer(fake)
			defer srv.Close()
			err := xteve.VerifyHomarr(t.Context(), newH(t, srv, tc.secrets, nil))
			if !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("err = %v, want Manual", err)
			}
			if fake.statuses != 0 {
				t.Fatal("status was requested after the credentials were refused")
			}
		})
	}
}

func TestVerifyRequiresTheInternalHTTPAddress(t *testing.T) {
	fake := newFake()
	srv := httptest.NewTLSServer(fake)
	defer srv.Close()
	err := xteve.VerifyHomarr(t.Context(), newH(t, srv, credentials(), nil))
	if !errors.Is(err, helpers.ErrManual) || fake.logins != 0 {
		t.Fatalf("err = %v logins = %d", err, fake.logins)
	}
}

func TestSetupStopsAsManualAtOnceAndRerunSkipsWhenReady(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	var results []helpers.StepResult
	err := (xteve.App{}).Setup(context.Background(), newH(t, srv, fakeSecrets{}, &results))
	if !errors.Is(err, helpers.ErrManual) || fake.logins != 0 {
		t.Fatalf("err = %v logins = %d", err, fake.logins)
	}
	if len(results) == 0 || !strings.Contains(results[len(results)-1].Hint, "self-host setup xteve") {
		t.Fatalf("hint missing: %+v", results)
	}
	results = nil
	h := newH(t, srv, credentials(), &results)
	if err := (xteve.App{}).Setup(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	results = nil
	if err := (xteve.App{}).Setup(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Status != stepstatus.StepSkip {
			t.Fatalf("rerun step = %q, want skip", result.Status)
		}
	}
	if err := (xteve.App{}).Check(context.Background(), h); err != nil {
		t.Fatal(err)
	}
}

func TestComposeKeepsAllDeclaredPersistenceAndIsHidden(t *testing.T) {
	compose := (xteve.App{}).Compose(app.RenderContext{Port: 34400})
	for _, want := range []string{"./config:/home/xteve/conf", "./guide2go:/home/xteve/guide2go/conf", "/tmp/xteve", "${BIND_IP}:34400:34400"} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose lacks %q", want)
		}
	}
	if strings.Contains(compose, "privileged") || strings.Contains(compose, "docker.sock") || strings.Contains(compose, "network_mode") {
		t.Error("compose grants host access")
	}
	for _, file := range (xteve.App{}).Files(app.RenderContext{}, nil) {
		if file.Content != "" {
			t.Errorf("%s would overwrite existing configuration", file.Path)
		}
	}
	if catalog.Unavailable("xteve") == "" {
		t.Error("xTeVe must stay hidden until an image is approved")
	}
}
