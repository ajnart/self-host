package testkit

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/project"
	"github.com/ajnart/self-host/internal/service"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
)

type PairOrder string

const (
	ConsumerFirst PairOrder = "consumer-first"
	TargetFirst   PairOrder = "target-first"
)

type PairOutcome string

const (
	PairComplete     PairOutcome = "complete"
	PairManual       PairOutcome = "manual"
	PairPrerequisite PairOutcome = "prerequisite"
	PairFailed       PairOutcome = "failed"
)

type PairOptions struct {
	Consumer            string
	Target              string
	Order               PairOrder
	Timeout             time.Duration
	RootParent          string
	Images              map[string]string
	SonarrFilterWitness bool
}

type PairStep struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Verified bool   `json:"verified,omitempty"`
}

func (s PairStep) linked() bool {
	return s.Status == string(stepstatus.StepOK) || s.Status == string(stepstatus.StepSkip) && s.Verified
}

type PairAppStatus struct {
	ID        string     `json:"id"`
	URL       string     `json:"url"`
	Installed bool       `json:"installed"`
	State     string     `json:"state"`
	Setup     string     `json:"setup"`
	Steps     []PairStep `json:"steps"`
}

type PairInspection struct {
	Outcome PairOutcome     `json:"outcome"`
	Reason  string          `json:"reason"`
	Link    string          `json:"link"`
	Apps    []PairAppStatus `json:"apps"`
}

type Pair struct{ owner *App }

type AppView struct {
	owner *App
	id    string
}

type pairState struct {
	options             PairOptions
	apps                []app.App
	ports               map[string]int
	install             service.InstallOptions
	link                string
	initial             PairInspection
	failed              bool
	sonarrFilterWitness sonarrFilterWitnessState
}

func StartPair(ctx context.Context, options PairOptions) (*Pair, error) {
	env := environmentSnapshot()
	deps := system.Real("testkit")
	deps.Getenv = func(key string) string { return env[key] }
	deps.Runner = system.NewExecRunner(environmentList(env))
	return startPair(ctx, options, deps, catalog.Default())
}

func startPair(ctx context.Context, options PairOptions, deps *system.Deps, cat *catalog.Catalog) (*Pair, error) {
	owner, err := startFixture(ctx, Options{App: options.Consumer, Timeout: options.Timeout, RootParent: options.RootParent}, deps, cat, &options)
	if err != nil {
		return nil, err
	}
	return &Pair{owner: owner}, nil
}

func pairSelection(cat *catalog.Catalog, options PairOptions) (app.App, error) {
	if options.SonarrFilterWitness && (options.Consumer != "homarr" || options.Target != "sonarr" || !sonarrWitnessImage(options.Images["sonarr"])) {
		return nil, startupError(StartupReasonUnsupported, "testkit Sonarr filter witness requires the selected pair and an immutable target image")
	}
	consumer, found := cat.App(options.Consumer)
	target, targetFound := cat.App(options.Target)
	if !found || !targetFound || options.Consumer == options.Target || catalog.Unavailable(options.Consumer) != "" || catalog.Unavailable(options.Target) != "" {
		return nil, startupError(StartupReasonApp, "testkit pair requires two distinct available Catalog Apps")
	}
	if options.Order != "consumer-first" && options.Order != "target-first" {
		return nil, startupError(StartupReasonOptions, "testkit pair requires an explicit installation order")
	}
	links := 0
	for _, link := range consumer.Links() {
		if link.Target == options.Target {
			links++
		}
	}
	if links != 1 {
		return nil, startupError(StartupReasonOptions, "testkit pair requires one declared consumer Link")
	}
	for _, selected := range []app.App{consumer, target} {
		if len(selected.Inputs()) != 0 || selected.Info().Port <= 0 || selected.Info().NoWebUI {
			return nil, startupError(StartupReasonUnsupported, "testkit pair requires web Apps without additional inputs")
		}
	}
	for id, image := range options.Images {
		if id != options.Consumer && id != options.Target || !project.ValidFixtureImage(image) {
			return nil, startupError(StartupReasonOptions, "invalid pair image selection")
		}
	}
	return target, nil
}

