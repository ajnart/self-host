// Package audiobookshelf is Audiobookshelf. Traps: docs/catalog/traps/audiobookshelf.md.
package audiobookshelf

import (
	"context"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/mediastorage"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/setupspec"
)

// App is Audiobookshelf. Traps: docs/catalog/traps/audiobookshelf.md.
type App struct{}

func (App) Info() app.Info {
	return app.Info{
		ID: "audiobookshelf", Name: "Audiobookshelf", Category: "Media",
		Description:   "Listen to your audiobooks and podcasts on your phone or in a browser",
		Image:         "ghcr.io/advplyr/audiobookshelf:latest",
		Docs:          "https://www.audiobookshelf.org/docs",
		Port:          13378,
		ContainerPort: 80,
		Service:       "audiobookshelf",
		UsesMedia:     true,
	}
}

func (App) Inputs() []app.Input { return nil }
func (App) Links() []app.Link   { return nil }

func (App) StandaloneGuide() string {
	return "Before the first start, create ${DATA_PATH}/media and any missing audiobook or podcast subfolders with your Host user's ownership so Docker does not create the media parent as root. The shared media folder is mounted at /data/media. After starting, create the root account on the first visit, then select the media subfolders in Settings > Libraries. Review existing libraries before adding any, without replacing their folders or importing automatically."
}

func (App) Compose(rc app.RenderContext) string {
	port := strconv.Itoa(rc.Port)
	return `services:
  audiobookshelf:
    image: ghcr.io/advplyr/audiobookshelf:latest
    restart: unless-stopped
    environment:
      TZ: ${TZ}
    volumes:
      - ./config:/config
      - ./metadata:/metadata
      - ${MEDIA_ROOT}:` + mediastorage.Media(rc.Settings) + `
    ports:
      - ${BIND_IP}:` + port + `:80
`
}

var setup = setupspec.Spec{
	Kind:  setupspec.KindFirstAdmin,
	Name:  "Audiobookshelf",
	Probe: setupspec.Probe{Path: "/status"},
	Exists: []setupspec.Call{{
		Path:   "/status",
		Assert: []setupspec.Assert{{Path: "isInit", Equals: "true"}},
	}},
	Create: []setupspec.Call{{
		Path: "/init",
		JSON: map[string]any{"newRoot": map[string]any{"username": "{user}", "password": "{password}"}},
	}},
	Login: []setupspec.Call{{
		Path: "/login",
		JSON: map[string]any{"username": "{user}", "password": "{password}"},
		Assert: []setupspec.Assert{
			{Path: "user.username", Equals: "{user}"},
			{Path: "user.type", Equals: "root"},
		},
	}},
	Manual: "the first visit offers to create the root account",
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := setup.Setup(ctx, h); err != nil {
		return err
	}
	h.Info("The media folder is mounted at " + mediastorage.Media(h.Settings) + ". Create missing audiobook or podcast subfolders as the Host user, then choose them in Audiobookshelf Settings > Libraries. Setup does not create libraries or import media; preserve existing folders and settings.")
	if h.Peer("homarr") == nil {
		h.Skip("homarr-account", "Homarr is not installed; no Audiobookshelf account or API key is needed")
		return nil
	}
	return setupHomarrCredential(ctx, h)
}

func (App) Check(ctx context.Context, h *helpers.H) error { return setup.Check(ctx, h) }
