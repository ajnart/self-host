package testkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/system"
)

type pairFixtureApp struct {
	fixtureApp
	id          string
	consumer    bool
	extra       int
	linkFailure bool
	target      string
}

func (a pairFixtureApp) Info() app.Info {
	return app.Info{ID: a.id, Name: a.id, Service: a.id, Image: "example/app:latest", Port: 8199, ContainerPort: 80}
}
func (a pairFixtureApp) Compose(rc app.RenderContext) string {
	text := fmt.Sprintf("services:\n  %s:\n    image: example/app:latest\n    ports: ['${BIND_IP}:%d:80']\n    volumes: ['./config:/config']\n", a.id, rc.Port)
	for i := range a.extra {
		text += fmt.Sprintf("  %s-db%d: {image: 'example/db:1'}\n", a.id, i)
	}
	return text
}
func (a pairFixtureApp) Links() []app.Link {
	if !a.consumer {
		return nil
	}
	target := a.target
	if target == "" {
		target = "peer"
	}
	return []app.Link{app.NewLink("link-"+target, target, "pair fixture", app.LinkStep{Do: func(ctx context.Context, from, to *helpers.H) error {
		if a.linkFailure {
			return errors.New("synthetic-private-api-key")
		}
		return from.SetSecret(from.Key("owned_peer"), to.Secret(to.Key("api_key")))
	}}, &app.LinkStep{Do: func(ctx context.Context, from, to *helpers.H) error {
		return from.SetSecret(from.Key("owned_peer"), "")
	}})}
}

