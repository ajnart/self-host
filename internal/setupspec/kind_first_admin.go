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
	for role, list := range map[string][]Call{"exists": s.Exists, "create": s.Create, "login": s.Login} {
		if err := calls(list).validate(role); err != nil {
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

func (s Spec) textScope(h *helpers.H) scope {
	return scope{"user": h.AdminUser(), "email": s.AdminEmail(h.AdminUser()), "url": h.URL, "app": h.App}
}

func (s Spec) login(ctx context.Context, h *helpers.H) (bool, error) {
	return calls(s.Login).run(ctx, h, s.scope(h), loginDenied)
}

func (s Spec) firstAdmin() firstadmin.FirstAdmin {
	f := firstadmin.FirstAdmin{Ready: s.probe(), StepID: s.StepID, Manual: s.Manual}
	if len(s.Exists) > 0 {
		f.Exists = func(ctx context.Context, h *helpers.H) (bool, error) {
			return calls(s.Exists).run(ctx, h, s.scope(h), nil)
		}
	}
	if len(s.Login) > 0 {
		f.Login = func(ctx context.Context, h *helpers.H, _, _ string) (bool, error) { return s.login(ctx, h) }
	}
	f.Create = s.creator()
	if s.MinPassword > 0 {
		f.Valid = func(_, password string) error {
			if len(password) < s.MinPassword {
				return fmt.Errorf("the Admin password must be at least %d characters", s.MinPassword)
			}
			return nil
		}
	}
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
		return fmt.Errorf("the Admin login %s cannot sign in to %s", h.AdminUser(), s.displayName(h))
	}
	return nil
}

func (s Spec) displayName(h *helpers.H) string {
	if s.Name == "" {
		return h.App
	}
	return s.Name
}
