package testkit

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/compose"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/project"
	"github.com/ajnart/self-host/internal/service"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
)

const syntheticKey = "synthetic-private-api-key"
const syntheticImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fixtureApp struct {
	setup func(context.Context, *helpers.H) error
}

func (fixtureApp) Info() app.Info {
	return app.Info{ID: "fixture", Name: "Fixture", Service: "fixture", Image: "example/app:latest", Port: 8199, ContainerPort: 80}
}
func (fixtureApp) Inputs() []app.Input { return nil }
func (fixtureApp) Links() []app.Link   { return nil }
func (fixtureApp) Compose(rc app.RenderContext) string {
	return fmt.Sprintf("services:\n  fixture:\n    image: example/app:latest\n    ports: ['${BIND_IP}:%d:80']\n    volumes: ['./config:/config']\n", rc.Port)
}
func (a fixtureApp) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.SetSecret(h.Key("api_key"), syntheticKey); err != nil {
		return err
	}
	if a.setup != nil {
		return a.setup(ctx, h)
	}
	return nil
}
func (fixtureApp) Check(context.Context, *helpers.H) error { return nil }

type fakeDocker struct {
	mu             sync.Mutex
	t              *testing.T
	runner         *systemtest.Runner
	deps           *system.Deps
	containers     map[string]containerInfo
	partialFailure bool
	unboundedCaps  bool
	cleanupFailure bool
	pulled         map[string]string
	creations      int
}

func newFakeDocker(t *testing.T) *fakeDocker {
	t.Helper()
	deps := systemtest.NewDeps(t)
	runner := deps.Runner.(*systemtest.Runner)
	deps.CurrentUser = func() (system.User, error) {
		return system.User{Name: "fixture-owner", Home: t.TempDir(), UID: os.Geteuid(), GID: os.Getegid()}, nil
	}
	f := &fakeDocker{t: t, deps: deps, runner: runner, containers: map[string]containerInfo{}, pulled: map[string]string{}}
	runner.OnFunc("docker", f.run)
	return f
}

