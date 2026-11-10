# Lore Windows icon set

SVG source icons for the Windows shell extension, the Lore Companion windows and the Lore application.
Requirements: `../icons-requirements.md`.

```
platform/windows/shared/svg/
  build-icons.mjs   generator, node build-icons.mjs
  lore-glyph.svg    master Lore mark, the single source of the glyph shape
  *.svg             28 generated icons
  preview.html      preview of every icon at 16, 20, 24, 32, 48 and 256 px
```

## Design rules

- **Main sign** is the Lore mark (`lore-glyph.svg`) drawn in `#0D1117` on a rounded blue square `#58A6FF`.
  The glyph is used unchanged in every icon.
- **An operation adds a badge, it never replaces the sign.** The badge sits in the lower right corner on the
  sign corner, so the mark stays readable.
- **A state overlay sits in the lower left corner**, TortoiseSVN style, and is a separate file composited over
  the file icon by the shell. The overlay never covers the file icon body.
- **State is carried by shape, not only by colour.** Pencil, plus, exclamation, padlock, minus and the combined
  group are different silhouettes; the same holds for the interface states (check, triangle, cross, `i`, spinner,
  folder, gear).
- Canvas is `512 x 512` with `viewBox="0 0 512 512"`. Nothing is drawn outside the canvas.
- Icons are flat: no gradients, no shadows, no baked-in background except where the design is a solid disc.

## Geometry

| Part | Value |
|---|---|
| Canvas | 512 x 512 |
| Sign square | 368 px at (12, 12), corner radius 84 |
| Mark inside the sign | scale 0.62, centred on the sign |
| Sign without badge (`lore`) | 368 px at (72, 72) |
| App icon (`app`) | 464 px at (24, 24), corner radius 100, mark scale 0.68 |
| Operation badge | centre (396, 396), r 104, white ring 20, symbol scale 0.86, symbol stroke 30 |
| Overlay badge | centre (150, 362), r 132, white ring 24, symbol scale 1.15, symbol stroke 30 |
| Interface state disc | centre (256, 256), r 216 |

The badge is centred on the sign corner and reaches x 282..510, y 282..510. The Lore mark has no strokes in
that corner, so the badge never touches the mark. `build-icons.mjs` was checked against the glyph outline for
zero overlap.

## Palette

| Colour | Used for |
|---|---|
| `#58A6FF` | main sign background |
| `#0D1117` | main sign mark, terminal badge fill |
| `#2EA043` | clone, sync, commit, success, update, installer, unlock |
| `#1F6FEB` | stage, status, info, staged overlay |
| `#8250DF` | history |
| `#D29922` | locked overlay |
| `#DB6D28` | modified overlay |
| `#CF222E` | error, conflicted overlay, uninstaller |
| `#6E7781` | ignored overlay, settings gear |
| `#8B949E` | lock, empty folder front plate |
| `#FFA000` / `#FFCA28` | folder back plate / front plate |
| `#CF1F25` / `#FEC901` / `#010101` | warning outline / fill / exclamation |
| `#57606A` | empty folder back plate |
| `#FFFFFF` | badge ring and badge symbols |

## Icons

| File | Sign | Badge or glyph |
|---|---|---|
| `lore.svg` | Lore mark on blue | none |
| `app.svg` | Lore mark on blue, full bleed | none |
| `app-installer.svg` | Lore mark on blue | green arrow into a tray |
| `app-uninstaller.svg` | Lore mark on blue | red minus |
| `cli.svg` | Lore mark on blue | ink terminal prompt |
| `update.svg` | Lore mark on blue | green circular arrow |
| `clone.svg` | Lore mark on blue | green download arrow |
| `sync.svg` | Lore mark on blue | green circular arrows |
| `commit.svg` | Lore mark on blue | green check |
| `stage.svg` | Lore mark on blue | blue plus |
| `status.svg` | Lore mark on blue | blue information sign |
| `history.svg` | Lore mark on blue | purple clock |
| `lock.svg` | Lore mark on blue | grey closed padlock |
| `unlock.svg` | Lore mark on blue | green open padlock |
| `folder-open.svg` | none | folder, used for choosing a directory |
| `overlay-modified.svg` | none | orange pencil |
| `overlay-staged.svg` | none | blue plus |
| `overlay-conflicted.svg` | none | red exclamation |
| `overlay-locked.svg` | none | amber closed padlock |
| `overlay-ignored.svg` | none | grey minus |
| `overlay-staged-modified.svg` | none | blue plus badge with an orange pencil badge behind it |
| `success.svg` | none | green disc with a white check |
| `warning.svg` | none | red-rimmed yellow triangle with a black exclamation |
| `error.svg` | none | red disc with a white cross |
| `info.svg` | none | blue disc with a white `i` |
| `progress.svg` | none | static spinner ring, rotate it in the UI |
| `empty.svg` | none | neutral grey folder |
| `settings.svg` | none | grey gear with a transparent hole |

## Working on the set

```bash
cd platform/windows/shared/svg
node build-icons.mjs          # regenerates all 28 icons from lore-glyph.svg
```

Edit `build-icons.mjs`, not the generated SVGs. The Lore mark itself lives in `lore-glyph.svg`; replace that
file when the artwork changes and regenerate.

Open `preview.html` in a browser to see every icon at all six sizes on light and dark backgrounds, the Explorer
menu mock at 16 px, the overlays composited over a file icon at 48, 24 and 16 px, the greyscale shape check
and the transparency checkerboard.

## Export for Windows

SVG is the source of truth. Run `node export-icons.mjs` to rasterise every icon at the exact target size with
Microsoft Edge. Set `LORE_EDGE` if Edge is installed in a non-standard location. No npm packages are needed.

- `../png/<size>/`: RGBA PNG at 16, 20, 24, 32, 48 and 256 px, with transparent backgrounds.
- `../ico/`: one ICO per icon carrying all six sizes; every entry is stored as a PNG payload.

The Explorer operation and overlay resources in `LoreShell/Resources.rc` and the companion resources in
`LoreCompanion.csproj` use these exports directly.

Acceptance, from the requirements: check on Windows 11 in light and dark themes at 100%, 125%, 150% and 200%
scale, in Explorer menu and submenu, for all overlay states, in window titles and shortcuts, with no colour
loss, no clipped edges and no blur at 16 px.