func pairFake(t *testing.T, order string, consumer, peer pairFixtureApp) (*Pair, *fakeDocker, error) {
	t.Helper()
	f := newFakeDocker(t)
	p, err := startPair(context.Background(), PairOptions{Consumer: "consumer", Target: "peer", Order: PairOrder(order), Timeout: time.Minute, RootParent: t.TempDir(), Images: map[string]string{"consumer": "example/consumer:selected", "peer": "example/peer:selected"}}, f.deps, catalog.New([]app.App{consumer, peer}, nil))
	if p != nil {
		t.Cleanup(func() {
			if err := p.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	return p, f, err
}

func TestPairProductionOrdersAndLifecycle(t *testing.T) {
	for _, order := range []string{"consumer-first", "target-first"} {
		t.Run(order, func(t *testing.T) {
			p, f, err := pairFake(t, order, pairFixtureApp{id: "consumer", consumer: true}, pairFixtureApp{id: "peer"})
			if err != nil {
				t.Fatal(err)
			}
			if p.Initial().Outcome != "prerequisite" || p.Initial().Reason != "absent" {
				t.Fatalf("initial=%+v", p.Initial())
			}
			observed, err := p.Inspect(t.Context())
			if err != nil || observed.Outcome != "complete" || len(f.containers) != 2 {
				t.Fatalf("inspection=%+v error=%v", observed, err)
			}
			consumer, _ := p.App("consumer")
			peer, _ := p.App("peer")
			if consumer.URL() == peer.URL() {
				t.Fatal("shared publication")
			}
			info, err := peer.Info(t.Context())
			if err != nil || info.Image != "example/peer:selected" {
				t.Fatal("per-App image was lost", err)
			}
			before, _ := consumer.Credentials(t.Context())
			if _, err := p.Setup(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				removed, err := p.Remove(t.Context(), "peer")
				if err != nil || removed.Outcome != "prerequisite" || len(f.containers) != 1 {
					t.Fatalf("remove=%+v error=%v", removed, err)
				}
				if _, err := peer.Info(t.Context()); err == nil {
					t.Fatal("stale removed Info")
				}
				if _, err := peer.GetAPIKey(t.Context(), ""); err == nil {
					t.Fatal("removed credentials exposed")
				}
				installed, err := p.Install(t.Context(), "peer")
				if err != nil || installed.Outcome != "complete" {
					t.Fatalf("install=%+v error=%v", installed, err)
				}
			}
			after, _ := consumer.Credentials(t.Context())
			if before.Password != after.Password {
				t.Fatal("shared login rotated")
			}
			if _, err := p.Remove(t.Context(), "outside"); err == nil {
				t.Fatal("unselected removal accepted")
			}
			for _, args := range [][]string{{"rm", "--stop", "--force", "-v", "peer"}, {"rm", "--stop", "--force", "outside"}, {"rm", "--force", "peer"}} {
				cmd := system.Cmd{Name: "docker", Args: append([]string{"compose", "--project-directory", p.owner.guard.p.Root, "-p", p.owner.marker.Name}, args...)}
				if err := p.owner.guard.validate(cmd); err == nil {
					t.Fatal("unsafe removal accepted")
				}
			}
			if err := p.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := p.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Inspect(t.Context()); err == nil {
				t.Fatal("closed pair inspection accepted")
			}
		})
	}
}

func TestPairAggregateAdmissionPrecedesCreation(t *testing.T) {
	_, f, err := pairFake(t, "consumer-first", pairFixtureApp{id: "consumer", consumer: true, extra: 2}, pairFixtureApp{id: "peer", extra: 2})
	if err == nil || f.creations != 0 || len(f.containers) != 0 {
		t.Fatal("aggregate pair was not refused before startup")
	}
}

func TestPairExplicitManualAndFailedOutcomes(t *testing.T) {
	for _, manual := range []bool{true, false} {
		peer := pairFixtureApp{id: "peer"}
		consumer := pairFixtureApp{id: "consumer", consumer: true, linkFailure: !manual}
		if manual {
			peer.setup = func(ctx context.Context, h *helpers.H) error {
				return h.Step(ctx, "prerequisite", "finish fixture", helpers.StepSpec{Do: func(context.Context) error { return helpers.ErrManual }, Hint: "fixture prerequisite"})
			}
		}
		p, _, err := pairFake(t, "consumer-first", consumer, peer)
		if err != nil {
			t.Fatal(err)
		}
		observed, err := p.Inspect(t.Context())
		want := PairFailed
		if manual {
			want = "manual"
		}
		if err != nil || observed.Outcome != want {
			t.Fatalf("outcome=%+v error=%v", observed, err)
		}
		if strings.Contains(fmt.Sprint(observed), syntheticKey) {
			t.Fatal("raw provider error exposed")
		}
		retried, err := p.Setup(t.Context(), "")
		if err != nil || retried.Outcome != want || p.owner.closed || p.owner.pair.failed {
			t.Fatal("expected Setup outcome closed the pair", err)
		}
	}
}

func TestPairObservedFailuresPrecedeManualAndMissingPeers(t *testing.T) {
	for _, order := range []string{"consumer-first", "target-first"} {
		for _, failingID := range []string{"consumer", "peer"} {
			for _, manual := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/manual=%t", order, failingID, manual), func(t *testing.T) {
					consumer := pairFixtureApp{id: "consumer", consumer: true}
					peer := pairFixtureApp{id: "peer"}
					for _, selected := range []*pairFixtureApp{&consumer, &peer} {
						selected.setup = func(ctx context.Context, h *helpers.H) error {
							return h.Step(ctx, "observation", "observe fixture", helpers.StepSpec{Do: func(context.Context) error {
								if h.App == failingID {
									return errors.New("synthetic failure")
								}
								if manual {
									return helpers.ErrManual
								}
								return nil
							}, Hint: "fixture prerequisite"})
						}
					}
					p, _, err := pairFake(t, order, consumer, peer)
					if err != nil {
						t.Fatal(err)
					}
					assertObserved := func(result PairInspection, required bool) {
						t.Helper()
						fails := 0
						manualObserved := false
						for _, item := range result.Apps {
							if item.Setup == "manual" {
								manualObserved = true
							}
							seen := map[string]bool{}
							for _, step := range item.Steps {
								if seen[step.ID] {
									t.Fatal("producer duplicated a step within an App")
								}
								seen[step.ID] = true
								if step.Status == "fail" {
									fails++
								}
							}
						}
						if required && fails == 0 {
							t.Fatal("producer dropped the observed failure")
						}
						if fails > 0 && (result.Outcome != PairFailed || result.Reason != "setup") {
							t.Fatal("observed failure lost summary precedence")
						}
						if fails == 0 && manualObserved && result.Outcome != PairManual {
							t.Fatal("ordinary Manual observation lost summary precedence")
						}
					}
					assertObserved(p.Initial(), false)
					for _, action := range []func() (PairInspection, error){
						func() (PairInspection, error) { return p.Inspect(t.Context()) },
						func() (PairInspection, error) { return p.Setup(t.Context(), "") },
						func() (PairInspection, error) {
							other := "consumer"
							if failingID == "consumer" {
								other = "peer"
							}
							return p.Remove(t.Context(), other)
						},
					} {
						observed, err := action()
						if err != nil {
							t.Fatal(err)
						}
						assertObserved(observed, true)
					}
					if p.owner.closed || p.owner.pair.failed {
						t.Fatal("ordinary failure observations disabled the Pair")
					}
				})
			}
		}
	}
}

