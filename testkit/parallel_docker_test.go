//go:build e2e

package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/arr"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/system"
)

type parallelSonarr struct {
	arr.App
	barrier *startupBarrier
}

func (a parallelSonarr) Setup(ctx context.Context, h *helpers.H) error {
	// Neither real Setup may proceed until BOTH independent containers exist.
	if err := a.barrier.wait(ctx); err != nil {
		return err
	}
	return a.App.Setup(ctx, h)
}

func TestDockerParallelSonarrStartup(t *testing.T) {
	if os.Getenv("SELF_HOST_TESTKIT_ACCEPTANCE") != "1" {
		t.Skip("explicit disposable-host acceptance only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	barrier := newStartupBarrier()
	type result struct {
		app *App
		err error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			env := environmentSnapshot()
			deps := system.Real("testkit")
			deps.Getenv = func(key string) string { return env[key] }
			deps.Runner = system.NewExecRunner(environmentList(env))
			selected := parallelSonarr{App: arr.Sonarr(), barrier: barrier}
			fixture, err := start(ctx, Options{App: "sonarr", Timeout: 8 * time.Minute}, deps, catalog.New([]app.App{selected}, nil))
			if err != nil {
				cancel()
			}
			results <- result{fixture, err}
		}()
	}
	var fixtures []*App
	for range 2 {
		r := <-results
		if r.app != nil {
			fixtures = append(fixtures, r.app)
			t.Cleanup(func() {
				if err := r.app.Close(context.Background()); err != nil {
					t.Error("owned cleanup failed; private recovery retained")
				}
			})
		}
		if r.err != nil {
			reason, phase := "unknown", "unknown"
			var startup *StartupError
			if errors.As(r.err, &startup) {
				reason, phase = startup.Reason(), startup.Phase()
			}
			t.Errorf("startup failed: reason=%s phase=%s cleanupIncomplete=%t", reason, phase, errors.Is(r.err, ErrCleanupIncomplete))
		}
	}
	if len(fixtures) != 2 {
		t.Fatal("both real fixtures did not finish")
	}
	if barrier.arrivals.Load() != 2 {
		t.Fatal("real container startup did not overlap")
	}
	if fixtures[0].URL() == fixtures[1].URL() || fixtures[0].marker.Name == fixtures[1].marker.Name {
		t.Fatal("fixture isolation failed")
	}
	for _, fixture := range fixtures {
		ids, err := resourceIDs(ctx, fixture.runner, fixture.marker.Name, "container")
		if err != nil || len(ids) != 1 {
			t.Fatal("acceptance exceeded one container per fixture")
		}
		if err := fixture.guard.verify(ctx); err != nil {
			t.Fatal("actual container caps or ownership failed")
		}
		credentials, err := fixture.Credentials(ctx)
		if err != nil || credentials.APIKey == "" {
			t.Fatal("fixture credentials unavailable")
		}
		for _, valid := range []bool{true, false} {
			key := credentials.APIKey
			if !valid {
				key = "invalid-parallel-test-key"
			}
			req, err := http.NewRequestWithContext(ctx, "GET", fixture.URL()+"/api/v3/system/status", nil)
			if err != nil {
				t.Fatal("request preparation failed")
			}
			req.Header.Set("X-Api-Key", key)
			client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Do(req)
			if err != nil {
				t.Fatal("Sonarr request failed")
			}
			if valid {
				var status struct {
					AppName string `json:"appName"`
					Version string `json:"version"`
				}
				err = json.NewDecoder(response.Body).Decode(&status)
				response.Body.Close()
				if err != nil || response.StatusCode != 200 || status.AppName != "Sonarr" || status.Version == "" {
					t.Fatal("authenticated Sonarr identity failed")
				}
			} else {
				response.Body.Close()
				if response.StatusCode != 401 {
					t.Fatal("Sonarr accepted invalid credentials")
				}
			}
		}
		t.Logf("parallel Sonarr image: %s", fixture.info.ImageID)
	}
	for _, fixture := range fixtures {
		if err := fixture.Close(context.Background()); err != nil {
			t.Fatal("owned cleanup failed; private recovery retained")
		}
		for _, kind := range []string{"container", "network"} {
			ids, err := resourceIDs(context.Background(), fixture.runner, fixture.marker.Name, kind)
			if err != nil || len(ids) != 0 {
				t.Fatal("owned Docker resources remain")
			}
		}
	}
	t.Log("two real Sonarr containers reached Setup before either Setup was allowed to continue; authentication, isolation, caps and owned cleanup passed")
}
