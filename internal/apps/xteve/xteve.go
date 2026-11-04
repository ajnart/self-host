// Package xteve is xTeVe, an IPTV playlist and guide proxy that presents its
// channels to media servers as a network tuner.
package xteve

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

// App is xTeVe. Traps: docs/catalog/traps/xteve.md.
type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "xteve", Name: "xTeVe", Category: "Media",
		Description:   "Turn your IPTV playlists and guides into a tuner for your media server",
		Image:         "dnsforge/xteve:latest",
		Docs:          "https://github.com/xteve-project/xTeVe-Documentation/blob/master/en/configuration.md",
		Port:          34400,
		ContainerPort: 34400,
		Service:       "xteve",
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{
		{Key: "username", Prompt: "Existing xTeVe API-only username (optional)", Help: "A dedicated xTeVe user with API permission only; the CLI never creates or rotates it", Private: true},
		{Key: "password", Prompt: "Existing xTeVe API-only password (optional)", Help: "Supply both credentials through private inputs", Private: true},
		{Key: "auth", Prompt: "Type none only when the xTeVe API is intentionally unauthenticated (optional)", Help: "Never inferred from missing credentials and never disables existing authentication", Private: true},
	}
}

func (App) Files(app.RenderContext, func(string) string) []app.File {
	return []app.File{
		{Path: "config/.keep", Mode: 0o644},
		{Path: "guide2go/.keep", Mode: 0o644},
	}
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  xteve:
    image: dnsforge/xteve:latest
    restart: unless-stopped
    volumes:
      - ./config:/home/xteve/conf
      - ./guide2go:/home/xteve/guide2go/conf
    tmpfs:
      - /tmp/xteve
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:34400
`
}

func (App) StandaloneGuide() string {
	return "Open xTeVe at /web/ and finish the first-run tuner, guide and playlist choices yourself."
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	return h.Step(ctx, "ready", "verify the xTeVe API status with its dedicated credentials", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			err := VerifyHomarr(ctx, h)
			return err == nil, err
		},
		Do: func(ctx context.Context) error {
			var manual error
			err := h.Poll(ctx, time.Minute, func(ctx context.Context) (bool, error) {
				err := VerifyHomarr(ctx, h)
				if errors.Is(err, helpers.ErrManual) {
					manual = err
					return true, nil
				}
				return err == nil, err
			})
			if manual != nil {
				return manual
			}
			return err
		},
		Hint: recoveryHint,
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	return VerifyHomarr(ctx, h)
}
