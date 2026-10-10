# Design System

All values are in [design-tokens.json](design-tokens.json). Components read them as CSS variables (`--color-bg`, `--space-4`, ...). A component never contains a literal color or size.

## 1. Color tokens and contrast

Contrast is measured with the WCAG 2.1 formula on the page background `bg` (and on `surface` for cards). Text needs 4.5:1. Icons and control borders need 3:1.

| Token | Light | Dark | Use |
|---|---|---|---|
| `bg` | `#ffffff` | `#0b0f19` | Page background |
| `surface` | `#f6f7f9` | `#131a2a` | Cards, panels, table headers |
| `text` | `#111827` | `#e6e9f0` | Body text |
| `muted` | `#4b5563` | `#a0a8b8` | Secondary text |
| `border` | `#d6dae1` | `#2a3447` | Decorative dividers only (no meaning) |
| `border-strong` | `#6b7280` | `#7d8799` | Borders of inputs, buttons and focus-relevant controls |
| `primary` | `#4338ca` | `#a5b4fc` | Primary action, links, focus ring |
| `on-primary` | `#ffffff` | `#0b0f19` | Text on a `primary` fill |
| `success` | `#15803d` | `#4ade80` | Running, passed |
| `warning` | `#a16207` | `#facc15` | Deploying, degraded, stale |
| `danger` | `#b91c1c` | `#f87171` | Failed, destructive, production border |
| `info` | `#1d4ed8` | `#7db0ff` | Fast rollback, hints |

Measured contrast (recomputed for this spec):

| Pair | Light on `bg` | Light on `surface` | Dark on `bg` | Dark on `surface` | Need |
|---|---|---|---|---|---|
| `text` | 17.74 | 16.55 | 15.76 | 14.29 | 4.5 |
| `muted` | 7.56 | 7.05 | 8.01 | 7.27 | 4.5 |
| `primary` | 7.90 | 7.37 | 9.61 | 8.71 | 4.5 |
| `success` | 5.02 | 4.68 | 10.99 | 9.97 | 4.5 |
| `warning` | 4.92 | 4.59 | 12.50 | 11.34 | 4.5 |
| `danger` | 6.47 | 6.04 | 6.92 | 6.28 | 4.5 |
| `info` | 6.70 | 6.25 | 8.69 | 7.88 | 4.5 |
| `border-strong` | 4.83 | 4.51 | 5.29 | 4.80 | 3 |
| `on-primary` on `primary` | 7.90 | - | 9.61 | - | 4.5 |

`border` is decorative (1.40 and 1.53) and never the only mark of a control or a state. Status colors are used as text and icon colors on `bg` or `surface`. A filled status badge uses `surface` as fill with colored icon and `text` for the word.

**Charts:** series colors come from a categorical set of 6 colors per theme that must reach 3:1 on `bg` and `surface`, and each series also has a distinct marker shape and line style. The set is added to the token file as `chart` in the web skeleton and checked by the same contrast test.

## 2. Other tokens

| Group | Values |
|---|---|
| Spacing (4 px base) | 4, 8, 12, 16, 24, 32, 48 px (`space-1`..`space-7`) |
| Radius | `sm` 6, `md` 10, `lg` 14, `full` 9999 px |
| Type | Sans: Inter Variable. Mono: JetBrains Mono Variable. Both self-hosted woff2 ([REQ-UI-012](../11-ui.md#4-rules-and-requirements)). Scale (size/line): xs 12/16, sm 14/20, base 16/24, lg 18/28, xl 24/32, 2xl 30/36. Body is `base` on phone, `sm` allowed in dense tables on laptop and large. Weights 400, 500, 600. |
| Shadows | `sm`, `md`. In dark theme a 1 px `border` is added because shadows are faint. |
| Motion | `fast` 120 ms, `base` 200 ms, `slow` 320 ms, easing `cubic-bezier(.2,.8,.2,1)`. Used for opacity, transform and color only. |
| Focus ring | 2 px `primary` outline, 2 px offset, always visible on keyboard focus. |
| Z layers | base 0, sticky 10, drawer 20, dialog 30, toast 40, tooltip 50. |

## 3. Theme modes and switch rules

<a id="requirements"></a>

- **REQ-UI-014** (MUST) The UI has a light and a dark theme built only from the tokens of [design-tokens.json](design-tokens.json). Every text and icon pair in the table of section 1 passes WCAG 2.1 AA. A CI test reads the token file, computes every pair and fails below 4.5:1 (text) or 3:1 (control borders, icons). No component contains a hard-coded color.
- **REQ-UI-015** (MUST) Theme modes are `system` (default), `light`, `dark`. In `system` mode the UI follows `prefers-color-scheme` live (a change of the OS theme applies without reload). The choice is saved on the device in `localStorage`. When the API can store user preferences (see [known gaps](screens.md#known-api-gaps)), it is also saved per user and the server value wins after login. An inline script in `index.html` sets `data-theme` before the first paint, so the page never flashes the wrong theme.

Rules:

1. First visit and logged-out pages (login, setup): mode `system`, read from `localStorage` key `polipan.theme` if present.
2. In v1 `localStorage` is the only store, because `updateMe` has no preference field. Later, a server value from `getMe` wins after login and `localStorage` is updated.
3. The theme switch is in the user menu (three radio items: System, Light, Dark) and on the top bar (a button that cycles the three). It is also a palette command ("Switch theme"). It works on a phone.
4. Changing the theme applies at once and is written to `localStorage`. A failed server save (when it exists) never blocks the change and shows no error toast: it is a preference, not data.
5. The `<meta name="color-scheme">` and `theme-color` follow the theme. Native controls (scrollbars, form widgets) use `color-scheme`.
6. Theme change uses no animation longer than `fast`. Under reduced-motion it is instant.
7. The log viewer and the terminal follow the theme. They have no always-dark mode in v1, so contrast is the same as for the rest.
8. Printing and screenshots use the light theme.
9. `prefers-contrast: more` raises `border` to `border-strong` and thickens focus rings to 3 px.
10. `forced-colors` (Windows high contrast) is respected: status icons keep their shape, borders use system colors.

## 4. Status vocabulary

Always an icon and a word ([REQ-UI-013](../11-ui.md#4-rules-and-requirements)). The icon shape is different for each state, so it works without color.

| State group | Icon | Word examples | Color |
|---|---|---|---|
| Running, healthy, passed, ready, succeeded, live | filled circle | Running, Healthy, Passed, Ready | `success` |
| Deploying, building, degraded, suspect, queued, running (job), stale | triangle | Deploying, Degraded, Suspect | `warning` |
| Failed, down, critical | cross | Failed, Down | `danger` |
| Stopped, cancelled, superseded, cordoned, maintenance | square | Stopped, Cordoned | `muted` |
| Fast rollback, info | bolt | Fast rollback | `info` |

Environment badge: `Production` (chip with `danger` border), `Staging`, `Preview`, `Custom`. Kind `production` also gets a red top border on pages. A protected environment of any kind adds a lock icon and a "Protected" chip and also gets the red top border. A protected production environment shows both chips.

## 5. Elevation, density, iconography

- Cards: `surface` fill, `border`, radius `md`. Dialogs and sheets: radius `lg` and `shadow-md`.
- Table density: `comfortable` (row 48 px) default, `compact` (row 36 px, mouse only). Saved in `localStorage`.
- Icons: one outline icon set (Lucide, bundled, not loaded from a CDN), 16 and 20 px, stroke 1.5. Icons next to text are `aria-hidden`. Icon-only buttons have an `aria-label`.
- Illustrations: none required. Empty states use an icon and text.
