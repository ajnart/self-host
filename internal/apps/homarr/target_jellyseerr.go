package homarr

import "github.com/ajnart/self-host/internal/helpers"

var jellyseerrFamily = seerrFamily{name: "Jellyseerr", kind: "jellyseerr", marker: "Jellyseerr API"}

func jellyseerrExternalTarget(c helpers.ExternalConnection) (target, error) {
	return jellyseerrFamily.externalTarget(c)
}