func (a *App) startPair(ctx context.Context, options PairOptions, apps []app.App, ports map[string]int, install service.InstallOptions, owner system.User) error {
	options.Images = cloneImages(options.Images)
	a.pair = &pairState{options: options, apps: apps, ports: ports, install: install}
	for _, link := range apps[0].Links() {
		if link.Target == options.Target {
			a.pair.link = link.ID
		}
	}
	settings := a.service.DetectSettings(owner, service.ExposureLocal)
	settings["DATA_ROOT"], settings["MEDIA_ROOT"], settings["DOWNLOADS_ROOT"] = install.DataRoot, install.MediaRoot, install.DownloadsRoot
	preview := &project.Project{Root: filepath.Join(a.marker.Root, "admission"), Owner: owner, ComposeName: a.marker.Name}
	if err := preview.MkdirAll(); err != nil {
		return startupError(StartupReasonStorage, "testkit pair admission storage unavailable")
	}
	if err := preview.SaveSettings(settings); err != nil {
		return err
	}
	images := map[string]string{}
	for _, selected := range apps {
		id := selected.Info().ID
		if image := options.Images[id]; image != "" {
			images[selected.Info().Service] = image
		}
		if err := preview.WriteFragment(id, selected.Compose(app.RenderContext{Port: ports[id], Settings: settings})); err != nil {
			return startupError(StartupReasonUnsupported, "testkit pair Compose admission failed")
		}
	}
	a.guard.policy.Images = images
	policy := a.guard.policy
	plan, err := preview.PrepareFixtureCompose(policy)
	if err != nil {
		return startupError(StartupReasonUnsupported, "testkit pair Compose admission failed")
	}
	if err := a.guard.ports.reserve(plan.Ports); err != nil {
		return err
	}
	a.guard.admitted = &plan
	order := []string{options.Consumer, options.Target}
	if options.Order == "target-first" {
		slices.Reverse(order)
	}
	for i, id := range order {
		selected := install
		selected.Targets = []string{id}
		if err := a.service.Install(ctx, selected); err != nil && !errors.Is(err, service.ErrManual) && !errors.Is(err, service.ErrIncomplete) {
			var safe *StartupError
			if errors.As(err, &safe) || ctx.Err() != nil {
				return err
			}
			return startupError(StartupReasonSetup, "testkit pair installation failed")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if i == 0 {
			initial, err := a.inspectPair(ctx)
			if err != nil {
				return err
			}
			a.pair.initial = initial
		}
	}
	return nil
}

func cloneImages(source map[string]string) map[string]string {
	result := map[string]string{}
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (p *Pair) RecoveryPath() string            { return p.owner.RecoveryPath() }
func (p *Pair) Close(ctx context.Context) error { return p.owner.Close(ctx) }

func (p *Pair) App(id string) (*AppView, error) {
	for _, selected := range p.owner.pair.apps {
		if selected.Info().ID == id {
			return &AppView{owner: p.owner, id: id}, nil
		}
	}
	return nil, errors.New("testkit App is outside the selected pair")
}

func (p *Pair) Initial() PairInspection {
	initial := p.owner.pair.initial
	initial.Apps = slices.Clone(initial.Apps)
	for i := range initial.Apps {
		initial.Apps[i].Steps = slices.Clone(initial.Apps[i].Steps)
	}
	return initial
}

func (p *Pair) Inspect(ctx context.Context) (_ PairInspection, err error) {
	p.owner.mu.Lock()
	defer p.owner.mu.Unlock()
	if p.owner.closed || p.owner.pair.failed {
		return PairInspection{}, errors.New("testkit pair is closed")
	}
	defer p.owner.closeOnPairFailure(&err)
	return p.owner.inspectPair(ctx)
}

func (a *App) closeOnPairFailure(err *error) {
	if *err == nil {
		return
	}
	a.pair.failed = true
	reason := StartupReasonSetup
	primary := startupError(reason, "testkit pair operation failed")
	var safe *StartupError
	if errors.As(*err, &safe) {
		primary = safe
		reason = StartupReason(safe.Reason())
	}
	if errors.Is(*err, context.DeadlineExceeded) {
		primary = errors.Join(primary, context.DeadlineExceeded)
	} else if errors.Is(*err, context.Canceled) {
		primary = errors.Join(primary, context.Canceled)
	}
	cleanup, stop := context.WithDeadline(context.Background(), a.deadline)
	defer stop()
	var cleanupError error
	if a.closeLocked(cleanup) != nil {
		cleanupError = errors.New("testkit pair cleanup incomplete")
	}
	*err = &OperationError{primary: primary, reason: reason, cleanup: cleanupError}
}

func (a *App) inspectPair(ctx context.Context) (PairInspection, error) {
	work, cancel := context.WithDeadline(ctx, a.workDeadline)
	defer cancel()
	ctx = work
	if err := ctx.Err(); err != nil {
		return PairInspection{}, err
	}
	status, err := a.service.Status(ctx)
	if err := ctx.Err(); err != nil {
		return PairInspection{}, err
	}
	if err != nil {
		return PairInspection{}, startupError(StartupReasonStorage, "testkit pair status unavailable")
	}
	if status.DockerError != "" {
		return PairInspection{}, startupError(StartupReasonDocker, "testkit pair container status unavailable")
	}
	result := PairInspection{Outcome: "prerequisite", Reason: "not-run", Link: a.pair.link, Apps: []PairAppStatus{}}
	for _, selected := range a.pair.apps {
		info := selected.Info()
		item := PairAppStatus{ID: info.ID, URL: service.AppURL(map[string]string{"EXPOSURE": "local"}, a.pair.ports[info.ID], info.Path), State: "missing", Setup: "pending", Steps: []PairStep{}}
		for _, observed := range status.Apps {
			if observed.ID != info.ID {
				continue
			}
			item.Installed = true
			item.Setup = string(observed.Setup)
			item.State = "unknown"
			if slices.Contains([]string{"running", "exited", "created", "restarting", "paused", "missing", "unknown", "dead", "removing"}, observed.State) {
				item.State = observed.State
			}
			for _, step := range observed.Steps {
				item.Steps = append(item.Steps, PairStep{ID: step.ID, Status: string(step.Status), Verified: step.Verified})
			}
		}
		result.Apps = append(result.Apps, item)
	}
	for _, item := range result.Apps {
		if !item.Installed {
			result.Reason = "absent"
		}
		if item.Setup == "pending" || item.Setup == "manual" || item.Setup == "incomplete" {
			if result.Reason != "absent" {
				result.Reason = "unready"
			}
		}
		for _, step := range item.Steps {
			if step.Status == "fail" {
				result.Outcome = "failed"
				result.Reason = "setup"
				return result, nil
			}
		}
	}
	for _, item := range result.Apps {
		if item.Setup == "manual" {
			result.Outcome = "manual"
			result.Reason = "setup"
			return result, nil
		}
	}
	for _, step := range result.Apps[0].Steps {
		if step.ID == result.Link && step.linked() && result.Apps[0].Installed && result.Apps[1].Installed && result.Apps[0].Setup == "ok" && result.Apps[1].Setup == "ok" {
			result.Outcome = "complete"
			result.Reason = "verified"
		}
	}
	return result, nil
}

func (p *Pair) Setup(ctx context.Context, id string) (PairInspection, error) {
	return p.action(ctx, "setup", id)
}
func (p *Pair) Remove(ctx context.Context, id string) (PairInspection, error) {
	return p.action(ctx, "remove", id)
}
func (p *Pair) Install(ctx context.Context, id string) (PairInspection, error) {
	return p.action(ctx, "install", id)
}

func (p *Pair) action(ctx context.Context, action, id string) (_ PairInspection, err error) {
	a := p.owner
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.pair.failed {
		return PairInspection{}, errors.New("testkit pair is closed")
	}
	if _, err := p.App(id); err != nil && (action != "setup" || id != "") {
		return PairInspection{}, err
	}
	defer a.closeOnPairFailure(&err)
	work, cancel := context.WithDeadline(ctx, a.workDeadline)
	defer cancel()
	if err := work.Err(); err != nil {
		return PairInspection{}, err
	}
	switch action {
	case "setup":
		ids := []string{id}
		if id == "" {
			ids = []string{a.pair.options.Consumer, a.pair.options.Target}
		}
		err = a.service.Setup(work, ids)
	case "remove":
		err = a.service.Remove(work, []string{id}, false)
	case "install":
		opts := a.pair.install
		opts.Targets = []string{id}
		err = a.service.Install(work, opts)
	}
	if err := work.Err(); err != nil {
		return PairInspection{}, err
	}
	if err != nil && !errors.Is(err, service.ErrManual) && !errors.Is(err, service.ErrIncomplete) {
		return PairInspection{}, err
	}
	if err := a.guard.verify(work); err != nil {
		return PairInspection{}, err
	}
	return a.inspectPair(work)
}

func (v *AppView) URL() string {
	for _, selected := range v.owner.pair.apps {
		if selected.Info().ID == v.id {
			return service.AppURL(map[string]string{"EXPOSURE": "local"}, v.owner.pair.ports[v.id], selected.Info().Path)
		}
	}
	return ""
}

func (a *App) pairAppPresent(ctx context.Context, id string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ids, err := a.guard.p.AppIDs()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err != nil {
		return false, startupError(StartupReasonStorage, "testkit selected App storage unavailable")
	}
	return slices.Contains(ids, id), nil
}

func (a *App) pairCredentials(ctx context.Context, id string) (_ service.Credentials, present bool, err error) {
	defer a.closeOnPairFailure(&err)
	work, cancel := context.WithDeadline(ctx, a.workDeadline)
	defer cancel()
	present, err = a.pairAppPresent(work, id)
	if err != nil || !present {
		return service.Credentials{}, present, err
	}
	creds, err := a.service.Credentials(work)
	if err := work.Err(); err != nil {
		return service.Credentials{}, false, err
	}
	if err != nil {
		return service.Credentials{}, false, startupError(StartupReasonStorage, "testkit private credentials unavailable")
	}
	return creds, true, nil
}

func (v *AppView) GetAPIKey(ctx context.Context, name string) (string, error) {
	return v.owner.apiKeyFor(ctx, v.id, name)
}

func (v *AppView) Credentials(ctx context.Context) (Credentials, error) {
	return v.owner.credentialsFor(ctx, v.id)
}

func (v *AppView) Info(ctx context.Context) (Info, error) {
	a := v.owner
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.pair.failed {
		return Info{}, errors.New("testkit pair is closed")
	}
	info, present, err := v.info(ctx)
	if err != nil {
		return Info{}, err
	}
	if !present {
		return Info{}, errors.New("testkit selected App is absent")
	}
	return info, nil
}

func (v *AppView) info(ctx context.Context) (_ Info, present bool, err error) {
	a := v.owner
	defer a.closeOnPairFailure(&err)
	work, cancel := context.WithDeadline(ctx, a.workDeadline)
	defer cancel()
	ctx = work
	present, err = a.pairAppPresent(ctx, v.id)
	if err != nil || !present {
		return Info{}, present, err
	}
	if err := a.guard.verify(ctx); err != nil {
		return Info{}, true, err
	}
	for _, selected := range a.pair.apps {
		if selected.Info().ID != v.id {
			continue
		}
		container := a.guard.containers[selected.Info().Service]
		if container.ID == "" {
			return Info{}, true, startupError(StartupReasonContainer, "testkit selected App container is absent")
		}
		data, err := docker(ctx, a.runner, "image", "inspect", "--format", `{{json .RepoDigests}}`, container.Image)
		if err != nil {
			return Info{}, true, err
		}
		info := Info{ID: v.id, URL: v.URL(), Image: container.ConfiguredImage, ImageID: container.Image}
		if err := decodeDigests(data, &info.RepoDigests); err != nil {
			return Info{}, true, err
		}
		return info, true, nil
	}
	return Info{}, true, errors.New("testkit selected App is unavailable")
}
