package setupspec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/firstadmin"
	"github.com/ajnart/self-host/internal/helpers"
)

var loginDenied = []int{401, 403}

type firstAdminKind struct{}

func (firstAdminKind) validate(s Spec) error { return s.validateFirstAdmin() }

func (firstAdminKind) setup(s Spec) func(context.Context, *helpers.H) error {
	return func(ctx context.Context, h *helpers.H) error { return s.setupFirstAdmin(ctx, h) }
}

func (firstAdminKind) check(s Spec) func(context.Context, *helpers.H) error {
	return func(ctx context.Context, h *helpers.H) error { return s.checkFirstAdmin(ctx, h) }
}

// AdminEmail is the sign-in address the Email template makes for user.
func (s Spec) AdminEmail(user string) string {
	if user == "" && strings.Contains(s.Email, "{user}") {
		return ""
	}
	return strings.ReplaceAll(s.Email, "{user}", user)
}

func (s Spec) validateFirstAdmin() error {
	if _, err := s.Probe.duration(); err != nil {
		return fmt.Errorf("probe timeout: %w", err)
	}
	if _, err := s.createWait(); err != nil {
		return fmt.Errorf("create_wait: %w", err)
	}
	roles := []struct {
		name  string
		calls []Call
	}{{"exists", s.Exists}, {"create", s.Create}, {"login", s.Login}}
	for _, role := range roles {
		if err := calls(role.calls).validate(role.name); err != nil {
			return err
		}
	}
	creators := 0
	for _, set := range []bool{len(s.Create) > 0, s.CreateWait != "", s.CreateError != ""} {
		if set {
			creators++
		}
	}
	if creators > 1 {
		return errors.New("create, create_wait and create_error are mutually exclusive")
	}
	if (s.CreateWait != "") && len(s.Login) == 0 {
		return errors.New("create_wait needs login calls to poll")
	}
	return nil
}

func (s Spec) createWait() (time.Duration, error) {
	if s.CreateWait == "" {
		return 0, nil
	}
	return time.ParseDuration(s.CreateWait)
}

func (s Spec) scope(h *helpers.H) scope {
	vars := s.textScope(h)
	vars["password"] = h.AdminPassword()
	return vars
}

// EmailFor is AdminEmail for the Admin login, honouring EmailInput.
func (s Spec) EmailFor(h *helpers.H) string {
	if email := h.Inputs[s.EmailInput]; s.EmailInput != "" && email != "" {
		return email
	}
	return s.AdminEmail(h.AdminUser())
}

func (s Spec) textScope(h *helpers.H) scope {
	return scope{"user": h.AdminUser(), "email": s.EmailFor(h), "url": h.URL, "app": h.App}
}

func (s Spec) login(ctx context.Context, h *helpers.H) (bool, error) {
	return calls(s.Login).run(ctx, h, s.scope(h), loginDenied)
}

func (s Spec) setupFirstAdmin(ctx context.Context, h *helpers.H) error {
	f := s.firstAdmin(h)
	if !s.Probe.Web {
		return f.Setup(ctx, h)
	}
	var opts []helpers.ReadyOption
	if len(s.Probe.Want) > 0 {
		opts = append(opts, helpers.WantStatus(s.Probe.Want...))
	}
	if timeout, _ := s.Probe.duration(); timeout > 0 {
		opts = append(opts, helpers.Timeout(timeout))
	}
	if err := h.WebReady(ctx, s.Probe.Path, opts...); err != nil {
		return err
	}
	return f.Admin(ctx, h)
}

func (s Spec) firstAdmin(h *helpers.H) firstadmin.FirstAdmin {
	f := firstadmin.FirstAdmin{Ready: s.probe(), StepID: s.StepID, Manual: s.textScope(h).expand(s.Manual)}
	if len(s.Exists) > 0 {
		f.Exists = func(ctx context.Context, h *helpers.H) (bool, error) {
			return calls(s.Exists).run(ctx, h, s.scope(h), nil)
		}
	}
	if len(s.Login) > 0 {
		f.Login = func(ctx context.Context, h *helpers.H, _, _ string) (bool, error) { return s.login(ctx, h) }
	}
	f.Create = s.creator()
	if s.Title != "" {
		f.Title = func(h *helpers.H, _ string) string { return s.textScope(h).expand(s.Title) }
	}
	if s.Hint != "" {
		f.Hint = func(h *helpers.H, _ string) string { return s.textScope(h).expand(s.Hint) }
	}
	return f
}

func (s Spec) creator() func(context.Context, *helpers.H, string, string) error {
	switch {
	case len(s.Create) > 0:
		return func(ctx context.Context, h *helpers.H, _, _ string) error {
			ok, err := calls(s.Create).run(ctx, h, s.scope(h), nil)
			if err == nil && !ok {
				return errors.New("the App refused to create the first administrator")
			}
			return err
		}
	case s.CreateWait != "":
		wait, _ := s.createWait()
		return func(ctx context.Context, h *helpers.H, _, _ string) error {
			return h.Poll(ctx, wait, func(ctx context.Context) (bool, error) { return s.login(ctx, h) })
		}
	case s.CreateError != "":
		return func(context.Context, *helpers.H, string, string) error { return errors.New(s.CreateError) }
	}
	return nil
}

func (s Spec) checkFirstAdmin(ctx context.Context, h *helpers.H) error {
	if len(s.Login) == 0 {
		return fmt.Errorf("%w: this App has no headless way to verify its administrator login", helpers.ErrManual)
	}
	ok, err := s.login(ctx, h)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New(s.failure(h))
	}
	return nil
}

func (s Spec) failure(h *helpers.H) string {
	if s.CheckError != "" {
		return s.textScope(h).expand(s.CheckError)
	}
	vars := s.textScope(h)
	if s.Name != "" {
		vars["app"] = s.Name
	}
	return vars.expand("the Admin login {user} cannot sign in to {app}")
}
