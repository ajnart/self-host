package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/helpers"
)

const sonarrWitnessTestImage = "example/sonarr@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type sonarrWitnessTransport struct {
	mu        sync.Mutex
	t         *testing.T
	address   string
	workEnd   time.Time
	mode      string
	resource  *sonarrFilterResource
	posts     int
	requests  []string
	blockPath string
	entered   chan struct{}
	release   chan struct{}
}

func (s *sonarrWitnessTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	s.mu.Lock()
	if request.URL.Scheme != "http" || "http://"+request.URL.Host != s.address || request.Header.Get("X-Api-Key") != syntheticKey || request.URL.RawQuery != "" {
		s.t.Error("witness escaped the authenticated owned origin")
	}
	end, bounded := request.Context().Deadline()
	if !bounded || end.After(s.workEnd) || time.Until(end) > 30*time.Second {
		s.t.Error("witness request renewed its work budget")
	}
	s.requests = append(s.requests, request.Method+" "+request.URL.Path)
	if request.URL.Path == s.blockPath {
		entered, release := s.entered, s.release
		s.blockPath = ""
		s.mu.Unlock()
		close(entered)
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-release:
		}
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	status, body := 200, ""
	if request.URL.Path == "/api/v3/system/status" && request.Method == "GET" {
		body = `{"appName":"Sonarr","version":"4.0.0.1"}`
		if s.mode == "identity" {
			body = `{"appName":"Other","version":"4.0.0.1"}`
		}
		if s.mode == "version" {
			body = `{"appName":"Sonarr","version":"5.0.0.1"}`
		}
	} else if request.URL.Path == sonarrFilterPath && request.Method == "POST" {
		s.posts++
		var fields map[string]json.RawMessage
		if json.NewDecoder(request.Body).Decode(&fields) != nil || len(fields) != 3 || fields["id"] != nil {
			s.t.Error("witness POST schema escaped its fixed body")
		}
		data, _ := json.Marshal(fields)
		var content sonarrFilterContent
		if json.Unmarshal(data, &content) != nil || content.Type != "series" || content.Filters == nil || len(content.Filters) != 0 || !strings.HasPrefix(content.Label, "selfhost-witness-") || len(content.Label) != len("selfhost-witness-")+64 {
			s.t.Error("witness POST was not one opaque inert resource")
		}
		s.resource = &sonarrFilterResource{ID: 17, sonarrFilterContent: content}
		status = 201
		body = s.body()
		if s.mode == "ambiguous" {
			return nil, errors.New("synthetic-private-api-key private-witness-body /private/fixture")
		}
		if s.mode == "post-bad-body" {
			body = "private-witness-body"
		}
		if s.mode == "post-bad-id" {
			body = strings.Replace(body, `"id":17`, `"id":0`, 1)
		}
	} else if request.URL.Path == sonarrFilterPath && request.Method == "GET" {
		body = "[]"
		if s.resource != nil {
			body = "[" + s.body() + "]"
			if s.mode == "extra-object" {
				body = "[" + s.body() + "," + s.body() + "]"
			}
		} else if s.mode == "collision" {
			body = `[{"id":99,"type":"series","label":"selfhost-witness-existing","filters":[]}]`
		} else if s.mode == "null-collection" {
			body = "null"
		}
	} else if strings.HasPrefix(request.URL.Path, sonarrFilterPath+"/") && request.Method == "GET" {
		if s.mode == "reset" || s.resource == nil || request.URL.Path != sonarrFilterPath+"/17" {
			status = 404
		} else {
			body = s.body()
		}
	} else {
		s.t.Error("witness used an unapproved endpoint or method")
		status = 400
	}
	if s.mode == "auth" {
		status = 401
	}
	response := &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}
	if s.mode == "redirect" {
		response.StatusCode = 302
		response.Header.Set("Location", "https://example.invalid/private-witness-body")
	}
	return response, nil
}

func (s *sonarrWitnessTransport) body() string {
	resource := *s.resource
	switch s.mode {
	case "wrong-id":
		resource.ID++
	case "wrong-label":
		resource.Label = "different"
	case "wrong-type":
		resource.Type = "other"
	case "null-filter":
		resource.Filters = nil
	case "changed-filter":
		resource.Filters = []json.RawMessage{json.RawMessage(`{}`)}
	}
	data, _ := json.Marshal(resource)
	body := string(data)
	switch s.mode {
	case "missing-filter":
		body = strings.Replace(body, `,"filters":[]`, "", 1)
	case "extra-field":
		body = strings.TrimSuffix(body, "}") + `,"extra":"private-witness-body"}`
	case "bad-body":
		body = "private-witness-body"
	case "trailing-body":
		body += `{}`
	case "property-order":
		body = `{"filters":[],"label":` + strconv.Quote(resource.Label) + `,"type":"series","id":17}`
	}
	return body
}

