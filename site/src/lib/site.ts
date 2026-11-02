import { VERSION } from "./version";

export { VERSION };

export const INSTALL = "curl -fsSL https://self-host.fyi/install | bash";

export const AGENT_INSTALL =
	"Use https://self-host.fyi/llms.txt to help me set up my own home server. Ask what I want to run, explain the plan, and wait for my approval before installing.";

export const INSTALL_METHODS = [
	{ label: "curl", slug: "curl", code: INSTALL },
	{
		label: "paste in your agent",
		slug: "paste-in-your-agent",
		code: AGENT_INSTALL,
		lang: "text",
	},
	{
		label: "wget",
		slug: "wget",
		code: "wget -qO- https://self-host.fyi/install | bash",
	},
	{
		label: "read it first",
		slug: "read-it-first",
		code: `curl -fsSL https://self-host.fyi/install -o install.sh &&
curl -fsSL https://self-host.fyi/install.sh.sha256 -o install.sh.sha256 &&
shasum -a 256 -c install.sh.sha256 &&
less install.sh && bash install.sh`,
	},
	{
		label: "release archive",
		slug: "release-archive",
		code: `(
set -eu
VERSION=${VERSION} OS=linux ARCH=amd64
BASE=https://github.com/ajnart/self-host/releases/download/v$VERSION
ARCHIVE=self-host_\${VERSION}_\${OS}_\${ARCH}.tar.gz
curl -fsSLO "$BASE/$ARCHIVE"
curl -fsSLO "$BASE/checksums.txt"
awk -v archive="$ARCHIVE" '$2 == archive || $2 == "*" archive { print; count++ } END { if (count != 1) exit 1 }' checksums.txt > "$ARCHIVE.sha256"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum -c "$ARCHIVE.sha256"
else
  shasum -a 256 -c "$ARCHIVE.sha256"
fi
tar -xzf "$ARCHIVE" self-host
mkdir -p "$HOME/.local/bin"
mv self-host "$HOME/.local/bin/"
)`,
	},
	{
		label: "Homebrew",
		slug: "homebrew",
		code: "brew install ajnart/tap/self-host",
	},
	{
		label: "Docker",
		slug: "docker",
		code: `docker run --rm -it --network host \\
  --user "$(id -u):$(id -g)" --group-add "$(stat -c %g /var/run/docker.sock)" \\
  -v /var/run/docker.sock:/var/run/docker.sock \\
  -v "$HOME/.self-host:$HOME/.self-host" \\
  -e USER -e HOME -e SELF_HOST_HOME="$HOME/.self-host" \\
  ghcr.io/ajnart/self-host:latest   # Linux; macOS: see the docs`,
	},
	{
		label: "deb / rpm / apk",
		slug: "deb-rpm-apk",
		code: `BASE=https://github.com/ajnart/self-host/releases/download/v${VERSION}
curl -fsSLO $BASE/self-host_${VERSION}_linux_amd64.deb && sudo apt install ./self-host_${VERSION}_linux_amd64.deb
# Fedora: .rpm with dnf install; Alpine: .apk with apk add --allow-untrusted`,
	},
	{
		label: "Arch / Omarchy",
		slug: "arch-omarchy",
		code: `curl -fsSLO https://github.com/ajnart/self-host/releases/download/v${VERSION}/PKGBUILD
makepkg -si   # not on the AUR yet: registration is closed`,
	},
	{
		label: "from source",
		slug: "from-source",
		code: `go install github.com/ajnart/self-host/cmd/self-host@latest
# or: git clone https://github.com/ajnart/self-host && cd self-host && go build -o ~/.local/bin/self-host ./cmd/self-host`,
	},
] as const;

export type InstallMethodSlug = (typeof INSTALL_METHODS)[number]["slug"];

export const AGENT_PROMPT = AGENT_INSTALL;

export const LINKS = {
	github: "https://github.com/ajnart/self-host",
	releases: "https://github.com/ajnart/self-host/releases",
	issues: "https://github.com/ajnart/self-host/issues",
	discord: "https://discord.gg/aCsmEV5RgA",
	sponsor: "https://github.com/sponsors/ajnart",
	kofi: "https://ko-fi.com/ajnart",
	awesome: "https://github.com/awesome-selfhosted/awesome-selfhosted",
	dcm: "/compose",
};
