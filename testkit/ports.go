package testkit

import (
	"io"
	"net"
	"strconv"

	"github.com/ajnart/self-host/internal/project"
)

type portLease struct {
	sockets  map[project.FixturePort]io.Closer
	released bool
}

func (p *portLease) reserve(ports []project.FixturePort) error {
	if p.sockets == nil {
		p.sockets = map[project.FixturePort]io.Closer{}
	}
	for _, port := range ports {
		if _, ok := p.sockets[port]; ok {
			continue
		}
		if p.released {
			return startupError("PORT", "testkit refuses publications added after startup")
		}
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port.Port))
		var socket io.Closer
		var err error
		switch port.Protocol {
		case "tcp":
			socket, err = net.Listen("tcp4", address)
		case "udp":
			socket, err = net.ListenPacket("udp4", address)
		default:
			return startupError("PORT", "unsupported fixture protocol")
		}
		if err != nil {
			return startupError("PORT", "testkit publication is already occupied")
		}
		p.sockets[port] = socket
	}
	return nil
}

func (p *portLease) random(protocols ...string) (int, error) {
	for range 100 {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return 0, startupError("PORT", "testkit port allocation failed")
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		var publications []project.FixturePort
		for _, protocol := range protocols {
			publications = append(publications, project.FixturePort{Port: port, Protocol: protocol})
		}
		if err := p.reserve(publications); err == nil {
			return port, nil
		}
	}
	return 0, startupError("PORT", "testkit could not reserve publications")
}

func (p *portLease) handoff() {
	for port, socket := range p.sockets {
		if socket != nil {
			socket.Close()
			p.sockets[port] = nil
		}
	}
	p.released = true
}
