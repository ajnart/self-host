// Package setupspec turns the declarative `setup:` block of an App spec into
// Setup and Check. Each kind lives in its own kind_<name>.go file.
package setupspec

import (
	"context"
	"fmt"

	"github.com/ajnart/self-host/internal/firstadmin"
	"github.com/ajnart/self-host/internal/helpers"
)

const KindNoAccount = "no-account"

type Probe struct {
	Path     string `yaml:"path"`
	Contains string `yaml:"contains,omitempty"`
}

type Spec struct {
	Kind  string `yaml:"kind"`
	Probe Probe  `yaml:"probe,omitempty"`
}

func (s Spec) Validate() error {
	switch s.Kind {
	case KindNoAccount:
		return nil
	}
	return fmt.Errorf("unknown setup kind %q", s.Kind)
}

func (s Spec) Setup(ctx context.Context, h *helpers.H) error {
	switch s.Kind {
	case KindNoAccount:
		return s.noAccount().Setup(ctx, h)
	}
	return fmt.Errorf("unknown setup kind %q", s.Kind)
}

func (s Spec) Check(ctx context.Context, h *helpers.H) error {
	switch s.Kind {
	case KindNoAccount:
		return s.noAccount().Check(ctx, h)
	}
	return fmt.Errorf("unknown setup kind %q", s.Kind)
}

func (s Spec) noAccount() firstadmin.NoAccount {
	return firstadmin.NoAccount{Probe: firstadmin.Probe{Path: s.Probe.Path, Contains: s.Probe.Contains}}
}
