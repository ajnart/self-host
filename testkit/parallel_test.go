package testkit

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/project"
	"github.com/ajnart/self-host/internal/system"
	"golang.org/x/sys/unix"
)

type startupBarrier struct {
	arrivals atomic.Int32
	ready    chan struct{}
}

func newStartupBarrier() *startupBarrier { return &startupBarrier{ready: make(chan struct{})} }
func (b *startupBarrier) wait(ctx context.Context) error {
	if b.arrivals.Add(1) == 2 {
		close(b.ready)
	}
	select {
	case <-b.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestIndependentPullCreationAndSetupOverlap(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	f := newFakeDocker(t)
	pull, up, setup := newStartupBarrier(), newStartupBarrier(), newStartupBarrier()
	f.runner.OnFunc("docker", func(cmd system.Cmd) system.Result {
		if len(cmd.Args) > 5 && cmd.Args[0] == "compose" {
			verb, _ := composeVerb(cmd.Args[5:])
			var barrier *startupBarrier
			if verb == "pull" {
				barrier = pull
			}
			if verb == "up" {
				barrier = up
			}
			if barrier != nil && barrier.wait(ctx) != nil {
				return system.Result{ExitCode: 1}
			}
		}
		return f.run(cmd) // Never block at a barrier while holding fakeDocker.mu.
	})
	type result struct {
		app *App
		err error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			selected := fixtureApp{setup: func(ctx context.Context, _ *helpers.H) error { return setup.wait(ctx) }}
			app, err := start(ctx, Options{App: "fixture", RootParent: t.TempDir(), Timeout: time.Minute}, f.deps, catalog.New([]app.App{selected}, nil))
			if err != nil {
				cancel()
			}
			results <- result{app, err}
		}()
	}
	var apps []*App
	for range 2 {
		r := <-results
		if r.app != nil {
			apps = append(apps, r.app)
			t.Cleanup(func() { r.app.Close(context.Background()) })
		}
		if r.err != nil {
			t.Error("concurrent startup failed")
		}
	}
	if len(apps) != 2 {
		return
	}
	if pull.arrivals.Load() != 2 || up.arrivals.Load() != 2 || setup.arrivals.Load() != 2 {
		t.Fatal("startup phases did not overlap")
	}
	if apps[0].URL() == apps[1].URL() || apps[0].marker.Name == apps[1].marker.Name {
		t.Fatal("concurrent fixtures share identity")
	}
	if err := apps[0].Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.containers) != 1 {
		t.Fatal("cleanup crossed fixture ownership")
	}
	if err := apps[1].Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.containers) != 0 {
		t.Fatal("owned containers remain")
	}
}

func TestSharedStartupAdmissionPreservesExclusiveHarness(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	first, err := sharedStartupLock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := sharedStartupLock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if unlock, err := startupLock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		if unlock != nil {
			unlock()
		}
		t.Fatal("exclusive harness entered shared startup")
	}
	first()
	second()
	exclusive, err := startupLock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer exclusive()
	ctx, cancel2 := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel2()
	if unlock, err := sharedStartupLock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		if unlock != nil {
			unlock()
		}
		t.Fatal("fixture bypassed exclusive harness")
	}
}

func TestPortClaimSubprocess(t *testing.T) {
	if os.Getenv("TESTKIT_PORT_CLAIM_HELPER") != "1" {
		return
	}
	port, err := strconv.Atoi(os.Getenv("TESTKIT_CLAIM_PORT"))
	if err != nil {
		os.Exit(2)
	}
	lease := &portLease{}
	err = lease.reserve([]project.FixturePort{{Port: port, Protocol: "tcp"}})
	lease.release()
	if (err != nil) != (os.Getenv("TESTKIT_EXPECT_BLOCKED") == "1") {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestPortClaimsSurviveHandoffAcrossProcesses(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	lease := &portLease{}
	port, err := lease.random("tcp")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.release()
	claim := lease.claims[project.FixturePort{Port: port, Protocol: "tcp"}]
	flags, err := unix.FcntlInt(claim.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("claim can leak into child executables")
	}
	lease.handoff()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	check := func(blocked string) {
		cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestPortClaimSubprocess$")
		cmd.Env = append(os.Environ(), "TESTKIT_PORT_CLAIM_HELPER=1", "TESTKIT_CLAIM_PORT="+strconv.Itoa(port), "TESTKIT_EXPECT_BLOCKED="+blocked)
		if err := cmd.Run(); err != nil {
			t.Fatal("cross-process claim outcome was wrong")
		}
	}
	check("1")
	lease.release()
	lease.release()
	check("0")
}

func TestPartialDualProtocolReservationRollsBack(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	port := udp.LocalAddr().(*net.UDPAddr).Port
	lease := &portLease{}
	defer lease.release()
	if err := lease.reserve([]project.FixturePort{{Port: port, Protocol: "tcp"}, {Port: port, Protocol: "udp"}}); err == nil {
		t.Fatal("occupied UDP port accepted")
	}
	if len(lease.claims) != 0 || len(lease.sockets) != 0 {
		t.Fatal("partial claim was retained")
	}
	other := &portLease{}
	defer other.release()
	if err := other.reserve([]project.FixturePort{{Port: port, Protocol: "tcp"}}); err != nil {
		t.Fatal("rolled-back TCP claim is blocked")
	}
}

func TestCleanupFailureRetainsClaimsUntilSuccessfulRetry(t *testing.T) {
	f := newFakeDocker(t)
	fixture, err := startFake(t, f, fixtureApp{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.cleanupFailure = false; fixture.Close(context.Background()) })
	var port project.FixturePort
	for selected := range fixture.guard.ports.claims {
		port = selected
		break
	}
	f.cleanupFailure = true
	if err := fixture.Close(context.Background()); err == nil {
		t.Fatal("cleanup unexpectedly passed")
	}
	if _, kept := incompletePortLeases.Load(fixture.marker.Root); !kept {
		t.Fatal("failed-cleanup lease can be garbage collected")
	}
	other := &portLease{}
	defer other.release()
	if err := other.reserve([]project.FixturePort{port}); err == nil {
		t.Fatal("failed cleanup released its claim")
	}
	f.cleanupFailure = false
	if err := fixture.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, kept := incompletePortLeases.Load(fixture.marker.Root); kept {
		t.Fatal("successful cleanup retained lease")
	}
	if err := other.reserve([]project.FixturePort{port}); err != nil {
		t.Fatal("successful cleanup did not release claim")
	}
}

func TestPortClaimRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "public", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			path := filepath.Join(os.TempDir(), "selfhost-testkit-port-tcp-12345.lock")
			switch kind {
			case "symlink":
				if err := os.Symlink("missing", path); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.WriteFile(path, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if claim, err := claimPort(project.FixturePort{Port: 12345, Protocol: "tcp"}); err == nil {
				claim.Close()
				t.Fatal("unsafe port claim accepted")
			}
		})
	}
}