func TestPairPartialStartupAndCancellationCleanOneOwner(t *testing.T) {
	for _, mode := range []string{"partial", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			f := newFakeDocker(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			consumer := pairFixtureApp{id: "consumer", consumer: true}
			peer := pairFixtureApp{id: "peer"}
			if mode == "partial" {
				f.partialFailure = true
			} else {
				peer.setup = func(context.Context, *helpers.H) error { cancel(); return ctx.Err() }
			}
			p, err := startPair(ctx, PairOptions{Consumer: "consumer", Target: "peer", Order: ConsumerFirst, Timeout: time.Minute, RootParent: t.TempDir()}, f.deps, catalog.New([]app.App{consumer, peer}, nil))
			if p != nil || err == nil || len(f.containers) != 0 || strings.Contains(err.Error(), syntheticKey) {
				t.Fatalf("partial pair retained or leaked: %v", err)
			}
			if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("primary cancellation lost")
			}
		})
	}
}

func TestPairActionsNeverRenewWorkDeadline(t *testing.T) {
	p, _, err := pairFake(t, "consumer-first", pairFixtureApp{id: "consumer", consumer: true}, pairFixtureApp{id: "peer"})
	if err != nil {
		t.Fatal(err)
	}
	p.owner.workDeadline = time.Now().Add(-time.Second)
	if _, err := p.Setup(t.Context(), ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired pair action accepted")
	}
	if err := p.Close(t.Context()); err != nil {
		t.Fatal("work deadline canceled reserved cleanup")
	}
}

