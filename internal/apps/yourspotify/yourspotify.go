package yourspotify

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{}

const serverService = "your-spotify-server"

const configurationHint = "Keep Your Spotify's server held. Supply the existing Spotify client ID and secret through private inputs your-spotify.spotify_public and your-spotify.spotify_secret using owner-only files with --set-file. Preserve existing Compose, credentials and browser Addresses; reinstalling can replace Compose, so review it separately before retrying self-host setup your-spotify."

const startupHint = "Your Spotify's API server remains held by the your-spotify-manual Compose profile and self-host's deferred-service boundary. Its upstream startup runs migrations, history repair and duplicate deletion, then polls Spotify. Review the existing database, backup, credentials and provider consent before separately approving any server start; Setup never starts it or treats an empty folder as permission."

const oauthHint = "Personally review Spotify developer-app eligibility, existing Premium requirements, user allowlisting and the exact API Address ending in /oauth/spotify/callback. Spotify requires HTTPS except literal loopback HTTP, not localhost or an HTTP LAN Address. Account linking and the upstream OAuth playback/playlist scopes need your own explicit consent; self-host does not contact Spotify, subscribe, claim an account or change Access."

const ingestionHint = "Review and explicitly approve ongoing listening-history ingestion separately. A valid local public token or an empty statistic does not prove Spotify consent, a healthy ingestion grant or completed polling. Keep existing history and account settings; do not import private history, fabricate playback or restart the server to manufacture data. This App slice remains Manual pending that review."

