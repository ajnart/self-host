package setupspec

import (
	"errors"
	"fmt"

	"github.com/ajnart/self-host/internal/firstadmin"
)

func (s Spec) validateNoAccount() error {
	if _, err := s.Probe.duration(); err != nil {
		return fmt.Errorf("probe timeout: %w", err)
	}
	if s.Probe.Web {
		return errors.New("probe.web belongs to the first-admin kind")
	}
	if s.Name != "" || s.Email != "" || s.EmailInput != "" || len(s.Exists)+len(s.Create)+len(s.Login) > 0 ||
		s.CreateWait != "" || s.CreateError != "" || s.CheckError != "" || s.StepID != "" || s.Title != "" || s.Hint != "" || s.Manual != "" {
		return errors.New("the no-account kind reads only probe")
	}
	return nil
}

func (s Spec) noAccount() firstadmin.NoAccount {
	return firstadmin.NoAccount{Probe: s.probe()}
}

func (s Spec) probe() firstadmin.Probe {
	timeout, _ := s.Probe.duration()
	return firstadmin.Probe{Path: s.Probe.Path, Contains: s.Probe.Contains, Want: s.Probe.Want, Timeout: timeout, Live: s.Probe.Live}
}
