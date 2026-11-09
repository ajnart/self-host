package testkit

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ajnart/self-host/internal/project"
	"golang.org/x/sys/unix"
)

type portLease struct {
	sockets  map[project.FixturePort]io.Closer
	claims   map[project.FixturePort]*os.File
	released bool
}

// Claims bridge the socket-to-Docker handoff across separate helper processes.
// Files must not be unlinked: doing so could create a second lock inode.
func claimPort(port project.FixturePort) (*os.File, error) {
	if port.Port < 1 || port.Port > 65535 || (port.Protocol != "tcp" && port.Protocol != "udp") {
		return nil, startupError(StartupReasonPort, "unsupported fixture publication")
	}
	path := filepath.Join(os.TempDir(), "selfhost-testkit-port-"+port.Protocol+"-"+strconv.Itoa(port.Port)+".lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, startupError(StartupReasonPort, "testkit port claim unavailable")
	}
	file := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Uid != uint32(os.Geteuid()) || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o077 != 0 {
		file.Close()
		return nil, startupError(StartupReasonPort, "testkit port claim is not private")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, startupError(StartupReasonPort, "testkit publication is already claimed")
	}
	return file, nil
}

func (p *portLease) reserve(ports []project.FixturePort) (err error) {
	if p.sockets == nil {
		p.sockets = map[project.FixturePort]io.Closer{}
	}
	if p.claims == nil {
		p.claims = map[project.FixturePort]*os.File{}
	}
	var added []project.FixturePort
	defer func() {
		if err != nil {
			for _, port := range added {
				p.sockets[port].Close()
				p.claims[port].Close()
				delete(p.sockets, port)
				delete(p.claims, port)
			}
		}
	}()
	for _, port := range ports {
		if _, ok := p.claims[port]; ok {
			continue
		}
		if p.released {
			return startupError(StartupReasonPort, "testkit refuses publications added after startup")
		}
		claim, err := claimPort(port)
		if err != nil {
			return err
		}
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port.Port))
		var socket io.Closer
		switch port.Protocol {
		case "tcp":
			socket, err = net.Listen("tcp4", address)
		case "udp":
			socket, err = net.ListenPacket("udp4", address)
		}
		if err != nil {
			claim.Close()
			return startupError(StartupReasonPort, "testkit publication is already occupied")
		}
		p.claims[port] = claim
		p.sockets[port] = socket
		added = append(added, port)
	}
	return nil
}

func (p *portLease) random(protocols ...string) (int, error) {
	for range 100 {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return 0, startupError(StartupReasonPort, "testkit port allocation failed")
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
	return 0, startupError(StartupReasonPort, "testkit could not reserve publications")
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

// release is only for confirmed cleanup; handoff alone must keep claims alive.
func (p *portLease) release() {
	p.handoff()
	for port, claim := range p.claims {
		claim.Close()
		delete(p.claims, port)
		delete(p.sockets, port)
	}
}