func (f *fakeDocker) run(cmd system.Cmd) system.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	args := cmd.Args
	if slices.Equal(args, []string{"compose", "version", "--short"}) {
		return system.Result{Stdout: []byte("2.30.0")}
	}
	if len(args) > 0 && args[0] == "context" {
		return system.Result{Stdout: []byte("unix:///synthetic/docker.sock")}
	}
	if len(args) > 0 && args[0] == "info" {
		if args[len(args)-1] == `{{json .}}` {
			return system.Result{Stdout: []byte(`{"MemoryLimit":true,"CpuCfsQuota":true,"PidsLimit":true}`)}
		}
		return system.Result{Stdout: []byte("[]")}
	}
	if len(args) > 0 && args[0] == "ps" {
		name := strings.TrimPrefix(args[len(args)-1], "label=com.docker.compose.project=")
		var ids []string
		for _, c := range f.containers {
			if c.Project == name {
				ids = append(ids, c.ID)
			}
		}
		return system.Result{Stdout: []byte(strings.Join(ids, "\n"))}
	}
	if len(args) > 1 && args[0] == "network" {
		return system.Result{}
	}
	if len(args) > 1 && args[0] == "image" {
		return system.Result{Stdout: []byte(`["example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]`)}
	}
	if len(args) > 1 && args[0] == "container" {
		for name, c := range f.containers {
			if args[len(args)-1] != c.ID {
				continue
			}
			if args[1] == "rm" {
				if f.cleanupFailure {
					return system.Result{ExitCode: 1}
				}
				delete(f.containers, name)
				return system.Result{}
			}
			data, _ := json.Marshal(c)
			return system.Result{Stdout: data}
		}
	}
	if len(args) > 5 && args[0] == "compose" {
		root, name := args[2], args[4]
		verb, rest := composeVerb(args[5:])
		if verb == "ps" {
			var containers []compose.Container
			for _, c := range f.containers {
				if c.Project == name {
					containers = append(containers, compose.Container{Service: c.Service, State: "running", Image: c.ConfiguredImage})
				}
			}
			data, _ := json.Marshal(containers)
			return system.Result{Stdout: data}
		}
		if verb == "rm" {
			for key, c := range f.containers {
				if c.Project == name && slices.Contains(rest, c.Service) {
					delete(f.containers, key)
				}
			}
			return system.Result{}
		}
		if verb == "down" {
			if f.cleanupFailure {
				return system.Result{ExitCode: 1}
			}
			for key, c := range f.containers {
				if c.Project == name {
					delete(f.containers, key)
				}
			}
			return system.Result{}
		}
		if verb != "up" && verb != "pull" {
			return system.Result{}
		}
		data, err := os.ReadFile(filepath.Join(root, "compose.yml"))
		if err != nil {
			f.t.Error(err)
			return system.Result{ExitCode: 1}
		}
		var doc struct {
			Services map[string]struct {
				Image  string `yaml:"image"`
				CPUs   string `yaml:"cpus"`
				Memory string `yaml:"mem_limit"`
				Pids   int    `yaml:"pids_limit"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			f.t.Error(err)
			return system.Result{ExitCode: 1}
		}
		for service, svc := range doc.Services {
			if !slices.Contains(rest, service) {
				continue
			}
			if svc.CPUs != "1.0" || svc.Memory != "1g" || svc.Pids != 512 {
				f.t.Error("container command reached Docker before caps")
				return system.Result{ExitCode: 1}
			}
			if verb == "pull" {
				f.pulled[name+":"+service] = svc.Image
				continue
			}
			if f.pulled[name+":"+service] != svc.Image {
				f.t.Error("selected image not freshly pulled")
			}
			key := name + ":" + service
			f.creations++
			f.containers[key] = containerInfo{ID: fmt.Sprintf("%x", sha256.Sum256([]byte(key))), Image: syntheticImageID, ConfiguredImage: svc.Image, Project: name, Service: service, Memory: 1 << 30, NanoCPUs: 1e9, Pids: 512}
			if f.unboundedCaps {
				container := f.containers[key]
				container.Memory = 0
				f.containers[key] = container
			}
			if f.partialFailure {
				return system.Result{ExitCode: 1, Stderr: []byte(syntheticKey)}
			}
		}
	}
	return system.Result{}
}

func startFake(t *testing.T, f *fakeDocker, selected fixtureApp) (*App, error) {
	t.Helper()
	return start(context.Background(), Options{App: "fixture", Image: "example/app:selected", RootParent: t.TempDir(), Timeout: time.Minute}, f.deps, catalog.New([]app.App{selected}, nil))
}

func TestFixturesIsolateOwnerEnvironmentPortsAndPrivateCredentials(t *testing.T) {
	before := os.Environ()
	f := newFakeDocker(t)
	first, err := startFake(t, f, fixtureApp{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := first.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	second, err := startFake(t, f, fixtureApp{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if first.RecoveryPath() == second.RecoveryPath() || first.URL() == second.URL() || first.marker.Name == second.marker.Name {
		t.Fatal("fixtures share identity")
	}
	if !reflect.DeepEqual(before, os.Environ()) {
		t.Fatal("global environment changed")
	}
	for _, fixture := range []*App{first, second} {
		if !privateOwned(fixture.RecoveryPath(), true) || !privateOwned(fixture.guard.p.Root, true) {
			t.Fatal("root or Project is not private")
		}
		key, err := fixture.GetAPIKey(context.Background(), "")
		if err != nil || key != syntheticKey {
			t.Fatal("private API key unavailable")
		}
		creds, err := fixture.Credentials(context.Background())
		if err != nil || creds.Username != "testkit" || creds.Password == "" || creds.APIKey != syntheticKey {
			t.Fatal("private credentials incomplete")
		}
		info, err := fixture.Info(context.Background())
		if err != nil || info.Image != "example/app:selected" || info.ImageID != syntheticImageID || len(info.RepoDigests) != 1 {
			t.Fatal("selected image provenance incomplete")
		}
		public, _ := json.Marshal(info)
		if strings.Contains(string(public), syntheticKey) || strings.Contains(string(public), creds.Password) {
			t.Fatal("public Info leaked credentials")
		}
		if err := fixture.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Close(context.Background()); err != nil {
			t.Fatal("Close is not idempotent")
		}
		if _, err := os.Stat(fixture.RecoveryPath()); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("successful Close retained private fixture data")
		}
	}
}

func TestSingleAppCredentialFailuresKeepLegacySemantics(t *testing.T) {
	for _, method := range []string{"key", "credentials"} {
		for _, mode := range []string{"deadline", "cancel", "storage"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				f := newFakeDocker(t)
				a, err := startFake(t, f, fixtureApp{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := a.Close(context.Background()); err != nil {
						t.Error(err)
					}
				})
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				a.workDeadline = time.Now().Add(-time.Second)
				if mode == "cancel" {
					cancel()
				}
				if mode == "storage" {
					a.service.Deps.CurrentUser = func() (system.User, error) {
						return system.User{}, errors.New(syntheticKey)
					}
				}
				if method == "key" {
					_, err = a.GetAPIKey(ctx, "")
				} else {
					_, err = a.Credentials(ctx)
				}
				if (err == nil) != (mode == "deadline") || a.closed || len(f.containers) != 1 {
					t.Fatal("legacy credential lifetime or cleanup changed")
				}
				var operation *OperationError
				if errors.As(err, &operation) || mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal("legacy credential failure typing changed")
				}
				if err != nil && strings.Contains(err.Error(), syntheticKey) {
					t.Fatal("legacy storage failure leaked")
				}
			})
		}
	}
}

func TestPartialCreationAndCancelledSetupCleanWithIndependentContext(t *testing.T) {
	for _, mode := range []string{"partial", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newFakeDocker(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			selected := fixtureApp{}
			if mode == "partial" {
				f.partialFailure = true
			} else {
				selected.setup = func(context.Context, *helpers.H) error { cancel(); return ctx.Err() }
			}
			parent := t.TempDir()
			parent, err := filepath.EvalSymlinks(parent)
			if err != nil {
				t.Fatal(err)
			}
			_, err = start(ctx, Options{App: "fixture", RootParent: parent, Timeout: time.Minute}, f.deps, catalog.New([]app.App{selected}, nil))
			if err == nil || strings.Contains(err.Error(), syntheticKey) {
				t.Fatal("missing or unsafe failure")
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation cause lost")
			}
			if len(f.containers) != 0 {
				t.Fatal("partial containers remain")
			}
			roots, err := os.ReadDir(parent)
			if err != nil || len(roots) != 0 {
				t.Fatal("successful failed-start cleanup retained private fixture data")
			}
		})
	}
}

func TestCloseFallsBackToLabelsWhenProjectIsMissingAndRetainsFailedRecovery(t *testing.T) {
	f := newFakeDocker(t)
	fixture, err := startFake(t, f, fixtureApp{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.guard.p.Root, fixture.guard.p.Root+"-retained"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(fixture.RecoveryPath(), "outside-link")); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := removeOwnedRoot(canceled, fixture.RecoveryPath()); !errors.Is(err, context.Canceled) {
		t.Fatal("filesystem cleanup ignored cancellation")
	}
	if _, err := readOwnership(fixture.RecoveryPath()); err != nil {
		t.Fatal("canceled cleanup lost ownership marker")
	}
	f.cleanupFailure = true
	if err := fixture.Close(context.Background()); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	if _, err := readOwnership(fixture.RecoveryPath()); err != nil {
		t.Fatal("cleanup failure lost ownership marker")
	}
	f.cleanupFailure = false
	if os.Geteuid() != 0 {
		for _, kind := range []string{"fifo", "ancestor-link"} {
			blocked := filepath.Join(fixture.RecoveryPath(), "blocked")
			if err := os.Mkdir(blocked, 0o700); err != nil {
				t.Fatal(err)
			}
			child := filepath.Join(blocked, "child")
			var err error
			if kind == "fifo" {
				err = unix.Mkfifo(child, 0o600)
			} else {
				err = os.Symlink("..", child)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(blocked, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(blocked, 0o700) })
			if err := fixture.Close(context.Background()); err == nil {
				t.Fatal("filesystem cleanup failure hidden")
			}
			if _, err := readOwnership(fixture.RecoveryPath()); err != nil {
				t.Fatal("filesystem cleanup failure lost ownership marker")
			}
			if err := os.Chmod(blocked, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(child); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(blocked); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := fixture.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.containers) != 0 {
		t.Fatal("label fallback failed")
	}
	if data, err := os.ReadFile(filepath.Join(outside, "sentinel")); err != nil || string(data) != "outside" {
		t.Fatal("filesystem cleanup followed a link outside its owned root")
	}
}

func TestActualResourceCapsAreVerifiedAndFailuresCleaned(t *testing.T) {
	f := newFakeDocker(t)
	f.unboundedCaps = true
	if fixture, err := startFake(t, f, fixtureApp{}); err == nil || fixture != nil {
		t.Fatal("unbounded actual container accepted")
	} else if !strings.Contains(err.Error(), "could not verify resource caps or selected image") {
		t.Fatal("safe startup cause lost")
	}
	if len(f.containers) != 0 {
		t.Fatal("unbounded container not cleaned")
	}
}

func TestCleanupRefusesUnownedAndLiveRootsBeforeDocker(t *testing.T) {
	if err := Cleanup(context.Background(), t.TempDir()); err == nil {
		t.Fatal("unowned cleanup accepted")
	}
	f := newFakeDocker(t)
	fixture, err := startFake(t, f, fixtureApp{})
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close(context.Background())
	if err := Cleanup(context.Background(), fixture.RecoveryPath()); err == nil {
		t.Fatal("live helper cleanup accepted")
	}
}

func TestPortReservationContendsUntilLockedHandoff(t *testing.T) {
	lease := &portLease{}
	port, err := lease.random("tcp", "udp")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.handoff()
	for _, protocol := range []string{"tcp", "udp"} {
		other := &portLease{}
		if err := other.reserve([]project.FixturePort{{Port: port, Protocol: protocol}}); err == nil {
			other.handoff()
			t.Fatal("reserved publication reused")
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := lease.reserve([]project.FixturePort{{Port: listener.Addr().(*net.TCPAddr).Port, Protocol: "tcp"}}); err == nil {
		t.Fatal("external collision accepted")
	}
}

func TestCreationAndHelpersCannotBypassCanonicalCaps(t *testing.T) {
	f := newFakeDocker(t)
	fixture, err := startFake(t, f, fixtureApp{})
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close(context.Background())
	guard := fixture.guard
	for _, args := range [][]string{{"up", "--scale", "fixture=9"}, {"run", "fixture"}, {"create", "--build"}, {"start", "outside"}} {
		if _, err := guard.ComposePreparation().PrepareCompose(context.Background(), guard.p.Root, guard.p.ComposeName, args); err == nil {
			t.Fatal("creation override accepted")
		}
	}
	for _, args := range [][]string{{"run", "busybox"}, {"create", "busybox"}, {"start", "outside"}, {"compose", "--project-directory", guard.p.Root, "-p", guard.p.ComposeName, "up", "-d", "fixture"}} {
		if _, err := guard.Run(context.Background(), system.Cmd{Name: "docker", Args: args}); err == nil {
			t.Fatal("direct helper bypassed preparation")
		}
	}
	deps := *f.deps
	deps.Runner = guard
	c := compose.New(&deps, events.Discard(), guard.p.Root, guard.p.ComposeName)
	if err := c.EmptyDir(context.Background(), "fixture", guard.p.Root); err == nil {
		t.Fatal("one-off helper was not refused")
	}
	if err := c.Chown(context.Background(), "fixture", guard.p.Root, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("direct helper fallback was not refused")
	}
}

func TestAPIKeyAbsentAndAmbiguousErrorsArePrivate(t *testing.T) {
	creds := service.Credentials{Apps: map[string]map[string]string{"fixture": {"FIXTURE_API_KEY": syntheticKey, "fixture_api_key": "synthetic-other-private"}}}
	for _, name := range []string{"", "missing"} {
		if key, err := apiKey(creds, "fixture", name); err == nil || key != "" || strings.Contains(err.Error(), syntheticKey) {
			t.Fatal("unsafe absent or ambiguous key result")
		}
	}
}

func TestFixtureEnvironmentCannotOverridePrivateComposeInterpolation(t *testing.T) {
	source := map[string]string{"PATH": "/synthetic/bin", "HOME": "/synthetic/home", "DOCKER_CONTEXT": "remote", "DOCKER_HOST": "ssh://remote", "COMPOSE_FILE": "/outside/compose.yml", "BIND_IP": "0.0.0.0", "DATA_ROOT": "/outside", "ADMIN_PASSWORD": "synthetic-private", "SELF_HOST_HOME": "/outside/project"}
	before := make(map[string]string, len(source))
	for key, value := range source {
		before[key] = value
	}
	env := fixtureEnvironment(source, "unix:///synthetic/docker.sock")
	if !reflect.DeepEqual(source, before) {
		t.Fatal("source environment mutated")
	}
	for _, key := range []string{"COMPOSE_FILE", "BIND_IP", "DATA_ROOT", "ADMIN_PASSWORD", "SELF_HOST_HOME"} {
		if _, found := env[key]; found {
			t.Fatal("ambient environment can override owned Compose")
		}
	}
	if env["DOCKER_HOST"] != "unix:///synthetic/docker.sock" || env["DOCKER_CONTEXT"] != "" {
		t.Fatal("Docker endpoint not pinned")
	}
}
