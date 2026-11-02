package jackett

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type App struct{}

func (App) Info() app.Info {
	return app.Info{
		ID: "jackett", Name: "Jackett", Category: "Requests",
		Description:   "Connect your download apps to the torrent search sites you choose",
		Image:         "ghcr.io/hotio/jackett:latest",
		Docs:          "https://hotio.dev/containers/jackett/",
		Port:          9117,
		ContainerPort: 9117,
		Service:       "jackett",
		Path:          "/UI/Dashboard",
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{
		{Key: "admin_password", Prompt: "Existing Jackett administrator password", Private: true, RecoveryOnly: true,
			Help: "Optional existing password when Jackett does not use the shared Admin password. Supply it privately; Setup never changes it. Reinstall replaces Compose, so review and approve those changes first."},
		{Key: "api_key", Prompt: "Existing Jackett instance API key", Private: true, RecoveryOnly: true,
			Help: "Optional recovery of the existing instance key. Setup never rotates it. Supply it privately; reinstall replaces Compose, so review and approve those changes first."},
	}
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  jackett:
    image: ghcr.io/hotio/jackett:latest
    restart: unless-stopped
    environment:
      PUID: ${PUID}
      PGID: ${PGID}
      TZ: ${TZ}
      UMASK: ${UMASK}
    volumes:
      - ./config:/config
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:9117
`
}

func (App) StandaloneGuide() string {
	return "Before sharing access, set an administrator password in Jackett. Its login uses only a password, not a username. Keep the existing instance key and tracker settings; choose any trackers and accounts yourself. Setup does not set the first password, test trackers, search or download releases."
}

func (App) Links() []app.Link { return nil }

const (
	configPath  = "/api/v2.0/server/Config"
	torznabPath = "/api/v2.0/indexers/all/results/torznab/api"
	loginHint   = "Open Jackett and review its administrator password privately using self-host credentials. On a new instance, set the first password yourself; Setup cannot safely infer first-run ownership. For an existing instance, restore its saved password or explicitly supply the existing password through the private admin_password input after reviewing reinstall's Compose changes. Preserve the instance key and trackers, then run self-host setup jackett."
	keyHint     = "Restore Jackett's existing instance API key through the private api_key input after reviewing reinstall's Compose changes, then run self-host setup jackett. Setup never replaces a rejected key, changes tracker credentials or searches trackers."
	apiHint     = "Back up Jackett and review its version, authentication and base path without changing its existing key or trackers. Setup supports the root-path API in Jackett 0.24.2994 and newer 0.x. Restore that supported connection, then run self-host setup jackett."
)

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := h.Ready(ctx, "/health", helpers.WantStatus(http.StatusOK), helpers.NoRedirect()); err != nil {
		return err
	}
	return h.Step(ctx, "api-key", "verify Jackett's administrator and instance API key", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			err := (App{}).Check(ctx, h)
			return err == nil, err
		},
		Do: func(ctx context.Context) error {
			return h.StoreAPIKey(ctx, helpers.APIKey{Discover: acceptedKey})
		},
		Hint:       loginHint,
		Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	if h.Secret(h.Key("api_key")) == "" {
		return &helpers.ManualError{Hint: "Run self-host setup jackett to verify its existing administrator password and save its accepted instance API key. " + loginHint}
	}
	_, err := acceptedKey(ctx, h)
	return err
}

func acceptedKey(ctx context.Context, h *helpers.H) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	password := h.Secret(h.Key("admin_password"))
	if password == "" {
		password = h.AdminPassword()
	}
	if password == "" {
		return "", &helpers.ManualError{Hint: loginHint}
	}
	h.Log.Secret(password)
	session := h.Session()
	login, err := session.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: "/UI/Dashboard", NoRedirect: true,
		Form: url.Values{"password": {password}},
	})
	if err != nil {
		return "", requestError(ctx)
	}
	if login.Status != http.StatusFound || session.Cookie("Jackett") == "" {
		return "", &helpers.ManualError{Hint: loginHint}
	}
	response, err := session.HTTP(ctx, helpers.Request{Path: configPath, NoRedirect: true})
	if err != nil {
		return "", requestError(ctx)
	}
	if response.Status != http.StatusOK {
		return "", &helpers.ManualError{Hint: loginHint}
	}
	var config struct {
		Key      *string `json:"api_key"`
		Version  *string `json:"app_version"`
		Password *string `json:"password"`
	}
	if response.Decode(&config) != nil || config.Version == nil || config.Key == nil || config.Password == nil {
		return "", &helpers.ManualError{Hint: apiHint}
	}
	if *config.Password == "" {
		return "", &helpers.ManualError{Hint: loginHint}
	}
	if !helpers.VersionCompatible(*config.Version, "0.24.2994") || strings.TrimSpace(*config.Key) == "" {
		return "", &helpers.ManualError{Hint: apiHint}
	}
	key := *config.Key
	h.Log.Secret(key)
	if saved := h.Secret(h.Key("api_key")); saved != "" && saved != key {
		return "", &helpers.ManualError{Hint: keyHint}
	}
	anonymous, err := h.HTTP(ctx, helpers.Request{Path: configPath, NoRedirect: true})
	if err != nil {
		return "", requestError(ctx)
	}
	if anonymous.Status != http.StatusUnauthorized && anonymous.Status != http.StatusForbidden && !loginRedirect(anonymous, h.BaseURL) {
		return "", &helpers.ManualError{Hint: loginHint}
	}
	var caps struct {
		XMLName xml.Name  `xml:"caps"`
		Text    string    `xml:",chardata"`
		Error   *struct{} `xml:"error"`
		Server  struct {
			Title string `xml:"title,attr"`
		} `xml:"server"`
		Search struct {
			Available string `xml:"available,attr"`
		} `xml:"searching>search"`
	}
	if err := torznab(ctx, h, key, "caps", &caps); err != nil {
		return "", err
	}
	if caps.Error != nil || caps.Server.Title != "Jackett" || caps.Search.Available != "yes" && caps.Search.Available != "no" || strings.TrimSpace(caps.Text) != "" {
		return "", &helpers.ManualError{Hint: apiHint}
	}
	var indexers struct {
		XMLName xml.Name   `xml:"indexers"`
		Text    string     `xml:",chardata"`
		Unknown []struct{} `xml:",any"`
		Entries []struct {
			ID         string `xml:"id,attr"`
			Configured string `xml:"configured,attr"`
			Title      string `xml:"title"`
		} `xml:"indexer"`
	}
	if err := torznab(ctx, h, key, "indexers", &indexers); err != nil {
		return "", err
	}
	if strings.TrimSpace(indexers.Text) != "" || len(indexers.Unknown) != 0 {
		return "", errors.New("Jackett returned invalid configured-indexer metadata; preserve trackers and retry self-host setup jackett")
	}
	seen := map[string]bool{}
	for _, indexer := range indexers.Entries {
		if strings.TrimSpace(indexer.ID) == "" || strings.TrimSpace(indexer.Title) == "" || indexer.Configured != "true" || seen[indexer.ID] {
			return "", errors.New("Jackett returned invalid configured-indexer metadata; preserve trackers and retry self-host setup jackett")
		}
		seen[indexer.ID] = true
	}
	return key, nil
}

func loginRedirect(response helpers.Response, baseURL string) bool {
	if response.Status != http.StatusFound {
		return false
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	location, err := base.Parse(response.Header.Get("Location"))
	return err == nil && location.User == nil && strings.EqualFold(base.Scheme, location.Scheme) && strings.EqualFold(base.Host, location.Host) && location.Path == "/UI/Login"
}

func torznab(ctx context.Context, h *helpers.H, key, action string, target any) error {
	query := url.Values{"apikey": {key}, "t": {action}}
	if action == "indexers" {
		query.Set("configured", "true")
	}
	response, err := h.HTTP(ctx, helpers.Request{
		Path: torznabPath + "?" + query.Encode(), NoRedirect: true,
		Header: map[string]string{"Accept": "application/xml"},
	})
	if err != nil {
		return requestError(ctx)
	}
	if response.Status != http.StatusOK {
		return &helpers.ManualError{Hint: keyHint}
	}
	decoder := xml.NewDecoder(strings.NewReader(string(response.Body)))
	if err := decoder.Decode(target); err != nil {
		return &helpers.ManualError{Hint: "Jackett did not return accepted Torznab metadata; HTTP 200 can contain an API-key error. " + keyHint}
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errors.New("Jackett returned malformed Torznab metadata; retry self-host setup jackett")
		}
		switch value := token.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(value)) == "" {
				continue
			}
		case xml.Comment:
			continue
		}
		return errors.New("Jackett returned unexpected trailing Torznab metadata; retry self-host setup jackett")
	}
}

func requestError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("Jackett request failed; check the App's Address and trusted TLS without changing credentials, then retry self-host setup jackett")
}
