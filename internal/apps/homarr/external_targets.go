package homarr

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

func externalTarget(c helpers.ExternalConnection) (target, error) {
	switch c.Kind {
	case "proxmox":
		return proxmoxExternalTarget(c)
	case "openmediavault":
		return openmediavaultExternalTarget(c)
	}
	return target{}, errors.New("unsupported external integration kind; use a supported exact Homarr kind")
}

func externalHTTPSOrigin(endpoint, label string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(endpoint, "\r\n\t\\ #") {
		return errors.New(label + " requires an approved trusted-HTTPS origin without embedded credentials, API/proxy paths, query or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("the private " + label + " endpoint has an invalid port")
		}
	}
	return nil
}
