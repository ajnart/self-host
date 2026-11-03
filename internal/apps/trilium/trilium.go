package trilium

import (
	"context"
	"net/http"
	"strconv"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{}

const (
	image              = "triliumnext/trilium:v0.106.1"
	loginRecovery      = "Open Trilium at its Address and review the intended notebook. Finish new-notebook, sync or Restore setup yourself, preserving existing notes and sync settings. Use the shared Admin password only for a new notebook; never reset an existing password or disable authentication. Restore the known matching Project credential privately if needed, then run self-host setup trilium."
	HomarrRecoveryHint = "Trilium ETAPI tokens have full notebook API access, not read-only scopes. Keep existing tokens and verify the intended notebook in Trilium. Automatic token issuance and positive notebook identity for native Homarr are not available yet; no token was created or replaced. Leave this step Manual until supported credential ownership and identity verification are available, then retry self-host setup trilium."
)

func (App) Info() app.Info {
	return app.Info{
		ID: "trilium", Name: "Trilium", Category: "Productivity",
		Description:   "Organize your notes and ideas in a private knowledge base",
		Image:         image,
		Docs:          "https://docs.triliumnotes.org/user-guide/setup/server/installation/docker.html",
		Port:          8080,
		ContainerPort: 8080,
		Service:       "trilium",
		WebSockets:    true,
	}
}

func (App) Inputs() []app.Input { return nil }

func (App) Compose(rc app.RenderContext) string {
	return `services:
  trilium:
    image: ` + image + `
    restart: unless-stopped
    environment:
      TZ: ${TZ}
    volumes:
      - ./data:/home/node/trilium-data
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:8080
`
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/api/health-check", helpers.WantStatus(http.StatusOK), helpers.NoRedirect()); err != nil {
		return err
	}
	if err := h.Step(ctx, "admin", "verify the existing Trilium password", helpers.StepSpec{
		Do:         func(ctx context.Context) error { return verifyLogin(ctx, h) },
		Hint:       loginRecovery,
		Credential: "admin",
	}); err != nil {
		return err
	}
	if h.Peer("homarr") == nil {
		return nil
	}
	return h.Step(ctx, "homarr-key", "review Trilium notebook identity and ETAPI access", helpers.StepSpec{
		Do:         func(context.Context) error { return &helpers.ManualError{Hint: HomarrRecoveryHint} },
		Hint:       HomarrRecoveryHint,
		Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	if err := verifyLogin(ctx, h); err != nil {
		return err
	}
	if h.Peer("homarr") != nil {
		return &helpers.ManualError{Hint: HomarrRecoveryHint}
	}
	return nil
}

func (App) Links() []app.Link { return nil }

func (App) StandaloneGuide() string {
	return "On the first visit, choose a new notebook, sync an existing notebook or Restore a Backup, then set its password yourself. Trilium has one password, not a username. Keep the entire data folder and a separate Backup. The image is versioned because server and desktop sync protocols must remain compatible; review all clients before deliberately changing the image. ETAPI tokens grant full notebook API access, not read-only statistics access."
}
