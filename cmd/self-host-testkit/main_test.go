package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ajnart/self-host/testkit"
)

type fakeFixture struct {
	err             error
	closed          int
	closeContextErr error
}

func (f *fakeFixture) URL() string { return "http://127.0.0.1:12345" }
func (f *fakeFixture) GetAPIKey(context.Context, string) (string, error) {
	return "private-test-key", f.err
}
func (f *fakeFixture) Credentials(context.Context) (testkit.Credentials, error) {
	return testkit.Credentials{Username: "admin", Password: "private-test-password", APIKey: "private-test-key"}, f.err
}
func (f *fakeFixture) Info(context.Context) (testkit.Info, error) {
	return testkit.Info{ID: "sonarr", URL: f.URL(), Image: "peer:latest", ImageID: "sha256:test"}, f.err
}
func (f *fakeFixture) Close(ctx context.Context) error {
	f.closed++
	f.closeContextErr = ctx.Err()
	return f.err
}

func ownedManifest(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "manifest.jsonl")
}

func TestBridgeTypedResponsesAndSanitizedErrors(t *testing.T) {
	f := &fakeFixture{}
	b := &bridge{app: f}
	for _, method := range []string{"getApiKey", "credentials", "info"} {
		req := request{Version: 1, ID: 1, Method: method, Params: json.RawMessage(`{}`)}
		res := b.handle(t.Context(), req)
		if res.Error != nil || res.Result == nil {
			t.Fatalf("%s did not return its result", method)
		}
		f.err = errors.New("provider leaked private-test-password /private/recovery")
		res = b.handle(t.Context(), req)
		data, err := json.Marshal(res)
		if err != nil || res.Error == nil || strings.Contains(string(data), "private-test-password") || strings.Contains(string(data), "/private/recovery") {
			t.Fatalf("%s did not sanitize its error", method)
		}
		f.err = nil
	}
}

func TestBridgeRejectsInvalidStateAndMethods(t *testing.T) {
	b := &bridge{}
	for _, method := range []string{"getApiKey", "credentials", "info", "shell", "install"} {
		res := b.handle(t.Context(), request{Version: 1, ID: 1, Method: method})
		if res.Error == nil || res.Result != nil {
			t.Fatalf("unexpected acceptance of %s", method)
		}
	}
}

