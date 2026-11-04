package setupspec

import "github.com/ajnart/self-host/internal/firstadmin"

func (s Spec) noAccount() firstadmin.NoAccount {
	return firstadmin.NoAccount{Probe: s.probe()}
}

func (s Spec) probe() firstadmin.Probe {
	timeout, _ := s.Probe.duration()
	return firstadmin.Probe{Path: s.Probe.Path, Contains: s.Probe.Contains, Want: s.Probe.Want, Timeout: timeout}
}
