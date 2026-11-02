package testkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/host"
	"github.com/ajnart/self-host/internal/project"
	"github.com/ajnart/self-host/internal/service"
	"github.com/ajnart/self-host/internal/system"
)

type Options struct {
	App        string
	Timeout    time.Duration
	RootParent string
	Image      string
}

type Credentials struct {
	Username string
	Password string
	Email    string
	APIKey   string
}

type Info struct {
	ID          string
	URL         string
	Image       string
	ImageID     string
	RepoDigests []string
}

type App struct {
	mu       sync.Mutex
	service  *service.Service
	runner   system.Runner
	guard    *guardedRunner
	marker   ownership
	info     Info
	closed   bool
	done     chan struct{}
	deadline time.Time
}

func Start(ctx context.Context, opts Options) (*App, error) {
	env := environmentSnapshot()
	deps := system.Real("testkit")
	deps.Getenv = func(key string) string { return env[key] }
	deps.Runner = system.NewExecRunner(environmentList(env))
	return start(ctx, opts, deps, catalog.Default())
}

func start(ctx context.Context, opts Options, original *system.Deps, cat *catalog.Catalog) (_ *App, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	selected, ok := cat.App(opts.App)
	if !ok {
		return nil, startupError("APP", "testkit requires one Catalog App")
	}
	if opts.Image != "" && !project.ValidFixtureImage(opts.Image) {
		return nil, startupError("OPTIONS", "invalid selected image")
	}
	total := opts.Timeout
	if total == 0 {
		total = 15 * time.Minute
	}
	if total <= 0 || total > 15*time.Minute {
		return nil, startupError("OPTIONS", "testkit timeout must be positive and at most 15 minutes")
	}
	reserve := min(time.Minute, total/4)
	deadline := time.Now().Add(total)
	workDeadline := deadline.Add(-reserve)
	work, cancel := context.WithDeadline(ctx, workDeadline)
	defer cancel()
	deps := *original
	owner, err := deps.CurrentUser()
	if err != nil || owner.UID != os.Geteuid() {
		return nil, startupError("OWNERSHIP", "testkit requires the current local user")
	}
	endpoint, err := host.ActiveDockerEndpoint(work, &deps)
	if err != nil || endpoint.Kind != "unix" || !strings.HasPrefix(endpoint.Host, "unix:///") {
		if work.Err() != nil {
			return nil, work.Err()
		}
		return nil, startupError("DOCKER", "testkit requires a local Unix Docker endpoint")
	}
	root, err := os.MkdirTemp(opts.RootParent, "selfhost-testkit-")
	if err != nil {
		return nil, startupError("STORAGE", "testkit private root unavailable")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, startupError("STORAGE", "testkit private root resolution failed")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, startupError("STORAGE", "testkit private root resolution failed")
	}
	name := "testkit-" + service.GenerateHex(24)
	meta := ownership{Version: 1, Root: root, Name: name, App: opts.App, Endpoint: endpoint.Host, PID: os.Getpid()}
	if err := writeOwnership(meta); err != nil {
		os.Remove(root)
		return nil, startupError("STORAGE", "testkit could not register private ownership")
	}
	base := deps.Runner
	if production, ok := base.(*system.ExecRunner); ok {
		env := map[string]string{}
		for _, entry := range production.Environment() {
			key, value, _ := strings.Cut(entry, "=")
			env[key] = value
		}
		base = system.NewExecRunner(environmentList(fixtureEnvironment(env, endpoint.Host)))
	}
	owner.Home = root
	getenv := func(key string) string {
		switch key {
		case "SELF_HOST_HOME":
			return filepath.Join(root, "project")
		case "SELF_HOST_COMPOSE_PROJECT":
			return name
		case "DOCKER_HOST":
			return endpoint.Host
		case "CI", "SELF_HOST_NO_TELEMETRY":
			return "1"
		}
		return ""
	}
	deps.Getenv = getenv
	deps.CurrentUser = func() (system.User, error) { return owner, nil }
	deps.LookupUser = func(string) (system.User, error) { return system.User{}, errors.New("testkit refuses another owner") }
	deps.LANIP = func() (string, error) { return "127.0.0.1", nil }
	deps.TimeZone = func() string { return "UTC" }
	deps.Out, deps.Err, deps.In, deps.Interactive = io.Discard, io.Discard, strings.NewReader(""), false
	p := project.Open(owner)
	p.Root, p.ComposeName = getenv("SELF_HOST_HOME"), name
	ports := &portLease{}
	guard := &guardedRunner{base: base, p: p, policy: project.FixturePolicy{Root: root, Service: selected.Info().Service, Image: opts.Image}, ports: ports}
	deps.Runner = guard
	svc := service.New(&deps, events.Discard(), cat)
	a := &App{service: svc, runner: base, guard: guard, marker: meta, deadline: deadline, done: make(chan struct{})}
	defer func() {
		if err != nil {
			cause := err
			var safe *StartupError
			safeCause := errors.As(cause, &safe)
			cleanupCtx, stop := context.WithDeadline(context.Background(), deadline)
			defer stop()
			cleanupErr := a.Close(cleanupCtx)
			if cleanupErr != nil {
				err = fmt.Errorf("testkit start failed; cleanup incomplete; recovery: %s", root)
			} else {
				err = errors.New("testkit start failed; owned resources and storage cleaned")
			}
			if safeCause {
				err = errors.Join(safe, err)
			}
			if errors.Is(cause, context.DeadlineExceeded) || errors.Is(work.Err(), context.DeadlineExceeded) {
				err = errors.Join(context.DeadlineExceeded, err)
			} else if errors.Is(cause, context.Canceled) || ctx.Err() != nil {
				err = errors.Join(context.Canceled, err)
			}
		}
	}()
	unlock, err := startupLock(work)
	if err != nil {
		return nil, err
	}
	defer unlock()
	defer ports.handoff()
	if err := inactive(work, base, name, false); err != nil {
		return nil, err
	}
	if err := requireCaps(work, base); err != nil {
		return nil, err
	}
	for _, path := range []string{p.Root, filepath.Join(root, "data"), filepath.Join(root, "data", "media"), filepath.Join(root, "data", "torrents")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, err
		}
	}
	var sets []string
	primary := 0
	if selected.Info().Port > 0 {
		primary, err = ports.random("tcp")
		if err != nil {
			return nil, err
		}
		sets = append(sets, opts.App+".port="+strconv.Itoa(primary))
	}
	for _, input := range selected.Inputs() {
		if len(input.HostPortProtocols) == 0 {
			continue
		}
		port, err := ports.random(input.HostPortProtocols...)
		if err != nil {
			return nil, err
		}
		sets = append(sets, opts.App+"."+input.Key+"="+strconv.Itoa(port))
	}
	if err := svc.Install(work, service.InstallOptions{Targets: []string{opts.App}, Sets: sets, Exposure: service.ExposureLocal, DataRoot: filepath.Join(root, "data"), MediaRoot: filepath.Join(root, "data", "media"), DownloadsRoot: filepath.Join(root, "data", "torrents"), AdminUser: "testkit", AdminPassword: service.GeneratePassword(32), Settings: map[string]string{"LAN_IP": "127.0.0.1", "TZ": "UTC"}}); err != nil {
		var safe *StartupError
		if errors.As(err, &safe) || work.Err() != nil {
			return nil, err
		}
		return nil, startupError("SETUP", "testkit App Setup failed or requires a Manual step")
	}
	if err := guard.verify(work); err != nil {
		return nil, err
	}
	if guard.main.ID == "" || guard.main.Image == "" {
		return nil, startupError("CONTAINER", "testkit selected container absent")
	}
	digests, err := docker(work, base, "image", "inspect", "--format", `{{json .RepoDigests}}`, guard.main.Image)
	if err != nil {
		return nil, err
	}
	a.info = Info{ID: opts.App, URL: service.AppURL(map[string]string{"EXPOSURE": "local"}, primary, selected.Info().Path), Image: guard.main.ConfiguredImage, ImageID: guard.main.Image}
	if primary == 0 {
		a.info.URL = ""
	}
	if err := decodeDigests(digests, &a.info.RepoDigests); err != nil {
		return nil, err
	}
	go func() {
		timer := time.NewTimer(time.Until(workDeadline))
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
		case <-a.done:
			return
		}
		cleanup, stop := context.WithDeadline(context.Background(), deadline)
		defer stop()
		a.Close(cleanup)
	}()
	return a, nil
}