func TestBridgeStartsOnceWithPrivateParent(t *testing.T) {
	manifest := ownedManifest(t)
	b := &bridge{manifest: manifest, owner: strings.Repeat("a", 32)}
	b.start = func(_ context.Context, options testkit.Options) (fixture, error) {
		if options.App != "sonarr" || options.Image != "peer:latest" || options.Timeout != time.Second || privateDirectory(options.RootParent) != nil {
			t.Error("incorrect fixture options")
		}
		return &fakeFixture{}, nil
	}
	req := request{Version: 1, ID: 1, Method: "spinup", Params: json.RawMessage(`{"app":"sonarr","timeoutMs":1000,"image":"peer:latest"}`)}
	if b.handle(t.Context(), req).Error != nil {
		t.Fatal("startup failed")
	}
	if b.handle(t.Context(), req).Error == nil {
		t.Fatal("a second fixture was accepted")
	}
	if err := cleanupManifest(t.Context(), manifest, func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeKeepsFixtureContextAliveUntilClose(t *testing.T) {
	manifest := ownedManifest(t)
	f := &fakeFixture{}
	var lifetime context.Context
	b := &bridge{manifest: manifest, owner: strings.Repeat("a", 32)}
	b.start = func(ctx context.Context, options testkit.Options) (fixture, error) {
		lifetime = ctx
		if options.Timeout != time.Second {
			t.Error("fixture lost its total time budget")
		}
		return f, nil
	}
	t.Cleanup(func() {
		b.handle(t.Context(), request{Version: 1, ID: 4, Method: "close"})
	})
	res := b.handle(t.Context(), request{Version: 1, ID: 1, Method: "spinup", Params: json.RawMessage(`{"app":"sonarr","timeoutMs":1000}`)})
	if res.Error != nil || lifetime == nil || lifetime.Err() != nil {
		t.Fatal("successful startup canceled the fixture lifetime")
	}
	if b.handle(t.Context(), request{Version: 1, ID: 2, Method: "getApiKey"}).Error != nil || lifetime.Err() != nil {
		t.Fatal("fixture was unavailable after startup returned")
	}
	if b.handle(t.Context(), request{Version: 1, ID: 3, Method: "close"}).Error != nil || !errors.Is(lifetime.Err(), context.Canceled) || f.closed != 1 || f.closeContextErr != nil {
		t.Fatal("close did not release the lifetime with an independent cleanup context")
	}
	if err := cleanupManifest(t.Context(), manifest, func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeCancelsFixtureContextAfterFailedStartup(t *testing.T) {
	manifest := ownedManifest(t)
	var lifetime context.Context
	b := &bridge{manifest: manifest, owner: strings.Repeat("a", 32)}
	b.start = func(ctx context.Context, _ testkit.Options) (fixture, error) {
		lifetime = ctx
		return nil, errors.New("startup failed")
	}
	res := b.handle(t.Context(), request{Version: 1, ID: 1, Method: "spinup", Params: json.RawMessage(`{"app":"sonarr"}`)})
	if res.Error == nil || lifetime == nil || !errors.Is(lifetime.Err(), context.Canceled) {
		t.Fatal("failed startup retained its fixture context")
	}
	if err := cleanupManifest(t.Context(), manifest, func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolRejectsVersionDuplicateAndOversizedMessages(t *testing.T) {
	for _, input := range []string{
		`{"version":2,"id":1,"method":"info"}`,
		`{"version":1,"id":0,"method":"info"}`,
		`{"version":1,"id":1,"method":"info"}` + "\n" + `{"version":1,"id":1,"method":"info"}`,
		strings.Repeat("x", maxMessage+1),
	} {
		t.Run(fmt.Sprint(len(input)), func(t *testing.T) {
			reader, writer := io.Pipe()
			outputReader, outputWriter := io.Pipe()
			b := &bridge{}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- b.serve(ctx, reader, outputWriter)
				outputWriter.Close()
			}()
			go func() {
				_, _ = io.WriteString(writer, input+"\n")
			}()
			data, err := io.ReadAll(outputReader)
			reader.Close()
			writer.Close()
			if err != nil || <-done == nil {
				t.Fatal("invalid protocol was not rejected")
			}
			if len(input) <= maxMessage && !strings.Contains(string(data), `"code":"PROTOCOL"`) {
				t.Fatal("missing sanitized protocol failure")
			}
		})
	}
}

func TestBridgeEOFCleansWithIndependentContext(t *testing.T) {
	reader, writer := io.Pipe()
	f := &fakeFixture{}
	lifetime, cancel := context.WithCancel(t.Context())
	defer cancel()
	b := &bridge{app: f, appCancel: cancel}
	done := make(chan error, 1)
	go func() { done <- b.serve(t.Context(), reader, io.Discard) }()
	writer.Close()
	if err := <-done; err != nil || f.closed != 1 || f.closeContextErr != nil || !errors.Is(lifetime.Err(), context.Canceled) {
		t.Fatal("EOF did not close the fixture")
	}
}

func TestStartupFailureAndOversizedResultsNeverExposeProviderData(t *testing.T) {
	manifest := ownedManifest(t)
	b := &bridge{manifest: manifest, owner: strings.Repeat("a", 32), start: func(context.Context, testkit.Options) (fixture, error) {
		return nil, errors.New("private-test-password /private/recovery")
	}}
	res := b.handle(t.Context(), request{Version: 1, ID: 1, Method: "spinup", Params: json.RawMessage(`{"app":"sonarr"}`)})
	var output strings.Builder
	if err := writeResponse(&output, res); err != nil || res.Error == nil || strings.Contains(output.String(), "private-test-password") || strings.Contains(output.String(), "/private/recovery") {
		t.Fatal("startup error was not sanitized")
	}
	output.Reset()
	if err := writeResponse(&output, response{Version: 1, ID: 2, Result: strings.Repeat("s", maxMessage+1)}); err != nil || output.Len() > maxMessage || !strings.Contains(output.String(), `"code":"PROTOCOL"`) {
		t.Fatal("oversized response was not bounded")
	}
	if err := cleanupManifest(t.Context(), manifest, func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

type fakePairFixture struct {
	fakeFixture
	view           fakeFixture
	selected       string
	actionErr      error
	witnessCreates int
	witnessReads   int
}

func (f *fakePairFixture) View(id string) (fixtureView, error) {
	if id != "homarr" && id != "sonarr" {
		return nil, errors.New("private-test-password")
	}
	f.selected = id
	return &f.view, nil
}
func (f *fakePairFixture) Initial() testkit.PairInspection {
	return testkit.PairInspection{Outcome: testkit.PairPrerequisite, Reason: "absent"}
}
func (f *fakePairFixture) Inspect(context.Context) (testkit.PairInspection, error) {
	return testkit.PairInspection{Outcome: testkit.PairComplete, Reason: "verified"}, f.actionErr
}
func (f *fakePairFixture) Setup(ctx context.Context, id string) (testkit.PairInspection, error) {
	return f.Inspect(ctx)
}
func (f *fakePairFixture) Remove(ctx context.Context, id string) (testkit.PairInspection, error) {
	return f.Inspect(ctx)
}
func (f *fakePairFixture) Install(ctx context.Context, id string) (testkit.PairInspection, error) {
	return f.Inspect(ctx)
}

func (f *fakePairFixture) CreateSonarrFilterWitness(context.Context) (testkit.SonarrFilterWitness, error) {
	f.witnessCreates++
	return testkit.SonarrFilterWitness{Kind: "sonarr-custom-filter-v1", ObjectID: 17, ContentSHA256: strings.Repeat("a", 64)}, f.actionErr
}

func (f *fakePairFixture) ReadSonarrFilterWitness(context.Context) (testkit.SonarrFilterWitness, error) {
	f.witnessReads++
	return testkit.SonarrFilterWitness{Kind: "sonarr-custom-filter-v1", ObjectID: 17, ContentSHA256: strings.Repeat("a", 64)}, f.actionErr
}

func TestPairBridgeAdditiveScopeActionsAndCleanup(t *testing.T) {
	manifest := ownedManifest(t)
	f := &fakePairFixture{}
	var lifetime context.Context
	b := &bridge{manifest: manifest, owner: strings.Repeat("a", 32), startPair: func(ctx context.Context, options testkit.PairOptions) (pairFixture, error) {
		lifetime = ctx
		if options.Consumer != "homarr" || options.Target != "sonarr" || options.Order != testkit.TargetFirst || options.Timeout != time.Second || options.SonarrFilterWitness {
			t.Error("pair options changed")
		}
		return f, nil
	}}
	start := request{Version: 1, ID: 1, Method: "spinupPair", Params: json.RawMessage(`{"consumer":"homarr","target":"sonarr","order":"target-first","timeoutMs":1000}`)}
	if b.handle(t.Context(), start).Error != nil || lifetime.Err() != nil {
		t.Fatal("pair start failed or canceled")
	}
	if b.handle(t.Context(), request{Method: "spinup", Params: json.RawMessage(`{"app":"sonarr"}`)}).Error == nil {
		t.Fatal("mixed single/pair startup accepted")
	}
	for _, method := range []string{"getApiKey", "credentials", "info"} {
		res := b.handle(t.Context(), request{Method: method, Params: json.RawMessage(`{"app":"sonarr"}`)})
		if res.Error != nil || f.selected != "sonarr" {
			t.Fatal("selected App access failed")
		}
		if b.handle(t.Context(), request{Method: method, Params: json.RawMessage(`{"app":"outside"}`)}).Error == nil {
			t.Fatal("unselected App accepted")
		}
	}
	for _, method := range []string{"inspect", "setup", "remove", "install"} {
		if method != "inspect" {
			invalid := b.handle(t.Context(), request{Method: method, Params: json.RawMessage(`{"app":"outside"}`)})
			if invalid.Error == nil || invalid.Error.Code != "STATE" || f.closed != 0 {
				t.Fatal("invalid selection closed a healthy pair")
			}
		}
		if b.handle(t.Context(), request{Method: method, Params: json.RawMessage(`{"app":"sonarr"}`)}).Error != nil {
			t.Fatal("pair action failed")
		}
	}
	if b.handle(t.Context(), request{Method: "close"}).Error != nil || f.closed != 1 || f.closeContextErr != nil || lifetime.Err() != context.Canceled {
		t.Fatal("pair cleanup did not preserve independent context")
	}
	if err := cleanupManifest(t.Context(), manifest, func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestSonarrWitnessCapabilitiesAreStaticBeforeStartup(t *testing.T) {
	starts := 0
	b := &bridge{startPair: func(context.Context, testkit.PairOptions) (pairFixture, error) { starts++; return nil, nil }}
	res := b.handle(t.Context(), request{Version: 1, ID: 1, Method: "capabilities", Params: json.RawMessage(`{}`)})
	data, _ := json.Marshal(res)
	if res.Error != nil || string(data) != `{"version":1,"id":1,"result":{"sonarrFilterWitness":1}}` || b.started || starts != 0 || b.pair != nil {
		t.Fatal("capability negotiation performed startup or changed its schema")
	}
	for _, payload := range []string{`null`, `[]`, `{"app":"sonarr"}`, `{"url":"http://example.invalid"}`} {
		res := b.handle(t.Context(), request{Method: "capabilities", Params: json.RawMessage(payload)})
		if res.Error == nil || res.Error.Code != "PROTOCOL" || starts != 0 || b.started {
			t.Fatal("capabilities accepted nonempty parameters")
		}
	}
}

func TestSonarrWitnessBridgeScopeOptInEchoAndActionParameters(t *testing.T) {
	manifest := ownedManifest(t)
	f := &fakePairFixture{}
	starts := 0
	b := &bridge{manifest: manifest, owner: strings.Repeat("a", 32), startPair: func(_ context.Context, options testkit.PairOptions) (pairFixture, error) {
		starts++
		if !options.SonarrFilterWitness || options.Consumer != "homarr" || options.Target != "sonarr" {
			t.Error("witness permission lost its selected role")
		}
		return f, nil
	}}
	wrong := request{Method: "spinupPair", Params: json.RawMessage(`{"consumer":"sonarr","target":"homarr","order":"consumer-first","sonarrFilterWitness":true}`)}
	if b.handle(t.Context(), wrong).Error == nil || starts != 0 || b.started {
		t.Fatal("wrong witness scope reached startup")
	}
	start := request{Method: "spinupPair", Params: json.RawMessage(`{"consumer":"homarr","target":"sonarr","order":"consumer-first","sonarrFilterWitness":true}`)}
	res := b.handle(t.Context(), start)
	data, _ := json.Marshal(res)
	if res.Error != nil || !strings.Contains(string(data), `"sonarrFilterWitness":1`) || starts != 1 || f.witnessCreates != 0 {
		t.Fatal("opt-in startup seeded data or omitted its echo")
	}
	for _, method := range []string{"createSonarrFilterWitness", "readSonarrFilterWitness"} {
		for _, payload := range []string{`null`, `[]`, `{"app":"sonarr"}`, `{"id":17}`, `{"label":"private-witness-body"}`, `{"url":"https://example.invalid"}`, `{"body":{}}`} {
			res := b.handle(t.Context(), request{Method: method, Params: json.RawMessage(payload)})
			if res.Error == nil || res.Error.Code != "PROTOCOL" || f.witnessCreates != 0 || f.witnessReads != 0 {
				t.Fatal("witness bridge accepted arbitrary resource inputs")
			}
		}
	}
	for _, method := range []string{"createSonarrFilterWitness", "readSonarrFilterWitness"} {
		res := b.handle(t.Context(), request{Method: method, Params: json.RawMessage(`{}`)})
		data, _ := json.Marshal(res)
		if res.Error != nil || !strings.Contains(string(data), `"objectId":17`) || strings.Contains(string(data), "label") {
			t.Fatal("witness bridge returned an unsafe receipt")
		}
	}
	if f.witnessCreates != 1 || f.witnessReads != 1 {
		t.Fatal("witness dispatch did not use the Pair owner")
	}
	if b.handle(t.Context(), request{Method: "close"}).Error != nil {
		t.Fatal("witness bridge did not close")
	}
	if cleanupManifest(t.Context(), manifest, func(context.Context, string) error { return nil }) != nil {
		t.Fatal("fake manifest cleanup failed")
	}
	legacy := &bridge{pair: &fakePairFixture{}}
	if legacy.handle(t.Context(), request{Method: "createSonarrFilterWitness"}).Error.Code != "STATE" {
		t.Fatal("legacy Pair acquired witness permission")
	}
}

func TestSonarrWitnessBridgePreservesPrimaryAndCleanupFailures(t *testing.T) {
	for _, cause := range []error{errors.New("private-witness-body /private/fixture"), context.Canceled, context.DeadlineExceeded} {
		for _, method := range []string{"createSonarrFilterWitness", "readSonarrFilterWitness"} {
			f := &fakePairFixture{actionErr: cause}
			f.err = errors.New("private-witness-cleanup")
			b := &bridge{pair: f, sonarrFilterWitness: true}
			res := b.handle(t.Context(), request{Method: method})
			data, _ := json.Marshal(res)
			want := "OPERATION"
			if errors.Is(cause, context.Canceled) {
				want = "CANCELED"
			}
			if errors.Is(cause, context.DeadlineExceeded) {
				want = "TIMEOUT"
			}
			if res.Error == nil || res.Error.Code != want || !res.Error.CleanupFailed || f.closed != 1 || strings.Contains(string(data), "private-witness") || strings.Contains(string(data), "/private/") {
				t.Fatal("witness bridge lost or exposed a failure")
			}
		}
	}
}

func TestPairBridgeInfrastructureFailureClosesAndDisablesOwner(t *testing.T) {
	for _, cause := range []error{errors.New("private-test-password"), context.Canceled, context.DeadlineExceeded} {
		for _, cleanupFailed := range []bool{false, true} {
			f := &fakePairFixture{actionErr: cause}
			if cleanupFailed {
				f.err = errors.New("private-cleanup-password")
			}
			lifetime, cancel := context.WithCancel(t.Context())
			b := &bridge{pair: f, appCancel: cancel}
			res := b.handle(t.Context(), request{Method: "setup", Params: json.RawMessage(`{"app":"sonarr"}`)})
			data, _ := json.Marshal(res)
			want := "OPERATION"
			if errors.Is(cause, context.Canceled) {
				want = "CANCELED"
			}
			if errors.Is(cause, context.DeadlineExceeded) {
				want = "TIMEOUT"
			}
			if res.Error == nil || res.Error.Code != want || res.Error.CleanupFailed != cleanupFailed || f.closed != 1 || f.closeContextErr != nil || lifetime.Err() != context.Canceled || strings.Contains(string(data), "password") {
				t.Fatalf("unsafe failure response: %s", data)
			}
			if want == "OPERATION" && res.Error.Reason != "SETUP" {
				t.Fatal("typed operation reason missing")
			}
			blocked := b.handle(t.Context(), request{Method: "install", Params: json.RawMessage(`{"app":"sonarr"}`)})
			if blocked.Error == nil || blocked.Error.Code != "STATE" || f.closed != 1 {
				t.Fatal("failed bridge accepted another action")
			}
			f.err = nil
			if b.handle(t.Context(), request{Method: "close"}).Error != nil {
				t.Fatal("cleanup retry failed")
			}
		}
	}
}
