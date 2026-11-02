package system

import (
	"os"
	"slices"
	"strings"
	"testing"
)

var _ Runner = (*ExecRunner)(nil)
var _ StreamIORunner = (*ExecRunner)(nil)

func TestExecRunnerEnvironmentIsImmutableAndFixtureLocal(t *testing.T) {
	t.Setenv("SELFHOST_RUNNER_TEST_AMBIENT", "ambient")
	environment := []string{"SELFHOST_RUNNER_TEST_VALUE=first"}
	first := NewExecRunner(environment)
	second := NewExecRunner([]string{"SELFHOST_RUNNER_TEST_VALUE=second"})
	environment[0] = "SELFHOST_RUNNER_TEST_VALUE=mutated"
	exposed := first.Environment()
	exposed[0] = "SELFHOST_RUNNER_TEST_VALUE=exposed"
	for _, tc := range []struct {
		name   string
		runner *ExecRunner
		value  string
	}{
		{name: "first", runner: first, value: "first"},
		{name: "second", runner: second, value: "second"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := tc.runner.command(t.Context(), Cmd{Name: os.Args[0]})
			values := command.Environ()
			if !slices.Contains(values, "SELFHOST_RUNNER_TEST_VALUE="+tc.value) || slices.Contains(values, "SELFHOST_RUNNER_TEST_AMBIENT=ambient") {
				t.Fatal("runner environment escaped its immutable snapshot")
			}
		})
	}
	overridden := first.command(t.Context(), Cmd{Name: os.Args[0], Env: []string{"SELFHOST_RUNNER_TEST_VALUE=override"}})
	if !slices.Contains(overridden.Environ(), "SELFHOST_RUNNER_TEST_VALUE=override") || slices.Contains(overridden.Environ(), "SELFHOST_RUNNER_TEST_VALUE=first") {
		t.Fatal("command overrides no longer take precedence")
	}
	if !slices.Equal(first.Environment(), []string{"SELFHOST_RUNNER_TEST_VALUE=first"}) {
		t.Fatal("command overrides mutated the runner snapshot")
	}
}

func TestExecRunnerNilEnvironmentInheritsButEmptyDoesNot(t *testing.T) {
	t.Setenv("SELFHOST_RUNNER_TEST_PARENT", "initial")
	inherit := NewExecRunner(nil)
	empty := NewExecRunner([]string{})
	t.Setenv("SELFHOST_RUNNER_TEST_PARENT", "current")
	command := inherit.command(t.Context(), Cmd{Name: os.Args[0]})
	if command.Env != nil || !slices.Contains(command.Environ(), "SELFHOST_RUNNER_TEST_PARENT=current") || inherit.Environment() != nil {
		t.Fatal("nil environment no longer preserves ordinary inheritance")
	}
	command = inherit.command(t.Context(), Cmd{Name: os.Args[0], Env: []string{"SELFHOST_RUNNER_TEST_PARENT=override", "SELFHOST_RUNNER_TEST_EXTRA=extra"}})
	if !slices.Contains(command.Environ(), "SELFHOST_RUNNER_TEST_PARENT=override") || !slices.Contains(command.Environ(), "SELFHOST_RUNNER_TEST_EXTRA=extra") || slices.Contains(command.Environ(), "SELFHOST_RUNNER_TEST_PARENT=current") {
		t.Fatal("inherited command overrides changed")
	}
	command = empty.command(t.Context(), Cmd{Name: os.Args[0]})
	if command.Env == nil || len(command.Environ()) != 0 || empty.Environment() == nil {
		t.Fatal("explicit empty environment inherited ambient values")
	}
	for _, value := range empty.command(t.Context(), Cmd{Name: os.Args[0], Env: []string{"SELFHOST_RUNNER_TEST_EXTRA=extra"}}).Environ() {
		if !strings.HasPrefix(value, "SELFHOST_RUNNER_TEST_EXTRA=") {
			t.Fatal("empty environment command overrides inherited ambient values")
		}
	}
}
