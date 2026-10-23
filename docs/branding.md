# CodexProxy console branding

The console uses the CodexProxy name on the browser tab, login screen, navigation,
about page, and upgrade guidance in English, Simplified Chinese, Traditional
Chinese, and Russian. Its existing typography, cards, theme colors, and navigation
remain the basis of the interface.

## Identity and resources

`management-webui/src/branding.ts` defines the name, logo, and project links.
The About CodexProxy page describes account access and request routing, shows
console/server versions and connection details, and links to this repository,
its documentation, issues, and releases. The upstream CLIProxyAPI attribution
remains visible. License files and compatibility headers are unchanged.

The former update check compared the server version with CLIProxyAPI releases.
It is replaced by a CodexProxy Releases link so custom fork build identifiers do
not produce misleading update results. Model browsing, local sign-out, and the
existing request logging settings remain available.

## Logo assets

- Master: [`codexproxy-logo.png`](../management-webui/src/assets/brand/codexproxy-logo.png).
- Runtime: [`codexproxy-logo.webp`](../management-webui/src/assets/brand/codexproxy-logo.webp).
- Usage: login, startup splash, navigation, about page, and browser favicon.

The mark combines a charcoal C-shaped frame with a copper routing arrow on an
ivory tile. The background outside the tile is transparent. The master was
created using the built-in image generation tool, then refined with an image edit.
The runtime file is a lossless WebP conversion of the master, preserving its
pixels and alpha channel. The single-file build embeds the runtime asset once;
no external logo request is required.

### Initial generation prompt

```text
Use case: logo-brand. Asset type: the production app icon and favicon for CodexProxy, a developer console for routing Codex and Claude API requests. Create one original, minimal, exceptionally crisp flat emblem: a bold open C-shaped geometric frame integrated with a compact right-facing routing arrow. The C and arrow should read as one coherent, balanced symbol with generous negative space, legible at 24px. Put the deep charcoal symbol on a warm ivory rounded-square tile with one restrained burnt-copper accent on the routing arrow. The existing console uses warm neutral surfaces, muted dark text and rounded controls. Large centered emblem filling most of a square canvas, generous equal safe margins. Transparent background outside the rounded tile. Clean vector-like edges, no texture, no shadows, no glow, no gradients, no 3D. No words, letters other than the implied C, slogans, mockups, grids, labels, or extra decorative marks. Deliver a single finished app emblem, not a sheet of options.
```

### Refinement prompt

```text
Refine this exact CodexProxy app logo for production. Keep the same C-shaped charcoal frame, copper right-pointing arrow, ivory rounded-square tile, proportions and composition. Make all color areas perfectly flat solid colors: charcoal #252525, copper #BB5A2D and warm ivory #F5EFE3. Remove all texture, gradients, speckles, stray pixels and ragged outlines. The outside of the rounded square must be entirely transparent with a perfectly smooth, clean antialiased perimeter. Preserve the clear interior geometric shapes, no text, no new objects, no glow, no shadow. A precise, pristine digital app icon, as if professionally drawn in a vector editor.
```

Both calls used `transparent_background: true`. The refinement used the first
generated image as its reference. Runtime conversion:

```bash
cwebp -lossless -z 9 management-webui/src/assets/brand/codexproxy-logo.png \
  -o management-webui/src/assets/brand/codexproxy-logo.webp
```

## Validation

Validation uses local synthetic management responses and synthetic model names;
no production account or upstream inference request is required.

- Bun 1.3.14: `bun run verify` (369 tests, ESLint, TypeScript, production build).
- Embedded management HTML generated with `VERSION=__CLI_PROXYAPI_VERSION__`.
- `go test -count=1 ./internal/managementasset` and a server build. The removed-provider
  guard checks the panel without image data URLs, avoiding random Base64 matches.
- Browser checks cover login, navigation, project links, and light/dark About
  CodexProxy layouts. The browser console reported no errors or warnings.