func newSonarrWitnessPair(t *testing.T, order PairOrder, enabled bool) (*Pair, *fakeDocker, *sonarrWitnessTransport) {
	t.Helper()
	f := newFakeDocker(t)
	w := &sonarrWitnessTransport{t: t}
	f.deps.HTTP = &http.Client{Transport: w}
	consumer := pairFixtureApp{id: "homarr", consumer: true, target: "sonarr"}
	target := pairFixtureApp{id: "sonarr"}
	p, err := startPair(t.Context(), PairOptions{Consumer: "homarr", Target: "sonarr", Order: order, Timeout: time.Minute, RootParent: t.TempDir(), Images: map[string]string{"sonarr": sonarrWitnessTestImage}, SonarrFilterWitness: enabled}, f.deps, catalog.New([]app.App{consumer, target}, nil))
	if err != nil {
		t.Fatal("fake witness pair did not start")
	}
	view, _ := p.App("sonarr")
	w.address, w.workEnd = view.URL(), p.owner.workDeadline
	t.Cleanup(func() {
		f.mu.Lock()
		f.cleanupFailure = false
		f.mu.Unlock()
		if p.Close(context.Background()) != nil {
			t.Error("fake witness owner cleanup failed")
		}
	})
	return p, f, w
}

func TestSonarrWitnessBothOrdersRetainOneObjectAcrossTwoProductionCycles(t *testing.T) {
	var previous string
	for _, order := range []PairOrder{ConsumerFirst, TargetFirst} {
		t.Run(string(order), func(t *testing.T) {
			p, _, w := newSonarrWitnessPair(t, order, true)
			baseline, err := p.CreateSonarrFilterWitness(t.Context())
			if err != nil || baseline.ObjectID != 17 || baseline.Kind != sonarrFilterWitnessKind || len(baseline.ContentSHA256) != 64 || baseline.ContentSHA256 == previous {
				t.Fatal("unique witness baseline did not verify")
			}
			previous = baseline.ContentSHA256
			public, _ := json.Marshal(baseline)
			if strings.Contains(string(public), p.owner.pair.sonarrFilterWitness.label) || strings.Contains(string(public), syntheticKey) || strings.Contains(string(public), "label") {
				t.Fatal("witness receipt exposed private content")
			}
			for cycle := 0; cycle < 3; cycle++ {
				if cycle > 0 {
					removed, err := p.Remove(t.Context(), "sonarr")
					if err != nil || removed.Outcome != PairPrerequisite {
						t.Fatal("production non-purge removal failed")
					}
					installed, err := p.Install(t.Context(), "sonarr")
					if err != nil || installed.Outcome != PairComplete {
						t.Fatal("production reinstall failed")
					}
				}
				w.mode = "property-order"
				readback, err := p.ReadSonarrFilterWitness(t.Context())
				if err != nil || readback != baseline {
					t.Fatal("original witness was not retained exactly")
				}
			}
			if w.posts != 1 {
				t.Fatal("lifecycle repeated the seed write")
			}
		})
	}
}

func TestSonarrWitnessCreationFailsClosedWithoutAdoptionOrRetry(t *testing.T) {
	for _, mode := range []string{"collision", "null-collection", "identity", "version", "auth", "redirect", "ambiguous", "post-bad-body", "post-bad-id", "wrong-id", "extra-object"} {
		t.Run(mode, func(t *testing.T) {
			p, _, w := newSonarrWitnessPair(t, ConsumerFirst, true)
			w.mode = mode
			_, err := p.CreateSonarrFilterWitness(t.Context())
			assertSonarrWitnessFailure(t, p, err)
			if !p.owner.closed || !p.owner.pair.failed {
				t.Fatal("uncertain creation left an active owner")
			}
			if (mode == "collision" || mode == "null-collection" || mode == "identity" || mode == "version" || mode == "auth" || mode == "redirect") && w.posts != 0 {
				t.Fatal("unsupported or dirty fixture received a write")
			}
			posts := w.posts
			if _, err := p.CreateSonarrFilterWitness(t.Context()); err == nil || w.posts != posts {
				t.Fatal("failed creation was retried or adopted")
			}
		})
	}
}