func TestPairDirectCloseFailurePermanentlyDisablesOwner(t *testing.T) {
	p, f, err := pairFake(t, "consumer-first", pairFixtureApp{id: "consumer", consumer: true}, pairFixtureApp{id: "peer"})
	if err != nil {
		t.Fatal(err)
	}
	f.cleanupFailure = true
	t.Cleanup(func() { f.cleanupFailure = false })
	workDeadline, deadline := p.owner.workDeadline, p.owner.deadline
	started, release := make(chan bool, 1), make(chan struct{})
	var once, unblock sync.Once
	defer unblock.Do(func() { close(release) })
	f.runner.OnFunc("docker container rm", func(cmd system.Cmd) system.Result {
		once.Do(func() {
			started <- p.owner.pair.failed
			<-release
		})
		return f.run(cmd)
	})
	closed := make(chan error, 1)
	go func() { closed <- p.Close(t.Context()) }()
	select {
	case disabled := <-started:
		if !disabled {
			t.Error("Pair was not disabled before cleanup began")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fake cleanup did not reach its owned removal")
	}
	consumer, _ := p.App("consumer")
	queued := make(chan struct{}, 2)
	results := make(chan error, 2)
	for _, action := range []func() error{
		func() error { _, err := p.Inspect(t.Context()); return err },
		func() error { _, err := consumer.GetAPIKey(t.Context(), ""); return err },
	} {
		go func() { queued <- struct{}{}; results <- action() }()
	}
	for range 2 {
		<-queued
	}
	unblock.Do(func() { close(release) })
	if err := <-closed; err == nil || p.owner.closed || !p.owner.pair.failed {
		t.Fatal("direct failed close did not retain a disabled recoverable owner")
	}
	for range 2 {
		if err := <-results; err == nil {
			t.Fatal("a call queued during close resumed the owner")
		}
	}
	assertDisabled := func() {
		t.Helper()
		before := len(f.runner.Calls())
		storage, svc := p.owner.guard.p, p.owner.service
		p.owner.guard.p, p.owner.service = nil, nil
		defer func() { p.owner.guard.p, p.owner.service = storage, svc }()
		for _, id := range []string{"consumer", "peer"} {
			view, _ := p.App(id)
			if _, err := view.GetAPIKey(t.Context(), ""); err == nil {
				t.Fatal("closing owner exposed a key")
			}
			if _, err := view.Credentials(t.Context()); err == nil {
				t.Fatal("closing owner exposed credentials")
			}
			if _, err := view.Info(t.Context()); err == nil {
				t.Fatal("closing owner read storage for Info")
			}
		}
		if _, err := p.Inspect(t.Context()); err == nil {
			t.Fatal("closing Pair remained inspectable")
		}
		if _, err := p.Setup(t.Context(), ""); err == nil {
			t.Fatal("closing Pair accepted Setup")
		}
		if _, err := p.Remove(t.Context(), "peer"); err == nil {
			t.Fatal("closing Pair accepted Remove")
		}
		if _, err := p.Install(t.Context(), "peer"); err == nil {
			t.Fatal("closing Pair accepted Install")
		}
		if _, err := p.CreateSonarrFilterWitness(t.Context()); err == nil {
			t.Fatal("closing Pair accepted a witness create")
		}
		if _, err := p.ReadSonarrFilterWitness(t.Context()); err == nil || len(f.runner.Calls()) != before {
			t.Fatal("closing Pair reached its runner")
		}
	}
	assertDisabled()
	if err := p.Close(t.Context()); err == nil {
		t.Fatal("repeated failed cleanup was hidden")
	}
	assertDisabled()
	if _, err := readOwnership(p.RecoveryPath()); err != nil {
		t.Fatal("failed direct cleanup lost its recovery ownership")
	}
	f.cleanupFailure = false
	if err := p.Close(t.Context()); err != nil || len(f.containers) != 0 || !p.owner.closed {
		t.Fatal("direct cleanup retry could not finish")
	}
	assertDisabled()
	if !p.owner.workDeadline.Equal(workDeadline) || !p.owner.deadline.Equal(deadline) {
		t.Fatal("direct close renewed an owner clock")
	}
}

func TestPairSelectedCredentialsInfrastructureFailureClosesOwner(t *testing.T) {
	for _, method := range []string{"key", "credentials", "info-consumer", "info-peer"} {
		for _, mode := range []string{"deadline", "cancel", "cancel-during-read", "deadline-during-read", "storage", "presence-storage"} {
			if strings.HasPrefix(method, "info-") && mode != "deadline" && mode != "cancel" && mode != "presence-storage" {
				continue
			}
			for _, cleanupFailed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/cleanup-failed=%t", method, mode, cleanupFailed), func(t *testing.T) {
					p, f, err := pairFake(t, "consumer-first", pairFixtureApp{id: "consumer", consumer: true}, pairFixtureApp{id: "peer"})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { f.cleanupFailure = false })
					view, _ := p.App("peer")
					if strings.HasPrefix(method, "info-") {
						view, _ = p.App(strings.TrimPrefix(method, "info-"))
					}
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					root := p.RecoveryPath()
					owner := p.owner.service.Deps.CurrentUser
					wantReason := string(StartupReasonSetup)
					switch mode {
					case "deadline":
						p.owner.workDeadline = time.Now().Add(-time.Second)
					case "cancel":
						cancel()
					case "cancel-during-read":
						p.owner.service.Deps.CurrentUser = func() (system.User, error) {
							cancel()
							return owner()
						}
					case "deadline-during-read":
						p.owner.workDeadline = time.Now().Add(20 * time.Millisecond)
						p.owner.service.Deps.CurrentUser = func() (system.User, error) {
							<-time.After(time.Until(p.owner.workDeadline) + time.Millisecond)
							return owner()
						}
					case "storage":
						wantReason = string(StartupReasonStorage)
						p.owner.service.Deps.CurrentUser = func() (system.User, error) {
							return system.User{}, errors.New(syntheticKey + " " + root)
						}
					case "presence-storage":
						wantReason = string(StartupReasonStorage)
						if err := os.WriteFile(p.owner.guard.p.Path("compose.yml"), []byte("services: ["), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					if strings.HasPrefix(method, "info-") && mode != "presence-storage" {
						if err := os.WriteFile(p.owner.guard.p.Path("compose.yml"), []byte("services: ["), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					deadline := p.owner.workDeadline
					f.cleanupFailure = cleanupFailed
					if strings.HasPrefix(method, "info-") {
						_, err = view.Info(ctx)
					} else if method == "key" {
						_, err = view.GetAPIKey(ctx, "")
					} else {
						_, err = view.Credentials(ctx)
					}
					var operation *OperationError
					if !errors.As(err, &operation) || operation.Reason() != wantReason {
						t.Fatal("credential failure lost its typed Pair boundary")
					}
					if strings.Contains(err.Error(), syntheticKey) || strings.Contains(err.Error(), root) {
						t.Fatal("private credential failure escaped")
					}
					if strings.HasPrefix(mode, "deadline") && !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal("owner work deadline lost")
					}
					if strings.HasPrefix(mode, "cancel") && !errors.Is(err, context.Canceled) {
						t.Fatal("caller cancellation lost")
					}
					if operation.CleanupError() != nil != cleanupFailed || p.owner.closed == cleanupFailed || !p.owner.pair.failed || !p.owner.workDeadline.Equal(deadline) {
						t.Fatal("credential cleanup state or immutable deadline lost")
					}
					if cleanupFailed {
						if len(f.containers) != 2 {
							t.Fatal("failed cleanup did not retain resources")
						}
						if _, err := readOwnership(root); err != nil {
							t.Fatal("cleanup recovery ownership lost")
						}
					}
					assertDisabled := func() {
						t.Helper()
						before := len(f.runner.Calls())
						for _, id := range []string{"consumer", "peer"} {
							selected, _ := p.App(id)
							if _, err := selected.GetAPIKey(t.Context(), ""); err == nil {
								t.Fatal("failed owner exposed a selected key")
							}
							if _, err := selected.Credentials(t.Context()); err == nil {
								t.Fatal("failed owner exposed selected credentials")
							}
							if _, err := selected.Info(t.Context()); err == nil {
								t.Fatal("failed owner exposed selected Info")
							}
						}
						if _, err := p.Inspect(t.Context()); err == nil {
							t.Fatal("failed Pair remained inspectable")
						}
						if _, err := p.Setup(t.Context(), ""); err == nil {
							t.Fatal("failed Pair accepted Setup")
						}
						if _, err := p.Remove(t.Context(), "peer"); err == nil {
							t.Fatal("failed Pair accepted Remove")
						}
						if _, err := p.Install(t.Context(), "peer"); err == nil || len(f.runner.Calls()) != before {
							t.Fatal("disabled owner reached its runner")
						}
					}
					assertDisabled()
					if cleanupFailed {
						if err := p.Close(t.Context()); err == nil {
							t.Fatal("failed credential cleanup retry was hidden")
						}
						assertDisabled()
					}
					f.cleanupFailure = false
					if err := p.Close(t.Context()); err != nil || len(f.containers) != 0 {
						t.Fatal("independent credential cleanup could not finish")
					}
					assertDisabled()
				})
			}
		}
	}
}

