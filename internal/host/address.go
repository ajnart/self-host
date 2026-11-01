package host

import (
	"net/netip"
	"strings"
)

// AddressScope describes an address, not whether a firewall permits access to it.
type AddressScope string

const (
	AddressUnknown   AddressScope = "unknown"
	AddressLocal     AddressScope = "local"
	AddressPrivate   AddressScope = "private"
	AddressShared    AddressScope = "shared"
	AddressLinkLocal AddressScope = "link-local"
	AddressPublic    AddressScope = "public"
)

var sharedAddressRange = netip.MustParsePrefix("100.64.0.0/10")

// ClassifyAddress keeps private LANs distinct from loopback, shared/VPN space
// and public host addresses. It never substitutes another network interface.
func ClassifyAddress(value string) AddressScope {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return AddressUnknown
	}
	ip = ip.Unmap()
	switch {
	case ip.IsLoopback():
		return AddressLocal
	case sharedAddressRange.Contains(ip):
		return AddressShared
	case ip.IsPrivate():
		return AddressPrivate
	case ip.IsLinkLocalUnicast():
		return AddressLinkLocal
	case ip.IsGlobalUnicast():
		return AddressPublic
	default:
		return AddressUnknown
	}
}

// Label is the short description shown beside a Host address.
func (scope AddressScope) Label() string {
	switch scope {
	case AddressLocal:
		return "LOCAL"
	case AddressPrivate:
		return "LAN"
	case AddressShared:
		return "SHARED/VPN"
	case AddressLinkLocal:
		return "LINK-LOCAL"
	case AddressPublic:
		return "PUBLIC IP"
	default:
		return "HOST IP"
	}
}
