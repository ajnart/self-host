package service

import (
	"github.com/ajnart/self-host/internal/host"
	"github.com/ajnart/self-host/internal/project"
)

func (s *Service) reservedPorts(p *project.Project, state *project.State, settings map[string]string, except string) ([]host.PortRequirement, error) {
	publications, err := p.PublishedPorts()
	if err != nil {
		return nil, err
	}
	var reserved []host.PortRequirement
	for _, publication := range publications {
		if publication.App != except {
			reserved = append(reserved, host.PortRequirement{App: publication.App, BindIP: publication.BindIP,
				Port: publication.Port, Protocols: []string{publication.Protocol}})
		}
	}
	if settings[settingNames] == namesOn {
		reserved = append(reserved, host.PortRequirement{Port: 80, Protocols: []string{"tcp"}})
	}
	for id, st := range state.Apps {
		if st == nil || id == except {
			continue
		}
		reserved = append(reserved, host.PortRequirement{App: id, BindIP: settings["BIND_IP"], Port: st.Port, Protocols: []string{"tcp"}})
		if a, found := s.Catalog.App(id); found {
			for _, input := range a.Inputs() {
				if len(input.HostPortProtocols) == 0 {
					continue
				}
				port, err := plannedPort(st.Inputs[input.Key], 0)
				if err != nil {
					return nil, err
				}
				reserved = append(reserved, host.PortRequirement{App: id, BindIP: settings["BIND_IP"], Port: port, Protocols: input.HostPortProtocols})
			}
		}
	}
	return reserved, nil
}
