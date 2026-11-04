# New Apps release visual

From `video/reel`, after `npm ci` and installing Playwright Chromium:

```bash
npm run release-visual -- --apps cleanuparr,immich --version 0.5.11 --out out/release
```

Writes `new-apps-0.5.11.png` (2× resolution) and `new-apps-0.5.11.svg` (animated, reduced-motion aware), and prints alt text. The self-host Logo connects to 1–24 Apps: up to six as one column, more as a grid of up to six rows and four columns, where each row's pulse continues from tile to tile. Names come from the site Catalog; icons and outlined fonts are local, with no network requests during rendering. Identical inputs and installed dependencies produce deterministic SVGs; PNGs also depend on the installed Chromium and platform.

Empty, unknown, duplicate and more than 24 App ids fail before launching Chromium. Missing local icons fail explicitly; App icons live in `assets/icons/<id>.svg|png`, which also feed the site's share images, so give an SVG without a size a `width` and `height`. `--preview` renders sample counts from 1 to 24 under `release/out/preview`; `index.html` provides an optional local preview.

From the repository root, discover newly added App ids:

```bash
scripts/release-new-apps.sh v0.5.8 v0.5.9
```

This prints `cleanuparr`. The second ref defaults to `HEAD`; both refs must use the Go layout. The helper lists App definitions, including Apps currently hidden from the Catalog. Skip the visual when the result is empty; split releases with more than 24 additions into separate images. No release is created or uploaded by these commands.

## Put the image in a release

GitHub serves release assets as `application/octet-stream` downloads, so an SVG uploaded to a release never renders or animates in its notes. Commit the animated SVG instead and embed it through `raw.githubusercontent.com`, which serves it as `image/svg+xml`; upload the PNG to the release as the static fallback.

```bash
APPS=$(scripts/release-new-apps.sh "$PREV" "$REF" | paste -sd, -)
npm run release-visual -- --apps "$APPS" --version "$VERSION" --out /tmp/rv
cp "/tmp/rv/new-apps-$VERSION.svg" docs/assets/releases/   # merge this in a PR, note the merge commit
gh release upload "v$VERSION" "/tmp/rv/new-apps-$VERSION.png"
```

At the top of the release notes, with the alt text the command prints and the commit that added the SVG:

```markdown
[![self-host X.Y.Z adds <App names>](https://raw.githubusercontent.com/ajnart/self-host/<commit>/docs/assets/releases/new-apps-X.Y.Z.svg)](https://github.com/ajnart/self-host/releases/download/vX.Y.Z/new-apps-X.Y.Z.png)
```

Pin the commit, not `main`, so the image never changes under a published release. Clicking it opens the PNG.
