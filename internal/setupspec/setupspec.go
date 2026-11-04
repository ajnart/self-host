// Package setupspec turns the declarative `setup:` block of an App spec into
// Setup and Check. Each kind lives in its own kind_<name>.go file.
package setupspec

import (
	"context"
	"fmt"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

const (
	KindNoAccount  = "no-account"
	KindFirstAdmin = "first-admin"
)

type Probe struct {
	Path     string `yaml:"path"`
	Contains string `yaml:"contains,omitempty"`
	// Want lists the accepted statuses; empty accepts any 2xx.
	Want []int `yaml:"want,omitempty"`
	// Live accepts any status below 500 when Want is empty: the App answers.
	Live bool `yaml:"live,omitempty"`
	// Timeout is a duration such as 5m; empty waits two minutes.
	Timeout string `yaml:"timeout,omitempty"`
	// Web words the ready step "wait for the web server" and accepts any
	// status below 500 unless Want is set. Only first-admin reads it.
	Web bool `yaml:"web,omitempty"`
}

// Spec is the plain-data Setup of one App. Kind picks the engine and the
// other fields feed it; fields a kind does not read must stay empty.
type Spec struct {
	Kind  string `yaml:"kind"`
	Probe Probe  `yaml:"probe,omitempty"`

	// Name is how Check errors call the App; empty uses the App id.
	Name string `yaml:"name,omitempty"`
	// Email is the sign-in address template, e.g. {user}@selfhost.local.
	Email string `yaml:"email,omitempty"`
	// EmailInput names an App input whose value, when the person set it,
	// replaces the Email template.
	EmailInput string `yaml:"email_input,omitempty"`
	// Exists probes whether the first Admin already exists; Create makes it;
	// Login proves the Admin login signs in. Each is run in order and all
	// calls must pass.
	Exists []Call `yaml:"exists,omitempty"`
	Create []Call `yaml:"create,omitempty"`
	Login  []Call `yaml:"login,omitempty"`
	// CreateWait polls Login for this long, as a duration such as 5m, for an
	// App that creates its administrator from its own start-up settings.
	CreateWait string `yaml:"create_wait,omitempty"`
	// CreateError is the failure for an App that cannot create it at all.
	CreateError string `yaml:"create_error,omitempty"`
	// CheckError is what a failed Login says instead of the standard
	// "the Admin login {user} cannot sign in to {app}".
	CheckError string `yaml:"check_error,omitempty"`
	// StepID, Title, Hint and Manual word the administrator step; Title,
	// Hint and Manual may use {user}, {email}, {url} and {app}.
	StepID string `yaml:"step_id,omitempty"`
	Title  string `yaml:"title,omitempty"`
	Hint   string `yaml:"hint,omitempty"`
	Manual string `yaml:"manual,omitempty"`
}

func (s Spec) Validate() error {
	switch s.Kind {
	case KindNoAccount:
		return s.validateNoAccount()
	case KindFirstAdmin:
		return s.validateFirstAdmin()
	}
	return fmt.Errorf("unknown setup kind %q", s.Kind)
}

// Setup and Check validate first so a Go App's spec is rejected on the same
// terms as an embedded spec file.
func (s Spec) Setup(ctx context.Context, h *helpers.H) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Kind == KindNoAccount {
		return s.noAccount().Setup(ctx, h)
	}
	return s.setupFirstAdmin(ctx, h)
}

func (s Spec) Check(ctx context.Context, h *helpers.H) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Kind == KindNoAccount {
		return s.noAccount().Check(ctx, h)
	}
	return s.checkFirstAdmin(ctx, h)
}

func (p Probe) duration() (time.Duration, error) {
	if p.Timeout == "" {
		return 0, nil
	}
	return time.ParseDuration(p.Timeout)
}
