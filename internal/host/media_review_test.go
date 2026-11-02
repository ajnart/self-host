package host

import (
	"context"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

func TestMediaUIDAllocationUsesHostIdentitySources(t *testing.T) {
	deps := systemtest.NewDeps(t)
	runner := deps.Runner.(*systemtest.Runner)
	runner.On("getent passwd", system.Result{ExitCode: 2})
	runner.On("getent passwd 0", system.Result{Stdout: []byte("root:x:0:0:root:/root:/bin/sh\n")})
	runner.On("getent passwd 10002", system.Result{Stdout: []byte("directory-user:x:10002:10000:Directory account:/home/directory-user:/bin/sh\n")})
	uid, err := UnusedMediaUID(context.Background(), deps, 10001, map[int]bool{10001: true})
	if err != nil || uid != 10003 {
		t.Fatalf("directory or reserved account was reused: %d, %v", uid, err)
	}
	if got := strings.Join(runner.Calls(), "\n"); got != "getent passwd 0\ngetent passwd 10002\ngetent passwd 10003" {
		t.Fatalf("unexpected identity probes: %s", got)
	}
}

func TestMediaUIDAllocationRefusesUnusableIdentitySources(t *testing.T) {
	for _, test := range []struct {
		name   string
		result system.Result
	}{
		{"missing getent", system.Result{ExitCode: 127}},
		{"unknown database", system.Result{ExitCode: 1}},
		{"unsupported lookup", system.Result{ExitCode: 3}},
		{"empty success", system.Result{}},
		{"wrong identity", system.Result{Stdout: []byte("other:x:20000:20000:Other:/home/other:/bin/sh\n")}},
		{"lookup diagnostics", system.Result{ExitCode: 2, Stderr: []byte("identity provider unavailable")}},
		{"unexpected missing output", system.Result{ExitCode: 2, Stdout: []byte("unexpected")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			deps := systemtest.NewDeps(t)
			runner := deps.Runner.(*systemtest.Runner)
			runner.On("getent passwd 0", system.Result{Stdout: []byte("root:x:0:0:root:/root:/bin/sh\n")})
			runner.On("getent passwd 10001", test.result)
			if uid, err := UnusedMediaUID(context.Background(), deps, 10001, nil); err == nil || uid != 0 {
				t.Fatalf("unusable identity source allocated %d: %v", uid, err)
			}
		})
	}
	deps := systemtest.NewDeps(t)
	deps.Runner.(*systemtest.Runner).On("getent passwd", system.Result{ExitCode: 2})
	if _, err := UnusedMediaUID(context.Background(), deps, 10001, nil); err == nil {
		t.Fatal("missing baseline identity must not establish a usable lookup")
	}
	deps.Runner = unavailableMediaLookup{Runner: deps.Runner}
	if _, err := UnusedMediaUID(context.Background(), deps, 10001, nil); err == nil || !strings.Contains(err.Error(), "working host identity lookup") {
		t.Fatalf("missing executable was not actionable: %v", err)
	}
}

func TestRestoredMediaUIDAvailabilityPreservesTheSelectedID(t *testing.T) {
	for _, test := range []struct {
		name   string
		result system.Result
		free   bool
	}{
		{"unassigned", system.Result{ExitCode: 2}, true},
		{"directory account", system.Result{Stdout: []byte("directory-user:x:10001:10000:Directory account:/home/directory-user:/bin/sh\n")}, false},
		{"unusable lookup", system.Result{ExitCode: 127}, false},
		{"malformed response", system.Result{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			deps := systemtest.NewDeps(t)
			runner := deps.Runner.(*systemtest.Runner)
			runner.On("getent passwd 0", system.Result{Stdout: []byte("root:x:0:0:root:/root:/bin/sh\n")})
			runner.On("getent passwd 10001", test.result)
			err := CheckUnusedMediaUID(context.Background(), deps, 10001)
			if (err == nil) != test.free {
				t.Fatalf("restored identity availability = %v, free = %t", err, test.free)
			}
			if got := strings.Join(runner.Calls(), "\n"); got != "getent passwd 0\ngetent passwd 10001" {
				t.Fatalf("availability check must not reallocate an ID: %s", got)
			}
		})
	}
	for _, uid := range []int{0, -1} {
		if err := CheckUnusedMediaUID(context.Background(), systemtest.NewDeps(t), uid); err == nil {
			t.Fatalf("invalid restored UID %d was accepted", uid)
		}
	}
}

type unavailableMediaLookup struct{ system.Runner }

func (unavailableMediaLookup) Run(context.Context, system.Cmd) (system.Result, error) {
	return system.Result{}, system.ErrNotFound
}
