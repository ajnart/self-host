package setupspec

import (
	"context"
	"fmt"

	"github.com/ajnart/self-host/internal/firstadmin"
	"github.com/ajnart/self-host/internal/helpers"
)

type noAccountKind struct{}

func (noAccountKind) validate(s Spec) error {
	if _, err := s.Probe.duration(); err != nil {
		return fmt.Errorf("probe timeout: %w", err)
	}
	return nil
}

func (noAccountKind) setup(s Spec) func(context.Context, *helpers.H) error {
	return s.noAccount().Setup
}

func (noAccountKind) check(s Spec) func(context.Context, *helpers.H) error {
	return s.noAccount().Check
}

func (s Spec) noAccount() firstadmin.NoAccount {
	return firstadmin.NoAccount{Probe: s.probe()}
}

func (s Spec) probe() firstadmin.Probe {
	timeout, _ := s.Probe.duration()
	return firstadmin.Probe{Path: s.Probe.Path, Contains: s.Probe.Contains, Want: s.Probe.Want, Timeout: timeout}
}
