# self-host on Omarchy

Omarchy users can discover, open, and operate Apps with the same keyboard menu they already use. The existing CLI owns the Project and the terminal Dashboard handles Onboarding, Catalog, detailed inspection, and longer Actions. The Omarchy package adds a shell adapter and a desktop entry. Its menu extension adds one row under Setup and one under Install.

## Install and open

The intended distribution is the Omarchy package repository. When both packages are available there, `sudo pacman -S self-host-omarchy` pulls in the matching `self-host` version. CLI-only users can select `self-host` alone. AUR registration is independent of this route.

For local development, build with [build-packages](build-packages) on Arch. Supply an absolute path to a Linux self-host binary for the builder's architecture, its known SHA-256, release version, package revision, and an absolute output directory. This stages both packages with makepkg. An optional sixth argument points to a local Dashboard Icons source tree. The builder matches the available `svg/` and `png/` files against the existing generated Catalog. The package uses the existing Logo and resolves its App shortcut icons offline. The standalone Pacman path is `sudo pacman -U` with the two built package files in one transaction. To publish a repository, sign the packages with the repository's trusted key, then index them with `repo-add --include-sigs --sign`. Use a trusted repository and its normal update entrypoint.

After installation, search for **self-host** in the launcher. To try the 4.5 Super menu integration, merge the two keys printed by `self-host-omarchy menu-extension` into `~/.config/omarchy/extensions/omarchy-menu.jsonc`, keeping your other entries. If you tried the earlier prototype, remove its standalone `"self-host"` key. **Setup → Self-hosting** and the branded launcher open the existing Onboarding on a fresh installation. Once a Project exists, they open the App picker using its current Status. **Install → Service → self-host** uses Omarchy’s normal Pacman installer. On 3.8 the adapter can use Walker from the application launcher.

- **Open in browser** reads the current Address and opens it as your desktop user.
- **Add launcher shortcut** pins that App to the selected Home folder. The shortcut resolves the current Address when opened. A packaged App icon appears in launcher results when available. Removing the App also removes its generated desktop entry, preserving edited shortcuts.
- **Start, Stop, Restart** review one managed App in the native picker. Cancel returns to that App. When the default local socket requires administrator access, these Actions are available with a conditional review and Omarchy’s authorization dialog. Self-host checks the App’s live Status under the Project lock before applying the Action. Use the Dashboard when the native dialog or live Docker inspection is unavailable.
- **App details in Dashboard** runs `self-host --app <id>` to open the Apps tab at that App. The main **Terminal Dashboard** row launches the usual Dashboard or Onboarding.
- **Ask your agent** opens your configured Omarchy agent and copies a starter prompt for you to paste. From App details in the picker, the prompt includes the App ID. It requests inspection, a plan, and explicit approval before changes. The Project-wide prompt points to `self-host skill unmanaged` for an App outside the Catalog. If no default is configured, Omarchy opens its agent picker.
- **Run Doctor** uses the existing CLI checks.

### Top bar and quick commands

On packaged Omarchy 4.5, Onboarding’s **Look** step offers the top bar shortcut alongside Omarchy’s own terminal Logo. It starts off and applies only after the first App install succeeds. In **Dashboard → Settings → Omarchy**, use Enter to show or hide the shortcut and `r` to read its current state. Left-click the Logo for Apps, or right-click for the Dashboard. Omarchy owns the placement, so you can drag it like other bar widgets. The shortcut opens the desktop’s default Project.

The Dashboard delegates this preference to Omarchy’s running shell, including when the Dashboard needed administrator authorization for Docker. The registered widget uses the existing white Logo in the bar's foreground color. Super search still uses the full-color Logo. The widget launches the existing adapter on click and performs no background App inspection. It uses the standard Omarchy bar. Hide it in Settings before removing the package.

Quick commands open in Omarchy’s centered floating terminal. Successful Start, Stop and Restart close it and show a notification. Doctor and failed Actions leave their output open for review. The Dashboard still uses the normal terminal.

Doctor, the Dashboard, and reviewed quick Actions use Omarchy’s system authorization dialog when the active desktop session needs access to the default local Docker socket. Cancelling closes the terminal that requested it. Doctor and the Dashboard can use the terminal’s existing sudo prompt for custom Projects and sessions without the built-in authorization agent. The Polkit action requires active administrator authentication for each request. The helper runs Doctor, the Dashboard, or one reviewed Start, Stop, or Restart in the invoking desktop user’s default Project.

Omarchy’s launcher uses the user’s configured agent and its usual permission settings. The menu leaves sending the prompt to the user. The data model, Docker work, upgrades and Setup stay in self-host and Pacman. One-shot pickers need no background process. Pacman package removal leaves the Home folder and any explicitly created user shortcuts. To clean up the latter beforehand use **Remove launcher shortcut** for each App.

## Upstream review

The split package and 4.5 JSONC extension are the implementation proof. Submit the package recipe to Omarchy’s package repository for its edge build and signing review. Once the package is available, add the Install row in the existing `default/omarchy/omarchy-menu.jsonc`. Include the Setup row to give people a predictable place to start Onboarding and return to the Dashboard. Super search also finds the branded desktop entry through Omarchy's existing Apps provider. The package recipe stages a checksum-pinned local build. Adapt its source declaration to a tagged release asset for Omarchy’s builders and verify both architectures before promotion.

- [Native test evidence and two-row menu patch](../../docs/research/omarchy-evidence/README.md)
- [Omarchy package repository contribution workflow](https://github.com/omacom/omarchy-pkgs#contribute-a-package)
- [Released 4.5 menu extension and picker contract](https://github.com/omacom/omarchy/blob/v4.5.0/docs/menu.md)
- [Pacman signature policy](https://man.archlinux.org/man/pacman.conf.5)
