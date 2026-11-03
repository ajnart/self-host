package homarr

import "github.com/ajnart/self-host/internal/helpers"

var overseerrFamily = seerrFamily{name: "Overseerr", kind: "overseerr", marker: "Overseerr API", partition: true}

func overseerrExternalTarget(c helpers.ExternalConnection) (target, error) {
	return overseerrFamily.externalTarget(c)
}