func (App) Info() app.Info {
	return app.Info{
		ID: "your-spotify", Name: "Your Spotify", Category: "Media",
		Description: "Explore your Spotify listening history after you choose to connect your account",
		Image:       "yooooomi/your_spotify_client:latest",
		Docs:        "https://github.com/Yooooomi/your_spotify#installation",
		Port:        3000, ContainerPort: 3000, Service: "your-spotify",
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{
		{Key: "api-port", Prompt: "Published Your Spotify API port", Default: "8080", HostPortProtocols: []string{"tcp"}, Validate: validateAPIPort},
		{Key: "client_endpoint", Prompt: "Browser Address for Your Spotify (optional)", Help: "An explicit HTTP or HTTPS origin; otherwise use the selected local or home-network Address. This does not change Access.", Validate: validateOrigin},
		{Key: "api_endpoint", Prompt: "Browser Address for the Your Spotify API (optional)", Help: "An explicit HTTP or HTTPS origin. Spotify callbacks require HTTPS except literal loopback HTTP. This does not provision a proxy or change Access.", Validate: validateOrigin},
		{Key: "spotify_public", Prompt: "Existing Spotify developer client ID", Private: true, Help: "The developer application's client ID, not a public sharing token. Missing credentials remain Manual."},
		{Key: "spotify_secret", Prompt: "Existing Spotify developer client secret", Private: true, Help: "Supply the existing developer secret privately; self-host never creates or replaces it."},
		{Key: "homarr_api_key", Prompt: "Existing Your Spotify public token", Private: true, RecoveryOnly: true, Help: "The raw read-only token from Settings > Account > Public token, not the sharing Address or Spotify OAuth tokens. Regenerating it revokes existing sharing links."},
		{Key: "homarr_user_id", Prompt: "Selected Your Spotify Account ID", Private: true, RecoveryOnly: true, Help: "Copy Account ID from Settings > Account infos for the chosen token owner. A different account is not silently adopted."},
	}
}

func (App) Compose(rc app.RenderContext) string {
	client, api := browserOrigins(rc)
	return `services:
  your-spotify:
    image: yooooomi/your_spotify_client:latest
    restart: unless-stopped
    environment:
      API_ENDPOINT: ` + strconv.Quote(api) + `
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:3000
  your-spotify-server:
    image: yooooomi/your_spotify_server:latest
    restart: "no"
    profiles: [your-spotify-manual]
    logging:
      driver: local
      options:
        max-size: "10m"
        max-file: "3"
    depends_on:
      - your-spotify-mongo
    environment:
      API_ENDPOINT: ` + strconv.Quote(api) + `
      CLIENT_ENDPOINT: ` + strconv.Quote(client) + `
      SPOTIFY_PUBLIC: ${YOUR_SPOTIFY_SPOTIFY_PUBLIC}
      SPOTIFY_SECRET: ${YOUR_SPOTIFY_SPOTIFY_SECRET}
      MONGO_ENDPOINT: mongodb://your-spotify-mongo:27017/your_spotify
      TIMEZONE: ${TZ}
      LOG_LEVEL: warn
    ports:
      - ${BIND_IP}:` + apiPort(rc.Inputs) + `:8080
  your-spotify-mongo:
    image: mongo:8
    restart: unless-stopped
    volumes:
      - ./data:/data/db
      - ./configdb:/data/configdb
`
}

func (App) Deferred(*helpers.H) []string { return []string{serverService} }

func (App) Setup(ctx context.Context, h *helpers.H) error {
	var pending []error
	for _, step := range []struct {
		id, title, hint string
		verify          func(context.Context) error
	}{
		{"configuration", "review Your Spotify's saved configuration", configurationHint, func(context.Context) error { return configuration(h) }},
		{"startup-review", "keep history-changing server startup held", startupHint, func(context.Context) error { return &helpers.ManualError{Hint: startupHint} }},
		{"spotify-consent", "review Spotify account consent personally", oauthHint, func(context.Context) error { return &helpers.ManualError{Hint: oauthHint} }},
		{"ingestion", "review listening-history ingestion separately", ingestionHint, func(context.Context) error { return &helpers.ManualError{Hint: ingestionHint} }},
		{"public-token", "verify the selected local Your Spotify account", AccountRecoveryHint, func(ctx context.Context) error { return VerifyAccount(ctx, h) }},
	} {
		err := h.Step(ctx, step.id, step.title, helpers.StepSpec{Do: step.verify, Hint: step.hint})
		if err != nil && !errors.Is(err, helpers.ErrManual) {
			return err
		}
		pending = append(pending, err)
	}
	return errors.Join(pending...)
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	if err := configuration(h); err != nil {
		return err
	}
	if err := VerifyAccount(ctx, h); err != nil {
		return err
	}
	return &helpers.ManualError{Hint: startupHint + " " + oauthHint + " " + ingestionHint}
}

func (App) Links() []app.Link { return nil }

func (App) StandaloneGuide() string {
	return "The API server is held behind the your-spotify-manual Compose profile with automatic restart disabled; the browser client alone is not a working account. Before deliberately starting the server, separately review backup and preservation of any existing history: upstream startup runs migrations, repair and duplicate deletion, then polls Spotify. Supply existing developer credentials privately and review the exact browser/API Addresses and callback ending in oauth/spotify/callback. Spotify OAuth, existing Premium eligibility and ingestion need the person's explicit consent; no account linking, subscriptions, history imports or playback are automatic. Keep LOG_LEVEL at warn or error before using a public token because default request logs expose query tokens. A public token is read-only but indefinite and account-wide; regenerating it revokes existing sharing links."
}

func apiPort(inputs map[string]string) string {
	if value := inputs["api-port"]; value != "" {
		return value
	}
	return "8080"
}

func browserOrigins(rc app.RenderContext) (string, string) {
	host := rc.Settings["LAN_IP"]
	if rc.Settings["EXPOSURE"] == "local" || host == "" {
		host = "127.0.0.1"
	}
	client := "http://" + net.JoinHostPort(host, strconv.Itoa(rc.Port))
	api := "http://" + net.JoinHostPort(host, apiPort(rc.Inputs))
	if value := rc.Inputs["client_endpoint"]; value != "" {
		client = strings.TrimRight(value, "/")
	}
	if value := rc.Inputs["api_endpoint"]; value != "" {
		api = strings.TrimRight(value, "/")
	}
	return client, api
}

func validateAPIPort(value string) error {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 || strings.Trim(value, "0123456789") != "" {
		return errors.New("use a decimal API port from 1 to 65535")
	}
	return nil
}

func validateOrigin(value string) error {
	if value == "" {
		return nil
	}
	if _, ok := helpers.HTTPOrigin(value); !ok {
		return errors.New("use an HTTP or HTTPS origin without credentials, a path, query or fragment")
	}
	return nil
}

func configuration(h *helpers.H) error {
	if h.Secret(h.Key("spotify_public")) == "" || h.Secret(h.Key("spotify_secret")) == "" {
		return &helpers.ManualError{Hint: configurationHint}
	}
	if err := validateAPIPort(apiPort(h.Inputs)); err != nil {
		return &helpers.ManualError{Hint: "Review Your Spotify's selected api-port without changing Access, then retry self-host setup your-spotify."}
	}
	client, api := browserOrigins(app.RenderContext{Port: h.Port, Inputs: h.Inputs, Settings: h.Settings})
	if validateOrigin(client) != nil || validateOrigin(api) != nil {
		return &helpers.ManualError{Hint: configurationHint}
	}
	origin, _ := url.Parse(api)
	ip := net.ParseIP(origin.Hostname())
	if origin.Scheme != "https" && (ip == nil || !ip.IsLoopback()) {
		return &helpers.ManualError{Hint: oauthHint}
	}
	return nil
}
