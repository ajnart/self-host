package testkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
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
	return []app.Link{app.NewLink("link-peer", "peer", "pair fixture", app.LinkStep{Do: func(ctx context.Context, from, to *helpers.H) error {
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
