package project

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

type PublishedPort struct {
	App, BindIP, Protocol string
	Port                  int
}

func (p *Project) PublishedPorts() ([]PublishedPort, error) {
	root, err := p.readRoot()
	if err != nil {
		return nil, err
	}
	if err := p.consolidateRoot(root); err != nil {
		return nil, err
	}
	settings, err := p.LoadSettings()
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var doc struct {
		Services map[string]struct {
			Ports []yaml.Node `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := expandAliases(root).Decode(&doc); err != nil {
		return nil, errors.New("cannot inspect Compose publications")
	}
	var ports []PublishedPort
	for service, definition := range doc.Services {
		id := serviceOwner(root, service)
		if id == "" {
			id = service
		}
		for _, node := range definition.Ports {
			publications, err := publishedPorts(node, settings)
			if err != nil {
				return nil, fmt.Errorf("service %s: cannot reserve Compose publications: %w", service, err)
			}
			for _, publication := range publications {
				publication.App = id
				ports = append(ports, publication)
			}
		}
	}
	return ports, nil
}

func publishedPorts(node yaml.Node, settings map[string]string) ([]PublishedPort, error) {
	bind, published, protocol := "", "", "tcp"
	switch node.Kind {
	case yaml.ScalarNode:
		value := strings.ReplaceAll(node.Value, "${BIND_IP}", settings["BIND_IP"])
		if strings.Contains(value, "$") {
			return nil, errors.New("interpolated port requires explicit resolution before installing")
		}
		if mapping, suffix, ok := strings.Cut(value, "/"); ok {
			value, protocol = mapping, suffix
		}
		last := strings.LastIndex(value, ":")
		if last == -1 {
			return nil, nil
		}
		prefix := value[:last]
		if separator := strings.LastIndex(prefix, ":"); separator != -1 {
			bind, published = strings.Trim(prefix[:separator], "[]"), prefix[separator+1:]
		} else {
			published = prefix
		}
	case yaml.MappingNode:
		var mapping struct {
			Published string `yaml:"published"`
			HostIP    string `yaml:"host_ip"`
			Protocol  string `yaml:"protocol"`
		}
		if err := node.Decode(&mapping); err != nil {
			return nil, errors.New("invalid port mapping")
		}
		bind, published = strings.ReplaceAll(mapping.HostIP, "${BIND_IP}", settings["BIND_IP"]), mapping.Published
		if mapping.Protocol != "" {
			protocol = mapping.Protocol
		}
	default:
		return nil, errors.New("unsupported port mapping")
	}
	if published == "" || published == "0" {
		return nil, nil
	}
	if bind != "" && net.ParseIP(bind) == nil {
		return nil, errors.New("bind address requires explicit resolution before installing")
	}
	if protocol != "tcp" && protocol != "udp" && protocol != "sctp" {
		return nil, errors.New("unsupported port protocol")
	}
	start, end, ranged := strings.Cut(published, "-")
	first, err := strconv.Atoi(start)
	if err != nil || first < 1 || first > 65535 {
		return nil, errors.New("published port requires explicit resolution before installing")
	}
	last := first
	if ranged {
		last, err = strconv.Atoi(end)
		if err != nil || last < first || last > 65535 {
			return nil, errors.New("invalid published port range")
		}
	}
	var ports []PublishedPort
	for port := first; port <= last; port++ {
		ports = append(ports, PublishedPort{BindIP: bind, Port: port, Protocol: protocol})
	}
	return ports, nil
}