func TestPairSelectedCredentialsExpectedAbsenceAndManualRemainLive(t *testing.T) {
	peer := pairFixtureApp{id: "peer"}
	peer.setup = func(ctx context.Context, h *helpers.H) error {
		if err := h.SetSecret(h.Key("api_key"), ""); err != nil {
			return err
		}
		return h.Step(ctx, "manual", "finish fixture", helpers.StepSpec{Do: func(context.Context) error { return helpers.ErrManual }, Hint: "fixture prerequisite"})
	}
	p, _, err := pairFake(t, "consumer-first", pairFixtureApp{id: "consumer", consumer: true}, peer)
	if err != nil {
		t.Fatal(err)
	}
	view, _ := p.App("peer")
	if _, err := view.GetAPIKey(t.Context(), ""); err == nil {
		t.Fatal("missing key accepted")
	}
	if creds, err := view.Credentials(t.Context()); err != nil || creds.APIKey != "" {
		t.Fatal("Manual credentials without an API key were rejected")
	}
	if result, err := p.Inspect(t.Context()); err != nil || result.Outcome != PairManual {
		t.Fatal("ordinary missing key closed a Manual Pair")
	}
	if _, err := p.Remove(t.Context(), "peer"); err != nil {
		t.Fatal(err)
	}
	if _, err := view.GetAPIKey(t.Context(), ""); err == nil {
		t.Fatal("absent App exposed a key")
	}
	if _, err := view.Credentials(t.Context()); err == nil {
		t.Fatal("absent App exposed credentials")
	}
	if _, err := view.Info(t.Context()); err == nil {
		t.Fatal("absent App exposed Info")
	}
	if _, err := p.Install(t.Context(), "peer"); err != nil || p.owner.closed || p.owner.pair.failed {
		t.Fatal("expected credential absence disabled the owner")
	}
}

