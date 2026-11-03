package catalog

import (
	"github.com/ajnart/self-host/internal/apps/actualbudget"
	"github.com/ajnart/self-host/internal/apps/anchor"
	"github.com/ajnart/self-host/internal/apps/aria2"
	"github.com/ajnart/self-host/internal/apps/autobrr"
	"github.com/ajnart/self-host/internal/apps/beszel"
	"github.com/ajnart/self-host/internal/apps/cleanuparr"
	"github.com/ajnart/self-host/internal/apps/deluge"
	"github.com/ajnart/self-host/internal/apps/jackett"
	"github.com/ajnart/self-host/internal/apps/nginxproxymanager"
	"github.com/ajnart/self-host/internal/apps/oxynote"
	"github.com/ajnart/self-host/internal/apps/pihole"
	"github.com/ajnart/self-host/internal/apps/plex"
	"github.com/ajnart/self-host/internal/apps/spoolman"
	"github.com/ajnart/self-host/internal/apps/tailscale"
	"github.com/ajnart/self-host/internal/apps/tandoor"
	"github.com/ajnart/self-host/internal/apps/technitiumdns"
	"github.com/ajnart/self-host/internal/apps/xteve"
	"github.com/ajnart/self-host/internal/apps/zerotier"
)

func init() {
	register(
		actualbudget.App{},
		anchor.App{},
		aria2.App{},
		autobrr.App{},
		beszel.App{},
		cleanuparr.App{},
		deluge.App{},
		jackett.App{},
		nginxproxymanager.App{},
		oxynote.App{},
		pihole.App{},
		plex.App{},
		spoolman.App{},
		tailscale.App{},
		tandoor.App{},
		technitiumdns.App{},
		xteve.App{},
		zerotier.App{},
	)
}