func assertSonarrWitnessFailure(t *testing.T, p *Pair, err error) {
	t.Helper()
	var operation *OperationError
	if err == nil || !errors.As(err, &operation) || strings.Contains(err.Error(), syntheticKey) || strings.Contains(err.Error(), "private-witness-body") || strings.Contains(err.Error(), p.RecoveryPath()) {
		t.Fatal("witness failure was missing, untyped or unsafe")
	}
	label := p.owner.pair.sonarrFilterWitness.label
	if label != "" && strings.Contains(err.Error(), label) {
		t.Fatal("witness failure exposed its private nonce")
	}
}

func TestSonarrWitnessReadNeverReseedsMissingOrChangedData(t *testing.T) {
	for _, mode := range []string{"reset", "wrong-id", "wrong-label", "wrong-type", "null-filter", "missing-filter", "changed-filter", "extra-field", "bad-body", "trailing-body", "hash", "image"} {
		t.Run(mode, func(t *testing.T) {
			p, f, w := newSonarrWitnessPair(t, TargetFirst, true)
			if _, err := p.CreateSonarrFilterWitness(t.Context()); err != nil {
				t.Fatal("baseline did not verify")
			}
			w.mode = mode
			if mode == "hash" {
				p.owner.pair.sonarrFilterWitness.receipt.ContentSHA256 = strings.Repeat("0", 64)
			}
			if mode == "image" {
				for key, container := range f.containers {
					if container.Service == "sonarr" {
						container.Image = "sha256:" + strings.Repeat("b", 64)
						f.containers[key] = container
					}
				}
			}
			_, err := p.ReadSonarrFilterWitness(t.Context())
			assertSonarrWitnessFailure(t, p, err)
			if w.posts != 1 {
				t.Fatal("read repaired or reseeded the witness")
			}
		})
	}
}

func TestSonarrWitnessStateAndAdmissionPrecedeAppWrites(t *testing.T) {
	for _, mode := range []string{"disabled", "before-create", "repeat-create", "expired", "canceled", "absent", "caps", "origin", "manual", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			p, f, w := newSonarrWitnessPair(t, ConsumerFirst, mode != "disabled")
			if mode == "repeat-create" {
				if _, err := p.CreateSonarrFilterWitness(t.Context()); err != nil {
					t.Fatal("baseline did not verify")
				}
			}
			ctx := t.Context()
			if mode == "expired" {
				p.owner.workDeadline = time.Now().Add(-time.Second)
			}
			if mode == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if mode == "absent" {
				if _, err := p.Remove(t.Context(), "sonarr"); err != nil {
					t.Fatal("fake target removal failed")
				}
			}
			if mode == "caps" {
				for key, container := range f.containers {
					container.Memory = 0
					f.containers[key] = container
				}
			}
			if mode == "origin" {
				p.owner.pair.ports["sonarr"] = 0
			}
			if mode == "manual" || mode == "incomplete" {
				consumer := pairFixtureApp{id: "homarr", consumer: true, target: "sonarr"}
				consumer.setup = func(ctx context.Context, h *helpers.H) error {
					return h.Step(ctx, "witness-prerequisite", "fake prerequisite", helpers.StepSpec{Do: func(context.Context) error {
						if mode == "manual" {
							return helpers.ErrManual
						}
						return errors.New("private-witness-body")
					}})
				}
				p.owner.service.Catalog = catalog.New([]app.App{consumer, pairFixtureApp{id: "sonarr"}}, nil)
				if _, err := p.Setup(t.Context(), "homarr"); err != nil {
					t.Fatal("fake incomplete/manual Setup did not return inspection")
				}
			}
			requests, posts := len(w.requests), w.posts
			var err error
			if mode == "before-create" {
				_, err = p.ReadSonarrFilterWitness(ctx)
			} else {
				_, err = p.CreateSonarrFilterWitness(ctx)
			}
			assertSonarrWitnessFailure(t, p, err)
			if len(w.requests) != requests || w.posts != posts {
				t.Fatal("invalid witness state reached the App")
			}
		})
	}
	for _, options := range []PairOptions{
		{Consumer: "sonarr", Target: "homarr", Images: map[string]string{"sonarr": sonarrWitnessTestImage}},
		{Consumer: "homarr", Target: "sonarr"},
		{Consumer: "homarr", Target: "sonarr", Images: map[string]string{"sonarr": "example/sonarr:latest"}},
	} {
		f := newFakeDocker(t)
		options.Order, options.Timeout, options.RootParent, options.SonarrFilterWitness = ConsumerFirst, time.Minute, t.TempDir(), true
		p, err := startPair(t.Context(), options, f.deps, catalog.New([]app.App{pairFixtureApp{id: "homarr", consumer: true, target: "sonarr"}, pairFixtureApp{id: "sonarr"}}, nil))
		if err == nil || p != nil || f.creations != 0 {
			t.Fatal("witness admission allowed an unsupported scope/image")
		}
	}
}

