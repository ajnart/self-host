package testkit

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ajnart/self-host/internal/compose"
	"github.com/ajnart/self-host/internal/project"
	"github.com/ajnart/self-host/internal/system"
	"golang.org/x/sys/unix"
)

func startupLock(ctx context.Context) (func(), error) {
	path := filepath.Join(os.TempDir(), "selfhost-e2e-global.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, startupError("LOCK", "testkit startup lock unavailable")
	}
	f := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Uid != uint32(os.Getuid()) || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o077 != 0 {
		f.Close()
		return nil, startupError("LOCK", "testkit startup lock is not private")
	}
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			var once sync.Once
			return func() { once.Do(func() { f.Close() }) }, nil
		}
		if err != unix.EAGAIN && err != unix.EWOULDBLOCK {
			f.Close()
			return nil, startupError("LOCK", "testkit startup lock failed")
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type guardedRunner struct {
	base     system.Runner
	p        *project.Project
	policy   project.FixturePolicy
	ports    *portLease
	prepared bool
	plan     project.FixtureCompose
	main     containerInfo
}

func composeVerb(args []string) (string, []string) {
	if len(args) >= 2 && args[0] == "--progress" {
		args = args[2:]
	}
	if len(args) == 0 {
		return "", nil
	}
	return args[0], args[1:]
}

type fixturePreparer struct {
	runner *guardedRunner
}

func (r *guardedRunner) ComposePreparation() compose.Preparer {
	return fixturePreparer{runner: r}
}

func (p fixturePreparer) PrepareCompose(ctx context.Context, root, name string, args []string) (func() error, error) {
	r := p.runner
	if root != r.p.Root || name != r.p.ComposeName {
		return nil, startupError("OWNERSHIP", "testkit refuses another Project")
	}
	verb, rest := composeVerb(args)
	if slices.Contains([]string{"run", "create", "start", "restart", "up"}, verb) {
		if verb == "run" {
			return nil, startupError("UNSUPPORTED", "testkit refuses unbounded one-off containers")
		}
		for _, arg := range rest {
			if strings.HasPrefix(arg, "-") && !slices.Contains([]string{"-d", "--remove-orphans"}, arg) {
				return nil, startupError("UNSUPPORTED", "testkit refuses creation overrides")
			}
		}
	}
	if !slices.Contains([]string{"up", "create", "start", "restart", "pull"}, verb) {
		return func() error { return nil }, nil
	}
	plan, err := r.p.PrepareFixtureCompose(r.policy)
	if err != nil {
		return nil, startupError("UNSUPPORTED", "testkit App Compose configuration is unsupported or unavailable")
	}
	for _, arg := range rest {
		if !strings.HasPrefix(arg, "-") && !slices.Contains(plan.Services, arg) {
			return nil, startupError("OWNERSHIP", "testkit service outside owned Compose")
		}
	}
	r.plan = plan
	if verb == "pull" {
		return func() error { return nil }, nil
	}
	if err := r.ports.reserve(plan.Ports); err != nil {
		return nil, err
	}
	r.ports.handoff()
	r.prepared = true
	return func() error {
		r.prepared = false
		return r.verify(ctx)
	}, nil
}

func (r *guardedRunner) validate(cmd system.Cmd) error {
	for _, entry := range cmd.Env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "COMPOSE_") || strings.HasPrefix(key, "DOCKER_") || strings.HasPrefix(key, "SELF_HOST_") {
			return startupError("UNSUPPORTED", "testkit refuses command scope overrides")
		}
	}
	if cmd.Name != "docker" || len(cmd.Args) == 0 {
		return startupError("UNSUPPORTED", "testkit refuses external command")
	}
	args := cmd.Args
	if args[0] == "compose" {
		if slices.Equal(args, []string{"compose", "version", "--short"}) {
			return nil
		}
		if len(args) < 6 || args[1] != "--project-directory" || args[2] != r.p.Root || args[3] != "-p" || args[4] != r.p.ComposeName {
			return startupError("OWNERSHIP", "testkit refuses unscoped Compose")
		}
		verb, _ := composeVerb(args[5:])
		if slices.Contains([]string{"up", "create", "start", "restart"}, verb) {
			if !r.prepared {
				return startupError("UNSUPPORTED", "testkit creation requires canonical preparation")
			}
			return nil
		}
		if slices.Contains([]string{"pull", "ps", "down", "stop", "exec", "logs", "version", "config"}, verb) {
			return nil
		}
		return startupError("UNSUPPORTED", "testkit refuses unsupported Compose command")
	}
	if slices.Contains([]string{"info", "version"}, args[0]) || len(args) >= 2 && args[0] == "context" && args[1] == "inspect" {
		return nil
	}
	return startupError("UNSUPPORTED", "testkit refuses direct Docker helper or unscoped command")
}

func (r *guardedRunner) Run(ctx context.Context, cmd system.Cmd) (system.Result, error) {
	if err := r.validate(cmd); err != nil {
		return system.Result{}, err
	}
	return r.base.Run(ctx, cmd)
}

func (r *guardedRunner) Stream(ctx context.Context, cmd system.Cmd, w io.Writer) error {
	if err := r.validate(cmd); err != nil {
		return err
	}
	return r.base.Stream(ctx, cmd, w)
}