func (a *App) URL() string { return a.info.URL }

func (a *App) RecoveryPath() string { return a.marker.Root }

func (a *App) GetAPIKey(ctx context.Context, name string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return "", errors.New("testkit App is closed")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	creds, err := a.service.Credentials(ctx)
	if err != nil {
		return "", errors.New("testkit private credentials unavailable")
	}
	return apiKey(creds, a.info.ID, name)
}

func apiKey(creds service.Credentials, id, name string) (string, error) {
	if name == "" {
		name = "api_key"
	}
	key := service.SecretKey(id, name)
	var matches []string
	for candidate, value := range creds.Apps[id] {
		if strings.EqualFold(candidate, key) && value != "" {
			matches = append(matches, value)
		}
	}
	if len(matches) != 1 {
		return "", errors.New("testkit API key absent or ambiguous")
	}
	return matches[0], nil
}

func (a *App) Credentials(ctx context.Context) (Credentials, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return Credentials{}, errors.New("testkit App is closed")
	}
	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}
	creds, err := a.service.Credentials(ctx)
	if err != nil {
		return Credentials{}, errors.New("testkit private credentials unavailable")
	}
	out := Credentials{Username: creds.Admin.Username, Password: creds.Admin.Password, Email: creds.Admin.Email}
	if alias := creds.Admin.Aliases[a.info.ID]; alias != "" {
		out.Username = alias
	}
	if email := creds.Admin.Emails[a.info.ID]; email != "" {
		out.Email = email
	}
	out.APIKey, _ = apiKey(creds, a.info.ID, "")
	return out, nil
}

func (a *App) Info(ctx context.Context) (Info, error) {
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	info := a.info
	info.RepoDigests = slices.Clone(info.RepoDigests)
	return info, nil
}

func (a *App) Close(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	cleanup, stop := context.WithDeadline(ctx, minTime(a.deadline, time.Now().Add(time.Minute)))
	defer stop()
	if err := inactive(cleanup, a.runner, a.marker.Name, true); err != nil {
		return errors.New("testkit cleanup incomplete; private recovery marker retained")
	}
	if err := removeOwnedRoot(cleanup, a.marker.Root); err != nil {
		return err
	}
	a.closed = true
	close(a.done)
	return nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func environmentSnapshot() map[string]string {
	env := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	return env
}

func environmentList(env map[string]string) []string {
	result := make([]string, 0, len(env))
	for key, value := range env {
		result = append(result, key+"="+value)
	}
	slices.Sort(result)
	return result
}

func fixtureEnvironment(source map[string]string, endpoint string) map[string]string {
	env := map[string]string{}
	for _, key := range []string{"PATH", "HOME", "DOCKER_CONFIG", "TMPDIR", "TMP", "TEMP", "SYSTEMROOT", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "LANG", "LC_ALL"} {
		if value, ok := source[key]; ok {
			env[key] = value
		}
	}
	env["DOCKER_HOST"], env["DOCKER_CONTEXT"] = endpoint, ""
	return env
}