type claimProbePairApp struct {
	pairFixtureApp
	ports map[string]int
}

func (a claimProbePairApp) Compose(rc app.RenderContext) string {
	a.ports[a.id] = rc.Port
	return a.pairFixtureApp.Compose(rc)
}

func TestPairKeepsUnstartedPeerPortClaimAfterHandoff(t *testing.T) {
	for _, order := range []string{"consumer-first", "target-first"} {
		t.Run(order, func(t *testing.T) {
			f := newFakeDocker(t)
			ports := map[string]int{}
			first, second := "consumer", "peer"
			if order == "target-first" {
				first, second = second, first
			}
			probed := false
			probe := func(_ context.Context, h *helpers.H) error {
				if h.App != first {
					return nil
				}
				if len(f.containers) != 1 {
					return errors.New("peer already started before claim probe")
				}
				port := ports[second]
				listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
				if err != nil {
					return errors.New("peer socket was not handed off")
				}
				listener.Close()
				other := &portLease{}
				defer other.release()
				if err := other.reserve([]project.FixturePort{{Port: port, Protocol: "tcp"}}); err == nil {
					return errors.New("unstarted peer port claim was lost")
				}
				probed = true
				return nil
			}
			consumer := claimProbePairApp{pairFixtureApp: pairFixtureApp{id: "consumer", consumer: true, fixtureApp: fixtureApp{setup: probe}}, ports: ports}
			peer := claimProbePairApp{pairFixtureApp: pairFixtureApp{id: "peer", fixtureApp: fixtureApp{setup: probe}}, ports: ports}
			pair, err := startPair(t.Context(), PairOptions{Consumer: "consumer", Target: "peer", Order: PairOrder(order), Timeout: time.Minute, RootParent: t.TempDir()}, f.deps, catalog.New([]app.App{consumer, peer}, nil))
			if pair != nil {
				defer pair.Close(context.Background())
			}
			if err != nil || !probed {
				t.Fatal("Pair handoff claim was not preserved")
			}
		})
	}
}

func TestFailedStartupClaimsSurviveGarbageCollection(t *testing.T) {
	f := newFakeDocker(t)
	f.partialFailure, f.cleanupFailure = true, true
	parent := t.TempDir()
	fixture, err := start(t.Context(), Options{App: "fixture", RootParent: parent, Timeout: time.Minute}, f.deps, catalog.New([]app.App{fixtureApp{}}, nil))
	if fixture != nil || !errors.Is(err, ErrCleanupIncomplete) {
		t.Fatal("expected failed startup and cleanup")
	}
	runtime.GC() // No App handle remains to keep os.File claims alive.
	var root string
	var lease *portLease
	incompletePortLeases.Range(func(key, value any) bool {
		if strings.HasPrefix(key.(string), parent+string(os.PathSeparator)) {
			root, lease = key.(string), value.(*portLease)
			return false
		}
		return true
	})
	if lease == nil {
		t.Fatal("unreachable failed-start lease was not retained")
	}
	t.Cleanup(func() {
		f.cleanupFailure = false
		for _, container := range f.containers {
			if err := inactive(context.Background(), f.runner, container.Project, true); err != nil {
				t.Error(err)
			}
		}
		if err := removeOwnedRoot(context.Background(), root); err != nil {
			t.Error(err)
		}
		lease.release()
		incompletePortLeases.Delete(root)
	})
	for port := range lease.claims {
		other := &portLease{}
		if err := other.reserve([]project.FixturePort{port}); err == nil {
			other.release()
			t.Fatal("garbage collection released a failed-start claim")
		}
	}
}
