// Package plantit is the retired Plant-it 0.10.0 server: a private plant
// diary whose intended owner is created by a person in its web client.
package plantit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/firstadmin"
	"github.com/ajnart/self-host/internal/helpers"
)

const (
	service         = "plant-it"
	defaultAPIPort  = "8080"
	ownerRecovery   = "Restore the saved Plant-it owner login and ID through the Project secret boundary after an explicit ownership decision, then run self-host setup plant-it. Do not create another garden, reset the password or delete data."
	missingOwnerMsg = "Plant-it owner login is missing or rejected"
)

// App is Plant-it. Traps: docs/catalog/traps/plant-it.md.
type App struct{}

func (App) Info() app.Info {
	return app.Info{
		ID: "plant-it", Name: "Plant-it", Category: "Productivity",
		Description:   "Keep a diary of your plants, their photos and care history",
		Image:         "docker.io/msdeluise/plant-it-server:0.10.0",
		Docs:          "https://github.com/MDeLuise/plant-it/blob/517fa4824ba0b3037f3a44733a76b6d6567e85d9/online-resources/documentation/docs/server-installation.md",
		Port:          3000,
		ContainerPort: 3000,
		Service:       service,
	}
}

func (App) Inputs() []app.Input {
	return []app.Input{{Key: "api-port", Prompt: "Plant-it API port", Default: defaultAPIPort, Help: "Host port of the API the web client asks for, separate from the web UI", HostPortProtocols: []string{"tcp"}, Validate: func(value string) error {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 || strings.Trim(value, "0123456789") != "" {
			return errors.New("must be a decimal port from 1 to 65535")
		}
		return nil
	}}}
}

func (App) Secrets() []app.SecretSpec {
	return []app.SecretSpec{
		{Name: "db_password", Length: 48},
		{Name: "db_root_password", Length: 48},
		{Name: "jwt_secret", Length: 64},
	}
}

func (App) AdminEmail(username string) string {
	if username == "" {
		return ""
	}
	return username + "@selfhost.local"
}