type pairContextRunner struct{ base system.Runner }

func (r pairContextRunner) Run(ctx context.Context, cmd system.Cmd) (system.Result, error) {
	if err := ctx.Err(); err != nil {
		return system.Result{}, err
	}
	return r.base.Run(ctx, cmd)
}
func (r pairContextRunner) Stream(ctx context.Context, cmd system.Cmd, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.base.Stream(ctx, cmd, w)
}

func TestPairInfrastructureActionFailureClosesWholeOwner(t *testing.T) {
	for _, mode := range []string{"partial", "caps", "timeout", "cancel", "timeout-cleanup-failure", "inspection", "view-caps"} {
		t.Run(mode, func(t *testing.T) {
			f := newFakeDocker(t)
			f.deps.Runner = pairContextRunner{base: f.deps.Runner}
			active := false
			var cancel context.CancelFunc
			consumer := pairFixtureApp{id: "consumer", consumer: true}
			consumer.setup = func(ctx context.Context, _ *helpers.H) error {
				if !active {
					return nil
				}
				if mode == "cancel" {
					cancel()
				}
				<-ctx.Done()
				return ctx.Err()
			}
			peer := pairFixtureApp{id: "peer"}
			p, err := startPair(context.Background(), PairOptions{Consumer: "consumer", Target: "peer", Order: ConsumerFirst, Timeout: time.Minute, RootParent: t.TempDir()}, f.deps, catalog.New([]app.App{consumer, peer}, nil))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				f.cleanupFailure = false
				if err := p.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			root := p.RecoveryPath()
			ctx := context.Background()
			wantReason := string(StartupReasonSetup)
			switch mode {
			case "partial", "caps":
				if _, err := p.Remove(t.Context(), "peer"); err != nil {
					t.Fatal(err)
				}
				f.partialFailure = mode == "partial"
				f.unboundedCaps = mode == "caps"
				if mode == "caps" {
					wantReason = string(StartupReasonLimits)
				}
				_, err = p.Install(ctx, "peer")
			case "inspection":
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				_, err = p.Inspect(ctx)
			case "view-caps":
				for key, container := range f.containers {
					container.Memory = 0
					f.containers[key] = container
				}
				view, _ := p.App("peer")
				_, err = view.Info(ctx)
				wantReason = string(StartupReasonLimits)
			default:
				active = true
				if mode == "cancel" {
					ctx, cancel = context.WithCancel(ctx)
				} else {
					ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				}
				defer cancel()
				f.cleanupFailure = mode == "timeout-cleanup-failure"
				_, err = p.Setup(ctx, "consumer")
			}
			var operation *OperationError
			if !errors.As(err, &operation) || operation.Reason() != wantReason || strings.Contains(err.Error(), syntheticKey) || strings.Contains(err.Error(), root) {
				t.Fatalf("unsafe or untyped operation cause: %v", err)
			}
			if mode == "cancel" || mode == "inspection" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("primary cancellation lost")
				}
			}
			if strings.HasPrefix(mode, "timeout") && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("primary timeout lost")
			}
			if mode == "timeout-cleanup-failure" {
				if operation.CleanupError() == nil || len(f.containers) != 2 || p.owner.closed {
					t.Fatal("cleanup failure was hidden")
				}
				if _, err := readOwnership(root); err != nil {
					t.Fatal("recovery ownership lost")
				}
				before := f.creations
				if _, err := p.Install(t.Context(), "peer"); err == nil || f.creations != before {
					t.Fatal("failed owner accepted further mutation")
				}
				f.cleanupFailure = false
				if err := p.Close(t.Context()); err != nil {
					t.Fatal("failed cleanup could not retry", err)
				}
			} else if operation.CleanupError() != nil || !p.owner.closed {
				t.Fatal("independent cleanup did not complete", operation.CleanupError())
			}
			if len(f.containers) != 0 {
				t.Fatal("pair resources remained after successful cleanup")
			}
			if _, err := p.Inspect(t.Context()); err == nil {
				t.Fatal("failed pair remained available")
			}
		})
	}
}
