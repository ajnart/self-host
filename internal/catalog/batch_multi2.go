package catalog

import (
	"github.com/ajnart/self-host/internal/apps/mayanedms"
	"github.com/ajnart/self-host/internal/apps/miniflux"
	"github.com/ajnart/self-host/internal/apps/nextcloud"
	"github.com/ajnart/self-host/internal/apps/paperlessngx"
	"github.com/ajnart/self-host/internal/apps/patchmon"
	"github.com/ajnart/self-host/internal/apps/seafile"
	"github.com/ajnart/self-host/internal/apps/stash"
	"github.com/ajnart/self-host/internal/apps/thingport"
	"github.com/ajnart/self-host/internal/apps/tracearr"
	"github.com/ajnart/self-host/internal/apps/tubearchivist"
	"github.com/ajnart/self-host/internal/apps/umami"
)

func init() {
	register(
		mayanedms.App{},
		miniflux.App{},
		nextcloud.App{},
		paperlessngx.App{},
		patchmon.App{},
		seafile.App{},
		stash.App{},
		tracearr.App{},
		thingport.App{},
		tubearchivist.App{},
		umami.App{},
	)
}