func apiPort(inputs map[string]string) string {
	if selected := inputs["api-port"]; selected != "" {
		return selected
	}
	return defaultAPIPort
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  plant-it:
    image: docker.io/msdeluise/plant-it-server:0.10.0
    restart: unless-stopped
    environment:
      TZ: ${TZ}
      MYSQL_HOST: plant-it-database
      MYSQL_PORT: "3306"
      MYSQL_DATABASE: bootdb
      MYSQL_USERNAME: plantit
      MYSQL_PSW: ${PLANT_IT_DB_PASSWORD}
      API_PORT: "8080"
      UPLOAD_DIR: /upload-dir
      JWT_SECRET: ${PLANT_IT_JWT_SECRET}
      JWT_EXP: "1"
      USERS_LIMIT: "1"
      CACHE_TYPE: none
      CACHE_PASSWORD: ""
      LOG_LEVEL: INFO
      NTFY_ENABLED: "false"
      GOTIFY_ENABLED: "false"
    volumes:
      - ./upload-dir:/upload-dir
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:3000
      - ${BIND_IP}:` + apiPort(rc.Inputs) + `:8080
    depends_on:
      - plant-it-database
  plant-it-database:
    image: docker.io/library/mysql:8.0
    restart: unless-stopped
    environment:
      MYSQL_DATABASE: bootdb
      MYSQL_USER: plantit
      MYSQL_PASSWORD: ${PLANT_IT_DB_PASSWORD}
      MYSQL_ROOT_PASSWORD: ${PLANT_IT_DB_ROOT_PASSWORD}
    volumes:
      - ./db:/var/lib/mysql
`
}

func (App) Links() []app.Link { return nil }

func (App) StandaloneGuide() string {
	return "After the first start, open the web client, enter the API address when it asks, and register the owner account. Keep ./db and ./upload-dir: they hold the plants, photos and care history."
}

func apiBase(h *helpers.H) string {
	return "http://127.0.0.1:" + apiPort(h.Inputs)
}

type loginResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	JWT      struct {
		Value string `json:"value"`
	} `json:"jwt"`
}

// ownerID signs in and returns the account ID after the authenticated
// /api/user read confirms the same ID and username. Any refusal or malformed
// answer is a rejection, never a signal that the instance is empty.
func ownerID(ctx context.Context, h *helpers.H, user, password string) (int64, error) {
	if user == "" || password == "" {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	resp, err := h.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, BaseURL: apiBase(h), Path: "/api/authentication/login", NoRedirect: true,
		JSON: map[string]string{"username": user, "password": password},
	})
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, errors.New("Plant-it sign-in request failed; retry Setup")
	}
	var login loginResponse
	if resp.Status != http.StatusOK || resp.Decode(&login) != nil {
		return 0, nil
	}
	h.Log.Secret(login.JWT.Value)
	if login.ID <= 0 || login.Username != user || login.JWT.Value == "" {
		return 0, nil
	}
	resp, err = h.HTTP(ctx, helpers.Request{
		BaseURL: apiBase(h), Path: "/api/user", NoRedirect: true,
		Header: map[string]string{"Authorization": "Bearer " + login.JWT.Value},
	})
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, errors.New("Plant-it account request failed; retry Setup")
	}
	var current loginResponse
	if resp.Status != http.StatusOK || resp.Decode(&current) != nil || current.ID != login.ID || current.Username != user {
		return 0, nil
	}
	return login.ID, nil
}

func validOwner(user, password string) error {
	if len(user) < 3 || len(user) > 20 {
		return errors.New("the Admin username must be 3 to 20 characters for Plant-it")
	}
	if len(password) < 8 || len(password) > 40 {
		return errors.New("the Admin password must be 8 to 40 characters for Plant-it")
	}
	return nil
}

func setup() firstadmin.FirstAdmin {
	return firstadmin.FirstAdmin{
		Ready: firstadmin.Probe{Path: "/api/info/ping", Contains: "pong", Request: func(h *helpers.H) helpers.Request {
			return helpers.Request{BaseURL: apiBase(h), Path: "/api/info/ping"}
		}},
		Valid: validOwner,
		Login: func(ctx context.Context, h *helpers.H, user, password string) (bool, error) {
			id, err := ownerID(ctx, h, user, password)
			return id > 0, err
		},
		Create: func(context.Context, *helpers.H, string, string) error {
			return fmt.Errorf("%w: Plant-it has no way to tell an empty instance from a rejected login, so a person registers the owner in its web client", helpers.ErrManual)
		},
		Hint: func(h *helpers.H, user string) string {
			return "Open " + h.URL + ", enter " + apiBase(h) + " as the server address when asked, register the owner " + user + " with email " + (App{}).AdminEmail(user) +
				" and the password shown by `self-host credentials`, then run: self-host setup plant-it. If an owner already exists, restore its login through the Project secret boundary; do not delete the database or reset the password."
		},
	}
}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := setup().Setup(ctx, h); err != nil {
		return err
	}
	if h.Peer("homarr") == nil {
		h.Skip("homarr-owner", "Homarr is not installed; no Plant-it owner snapshot is needed")
		return nil
	}
	return h.Step(ctx, "homarr-owner", "record the Plant-it owner Homarr reads", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			err := VerifyHomarrOwner(ctx, h)
			if errors.Is(err, helpers.ErrManual) {
				return false, nil
			}
			return err == nil, err
		},
		Do:         func(ctx context.Context) error { return recordOwner(ctx, h) },
		Hint:       ownerRecovery,
		Credential: "admin",
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	return setup().Check(ctx, h)
}

func recordOwner(ctx context.Context, h *helpers.H) error {
	if h.Secret(h.Key("owner_id")) != "" || h.Secret(h.Key("homarr_username")) != "" || h.Secret(h.Key("homarr_password")) != "" {
		return fmt.Errorf("%w: %s", helpers.ErrManual, missingOwnerMsg)
	}
	id, err := ownerID(ctx, h, h.AdminUser(), h.AdminPassword())
	if err != nil {
		return err
	}
	if id == 0 {
		return fmt.Errorf("%w: Plant-it did not verify the owner %s", helpers.ErrManual, h.AdminUser())
	}
	if err := h.SetSecret(h.Key("homarr_password"), h.AdminPassword()); err != nil {
		return err
	}
	if err := h.SetSecret(h.Key("homarr_username"), h.AdminUser()); err != nil {
		return err
	}
	return h.SetSecret(h.Key("owner_id"), strconv.FormatInt(id, 10))
}

// VerifyHomarrOwner confirms the recorded owner login still signs in as the
// recorded ID. It never records, replaces or resets anything.
func VerifyHomarrOwner(ctx context.Context, h *helpers.H) error {
	recorded, err := strconv.ParseInt(h.Secret(h.Key("owner_id")), 10, 64)
	if err != nil || recorded <= 0 {
		return fmt.Errorf("%w: %s", helpers.ErrManual, missingOwnerMsg)
	}
	id, err := ownerID(ctx, h, h.Secret(h.Key("homarr_username")), h.Secret(h.Key("homarr_password")))
	if err != nil {
		return err
	}
	if id != recorded {
		return fmt.Errorf("%w: %s or belongs to a different account", helpers.ErrManual, missingOwnerMsg)
	}
	return nil
}

// HomarrOwnerID returns the recorded owner's ID after VerifyHomarrOwner.
func HomarrOwnerID(ctx context.Context, h *helpers.H) (int64, error) {
	if err := VerifyHomarrOwner(ctx, h); err != nil {
		return 0, err
	}
	return strconv.ParseInt(h.Secret(h.Key("owner_id")), 10, 64)
}