func TestSonarrWitnessCancellationPreservesPrimaryAndIndependentCleanup(t *testing.T) {
	for _, cleanupFailed := range []bool{false, true} {
		t.Run(strconv.FormatBool(cleanupFailed), func(t *testing.T) {
			p, f, w := newSonarrWitnessPair(t, ConsumerFirst, true)
			w.blockPath, w.entered, w.release = "/api/v3/system/status", make(chan struct{}), make(chan struct{})
			f.mu.Lock()
			f.cleanupFailure = cleanupFailed
			f.mu.Unlock()
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { _, err := p.CreateSonarrFilterWitness(ctx); done <- err }()
			<-w.entered
			cancel()
			err := <-done
			assertSonarrWitnessFailure(t, p, err)
			var operation *OperationError
			if !errors.Is(err, context.Canceled) || !errors.As(err, &operation) || (operation.CleanupError() != nil) != cleanupFailed || w.posts != 0 {
				t.Fatal("witness cancellation lost primary/secondary failure")
			}
		})
	}
}

func TestSonarrWitnessConcurrentCreatesRemainOnceOnly(t *testing.T) {
	p, _, w := newSonarrWitnessPair(t, ConsumerFirst, true)
	w.blockPath, w.entered, w.release = "/api/v3/system/status", make(chan struct{}), make(chan struct{})
	done := make(chan error, 2)
	go func() { _, err := p.CreateSonarrFilterWitness(t.Context()); done <- err }()
	<-w.entered
	go func() { _, err := p.CreateSonarrFilterWitness(t.Context()); done <- err }()
	close(w.release)
	succeeded := 0
	for range 2 {
		if <-done == nil {
			succeeded++
		}
	}
	if succeeded != 1 || w.posts != 1 {
		t.Fatal("concurrent creates bypassed owner serialization")
	}
}

func TestSonarrWitnessConcurrentReadRemoveCloseUsesOneOwner(t *testing.T) {
	p, f, w := newSonarrWitnessPair(t, ConsumerFirst, true)
	if _, err := p.CreateSonarrFilterWitness(t.Context()); err != nil {
		t.Fatal("baseline did not verify")
	}
	w.blockPath, w.entered, w.release = sonarrFilterPath+"/17", make(chan struct{}), make(chan struct{})
	done := make(chan error, 3)
	go func() { _, err := p.ReadSonarrFilterWitness(t.Context()); done <- err }()
	<-w.entered
	go func() { _, err := p.Remove(t.Context(), "sonarr"); done <- err }()
	go func() { done <- p.Close(t.Context()) }()
	f.mu.Lock()
	remaining := len(f.containers)
	f.mu.Unlock()
	if remaining != 2 {
		t.Fatal("lifecycle escaped the held witness owner lock")
	}
	close(w.release)
	for range 3 {
		<-done
	}
	if p.Close(t.Context()) != nil || w.posts != 1 || len(f.containers) != 0 {
		t.Fatal("concurrent witness lifecycle did not clean one owner")
	}
}

func TestSonarrWitnessDecoderRejectsUnsupportedBodies(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{}`, `{"id":1,"type":"series","label":"x","filters":null}`, `{"id":2147483648,"type":"series","label":"x","filters":[]}`, `{"id":1,"type":"series","label":"x","filters":[]} {}`, `{"id":1,"type":"series","label":"x","filters":[],"other":true}`, `{"id":1,"id":1,"type":"series","label":"x","filters":[]}`} {
		if _, err := decodeSonarrFilter([]byte(body)); err == nil || strings.Contains(fmt.Sprint(err), body) {
			t.Fatal("unsupported resource body was accepted or exposed")
		}
	}
}

func TestSonarrWitnessHTTPDeadlineDoesNotRenewWork(t *testing.T) {
	p, _, w := newSonarrWitnessPair(t, ConsumerFirst, true)
	p.owner.workDeadline = time.Now().Add(25 * time.Millisecond)
	w.workEnd = p.owner.workDeadline
	w.blockPath, w.entered, w.release = "/api/v3/system/status", make(chan struct{}), make(chan struct{})
	_, err := p.CreateSonarrFilterWitness(t.Context())
	assertSonarrWitnessFailure(t, p, err)
	if !errors.Is(err, context.DeadlineExceeded) || w.posts != 0 || !p.owner.closed {
		t.Fatal("HTTP work timeout renewed the deadline or lost independent cleanup")
	}
}
