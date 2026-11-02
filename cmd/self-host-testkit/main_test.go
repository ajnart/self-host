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
