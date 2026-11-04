package catalog

// defaultRecipes is the Recipe order the Dashboard and the site show.
var defaultRecipes = []Recipe{
	{
		ID:          "media",
		Name:        "Media",
		Description: "Watch your movies and shows, and have new ones requested and downloaded for you",
		// Seerr comes last: its Links need Jellyfin, Sonarr and Radarr ready.
		Apps: []string{"qbittorrent", "sonarr", "radarr", "prowlarr", "jellyfin", "seerr"},
	},
	{
		ID:          "full",
		Name:        "Full private home server",
		Description: "Everything for a household: movies, photos, files, music, passwords and smart home",
		// Downloaders and media sources first, then the Apps that consume
		// them in turn, then the remaining independent services.
		Apps: []string{"qbittorrent", "prowlarr", "sonarr", "radarr", "jellyfin", "seerr", "immich", "nextcloud", "navidrome", "audiobookshelf", "vaultwarden", "home-assistant"},
	},
	{
		ID:          "homarr-control-plane",
		Name:        "Homarr control plane",
		Description: "One home page for all your apps, that your AI assistant can use too",
		Apps:        []string{"homarr"},
	},
	{
		ID:          "home-automation",
		Name:        "Home automation",
		Description: "Automate your smart home, with cameras that spot people and cars",
		Apps:        []string{"home-assistant", "node-red", "frigate"},
	},
	{
		ID:          "local-ai",
		Name:        "Local AI",
		Description: "Chat with AI on your own computer, so your questions stay private",
		// The model server precedes the chat UI; monitoring comes last.
		Apps: []string{"ollama", "open-webui", "beszel"},
	},
	{
		ID:          "monitoring",
		Name:        "Monitoring and observability",
		Description: "Watch your computers and apps and get alerts when something goes wrong",
		// Metrics and logs are the backends; grafana is the dashboard last.
		Apps: []string{"prometheus", "loki", "beszel", "uptime-kuma", "grafana"},
	},
	{
		ID:          "photos-and-files",
		Name:        "Photos and cloud files",
		Description: "Back up every phone's photos and keep your files in sync, replacing cloud drives",
		Apps:        []string{"immich", "nextcloud"},
	},
	{
		ID:          "remote-access",
		Name:        "Remote access",
		Description: "Reach your apps from outside your home, privately or through your own web address",
		Apps:        []string{"tailscale", "cloudflare-tunnel", "caddy"},
	},
}
