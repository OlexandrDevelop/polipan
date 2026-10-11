# Terminal Style (visual direction)

This file replaces the calm product look of [design-system](design-system.md) with a Linux terminal look. It changes the **skin and the interaction feel**, not the screens, data, roles or requirements. Every screen of [screens](screens.md) and every component of [components](components.md) stays. The map of changes is in [terminal-restyle-map](terminal-restyle-map.md).

Sources: tokens [tokens-terminal.json](../../design/tokens-terminal.json), prototype [ui-prototype-terminal.html](../../design/ui-prototype-terminal.html) (open it with `#svc` or `#dep` at the end of the URL for the other views). Token names match [design-tokens.json](design-tokens.json), so the build swaps the file and the CSS variables keep working.

## 1. Goal and decisions

A DevOps user should feel at home in the first second: a prompt, a status bar, keys, dense data. It must also be **comfortable**: fast, readable, calm when asked.

| # | Decision | Why |
|---|---|---|
| T1 | Monospace is the UI font (JetBrains Mono). Inter only for long text (help, empty-state paragraphs, audit descriptions, docs). | Columns line up, numbers compare at a glance, it looks like a terminal. One variable font file for UI keeps the font budget. |
| T2 | Corners are square (radius 0, 2 px at most). Lines are 1 px. Layers are shown by lines and a slightly lighter fill, not by shadow. | Crisp "pixel" look, cheap to paint, same in both themes. |
| T3 | Panels have a 1 px border and a **title notch** (`┤ services ├`) on the top line. The notch is real text on the border line, not box-drawing characters in content. | The TUI feel, with no cost for screen readers: the title is an `h2`. |
| T4 | A tmux-like **status bar** at the bottom (top on phone). A **prompt line** at the top is the command palette. | One place for context and one place for commands. |
| T5 | Dark theme "Phosphor": near-black with a green tint, accents green (primary), amber (warning), cyan (info). Light theme "Paper": warm off-white, ink text, dark green primary. | Signature look, restrained: one accent color for action, the rest only for state. |
| T6 | Status keeps icon + word: `● Running`, `▲ Deploying`, `✕ Failed`, `■ Stopped`, `⚡ Fast rollback`. Glyphs are the same set as [design-system](design-system.md#4-status-vocabulary). | [REQ-UI-013](../11-ui.md#4-rules-and-requirements) stays true. |
| T7 | Meters are text: `[||||      ] 41%`, like htop. The number is always in the text. | Accessible (the value is text), cheap, very "terminal". |
| T8 | Motion is small, fast and meaningful. Everything can be turned off by **Calm mode**. | "Alive" without noise. See section 5. |
| T9 | Effects (scanlines, glow, curvature) are optional, **off by default**, and never reduce legibility. | Wow for those who want it, safe for the rest. |
| T10 | Keyboard first: vim keys on top of the existing shortcuts, key hints next to actions. Mouse and touch keep full function. | Power users are fast, nobody is locked out. |

Not changed: URLs, roles, hide/disable rules, confirmations (typed name for destructive actions), the API, i18n keys, the hide/disable rule, the phone limits of [responsive](responsive.md#5-what-a-phone-may-not-do).

## 2. Look

### 2.1 Color

Two themes plus `system` (default, follows the OS live, same rules as [REQ-UI-015](design-system.md#requirements)). Values and measured contrast are in [tokens-terminal.json](../../design/tokens-terminal.json). Summary:

| Token | Dark "Phosphor" | Light "Paper" | Use |
|---|---|---|---|
| `bg` | `#0a0d0c` | `#fbf8f0` | Page |
| `surface` | `#101512` | `#f3efe3` | Prompt line, log area, palette |
| `raised` | `#161d19` | `#ebe6d6` | Status bar, hover |
| `text` | `#d5e0d9` | `#161b18` | Text |
| `muted` | `#8fa196` | `#4c5a52` | Labels, hints |
| `border` / `border-strong` | `#212a25` / `#5d7066` | `#d8d2c0` / `#6b756f` | Dividers (decor only) / panel and control borders |
| `primary` | `#4be08a` | `#0d6a37` | Action, focus, cursor, links |
| `amber`, `cyan` | `#f0b33c`, `#4cd3e6` | `#855300`, `#0a6577` | Warning, info |
| `danger` | `#ff7070` | `#b3261e` | Failed, production chip |
| `selection` | `#1c3a2b` | `#d3e8da` | Cursor row, active tab |

- Every text pair is at least 4.5:1 on `bg`, `surface` and `raised`; borders and chart colors at least 3:1 (values in the token file; the lowest text pair is `log-time` on `raised` in the light theme, 4.62; the lowest control pair is `border-strong` on `raised`, dark 3.25).
- Text on `selection` is also measured (text 9.17 dark / 13.56 light, muted 4.56 / 5.65).
- Chart set: 6 colors per theme, each also has a marker/dash shape ([components](components.md#4-metricchart)). Series use `c1`..`c6` = `chart[0..5]`.
- Glow and effects are never part of a contrast measurement and never carry meaning.
- `prefers-contrast: more` raises `border` to `border-strong` and the focus ring to 3 px. `forced-colors` uses system colors; glyphs keep the state.

### 2.2 Type

| Use | Font | Size / line |
|---|---|---|
| Everything in the UI: nav, labels, tables, logs, buttons, inputs, numbers | JetBrains Mono Variable | 13/20 laptop; 14/22 large; 14/22 phone body (16 for inputs) |
| Page title (`$ shop-web`) | Mono 700 | 20/28 (18/26 phone) |
| Big numbers (tiles) | Mono 700 | 28/36 |
| Long text (help, empty-state text, descriptions over 2 lines) | Inter Variable | 14/22 |

`font-variant-numeric: tabular-nums` always. Ligatures off in logs. Caps with 0.06em tracking only for table headers. Fonts are self-hosted woff2, Latin subset, `font-display: swap`, preloaded; total at most 120 KB ([REQ-UI-029](quality.md#3-performance-budget)). No third-party font request.

### 2.3 Space, lines, shape

- Horizontal space counts in `ch` (1 ch = one character): panel padding 1.5 ch, cell padding 1 ch. Vertical rhythm is the line height: table row 28 px on laptop (22 px compact), 40 px tablet, 48 px phone.
- 1 px solid lines. Panel: `1px solid border-strong`. Divider: `1px solid border`. Focus: 2 px `primary` outline plus (dark theme only, not in Calm mode) a soft glow.
- Radius 0 for panels, buttons, inputs and chips; 2 px allowed; `full` only for the live dot.
- Z layers as in [design-system](design-system.md#2-other-tokens) plus `scanlines` 60.

### 2.4 Signature parts

| Part | Rule |
|---|---|
| **Prompt line** (top, 36 px; phone: bottom, 56 px) | `$` in primary, breadcrumb `polipan / acme / shop / prod / page` (the last two in text color; each part is a link), then the command field `: type a command` with a block cursor and the hint `Ctrl K`. It is a button that opens the [command palette](navigation.md#4-command-palette-and-global-search). The breadcrumb is the `$`-style path; on tablet and phone it is hidden (the page `h1` and the status bar carry the context). |
| **Window bar** (under the prompt) | Main nav as tmux windows: `1:dashboard 2:service 3:deploy 4:nodes 5:more`. Number keys are not shortcuts (they clash with typing); `g` chords are. The current window has an underline and `selection` fill and `aria-current="page"`. Phone: bottom nav, 5 items, 48 px. |
| **Status bar** (bottom, 28 px; phone: top, 40 px) | Left: org chip, environment chip (`Production` has a `danger` border and is never hidden, [REQ-UI-020](navigation.md#context-switcher)), `● Live` / `▲ Reconnecting`, node health `● 3/3 nodes`. Right: user, clock (HH:MM:SS), `[theme:system]`, `[calm:off]`, `[fx:off]`, `[?]`. Each bracket item is a real button. The bar is a `footer` with an accessible name. The clock is not announced (`time` without live region). |
| **Panel** | Bordered box with a title notch (`h2`) and an optional hint on the right (`sort: cpu ↓`, `following`). Title is primary and bold. |
| **Key-hint chips** | `[d] deploy  [r] rollback  [l] logs` under tables and next to buttons. Chips are `kbd` elements, `aria-hidden` when the same action is a button (the button carries `aria-keyshortcuts`). Hidden on phone. |
| **Cursor row** | The current table row has `selection` fill and a `>` marker in the left margin. `j`/`k` move it, `Enter` opens it. It is a visual cursor on top of real links in the row, so Tab, mouse and screen readers work as in a normal table. |
| **Meters** | `[|||||     ] 41%`: 12 cells, color by value (normal `success`, from 70 % `warning`, from 90 % `danger`); the wrapper has `role="img"` and an `aria-label` with the value. |
| **Sparkline** | 1.5 px polyline, 20 px high, decorative (`aria-hidden`); the number next to it is the text. |
| **Buttons** | 1 px `border-strong`, no radius. Primary: filled `primary`, `on-primary` text, bold. Destructive: `danger` border and text. Pressed/selected: `selection` fill and `primary` border. The label may start with the key: `[d] Deploy`. |
| **Inputs** | 1 px border, `$`/`/`/`:` prefix glyph where it helps. 16 px on phone. Labels stay visible ([components](components.md#1-rules-for-all-components)). |
| **Dialogs** | Same panel border in `primary`, title notch, no radius. Phone: bottom sheet. Destructive dialogs keep the typed confirmation. |
| **Toasts** | Panel with `primary` border at the bottom right (above the bottom bars on phone). Same live-region rules as [components](components.md#2-components). |
| **Empty and error text** | Short, plain, with the next step: `No services yet.  [n] Add a service`. Error: `✕ Failed to load nodes. Retry [r]`. Error text is also in the page ([REQ-UI-032](components.md#requirements)). |

## 3. Command line and keyboard

- The prompt line is the command palette. Open with `Ctrl/Cmd+K` or `:` (when no input has focus). Prefix `>` commands only, `/` pages only, anything else searches all. The last row is always `? Search logs for "<text>"`. Roles, disabled reasons and the ARIA combobox pattern are as in [navigation](navigation.md#4-command-palette-and-global-search). Commands that need a target ask in a second step.
- The palette shows the key next to each row (`g d`, `d`, `t`). It opens in under 100 ms (no network before first paint; lists come from cache).
- Vim-style keys are **extra**; all keys of [REQ-UI-021](navigation.md#shortcuts) stay. New: `j` / `k` move the cursor row or the log line, `Enter` opens, `g d / g s / g p / g n` (dashboard, service, deploy, nodes) join the existing `g` chords, `c` toggles Calm mode, `x` cycles effects, `:` opens the palette, `gg` / `G` jump to first / last row (lists and logs).
- All of them are off while an input has focus, none runs a destructive action (`d` and `r` only open a dialog), and a user can turn single-key shortcuts off in UI-43. The `g` chord times out after 900 ms.
- The cursor blink (block caret in the prompt line) runs only on hover or focus, at most 10 times, then stays solid.

## 4. Data-dense views

| View | Design |
|---|---|
| **Dashboard "top"** (UI-03) | Panel `overview`: 4 tiles (big number, label, sparkline). Panel `services`: table with state, CPU meter, memory, req/s, version; sorted by CPU by default; cursor row. Panel `nodes`: CPU, memory, disk meters. Panel `activity`: last 5 events, newest first, time plus glyph plus text. Large screen adds a wide activity column. Refresh by SSE (`streamEvents`), patched in place. |
| **Service detail** (UI-06) | Title `$ shop-web`, state, environment chip, version chip, key-hint action buttons. Panels: `logs`, `metrics`, `info`. Laptop: logs left (3/5), metrics and info right (2/5). |
| **Deploy and timeline** (UI-07, 08, 34) | Panel `steps`: ordered list, one line per step: glyph, name, word (Done, Running, Pending, Paused), time. The active step has `selection` fill, `aria-current="step"`, a braille spinner (static `▲` in Calm mode). Panel `traffic`: see 5.2. |
| **Log viewer** (UI-15, 37) | Lines are a 3-column grid: time (muted), level glyph and word, text. Filters are toggle buttons (`info`, `warn`, `error`), search with highlight (background plus underline), `follow` toggle, `announce` toggle (off by default). New lines stream in (see 5.1). A gap marker line is `── gap: 12 lines skipped ──`. Rules of [components](components.md#3-logviewer) stay (virtualized, 10 000 lines, `role="log"`, pause for screen readers). Phone: one column, level glyph kept, time hidden, wrap on. |
| **Metrics** (UI-16) | Chart panel with a text summary above (`CPU: last 32 %, max 71 %`), 1 px grid lines, 1.5 px series, solid vs dashed series, legend below with line samples, `table` toggle in the panel hint. Release markers are dashed vertical lines with a label. Rules of [components](components.md#4-metricchart) stay. |
| **Tables** | Dense by default on laptop and large (28 px rows), `comfortable` on tablet and phone. Real `table` markup. On phone each row is a card: name, state, CPU meter ([REQ-UI-018](responsive.md#requirements)). |
| **Nodes** (UI-19, 40) | Same pattern as the services panel: state, CPU, memory, disk meters. Node detail adds a sparkline per resource. |

## 5. Motion

<a id="motion"></a>

General rules (**REQ-UI-038**):

1. Only `transform` and `opacity` animate (and `color`/`background-color` for 120 ms hover). No layout animation (no `width`, `height`, `top`; the traffic bar uses `transform: scaleX` in the build, the prototype uses a width transition for brevity and the build must not copy it).
2. Durations: `fast` 120 ms, `base` 200 ms, `slow` 320 ms. Loops (packets, cursor blink, spinner) are the only longer ones and are always tied to a live state.
3. Easing `cubic-bezier(.2,.8,.2,1)`; `linear` only for packet flow and the blink.
4. Every motion has a purpose: it shows **change** (row flash, counter), **live data** (stream, packets, spinner) or **focus** (cursor, glow).
5. Never delay real data. Animation catches up in one frame at most: the final text and numbers are in the DOM at once; the effect is only paint.
6. 60 fps on a mid laptop (Core i5 8th gen, integrated GPU) with 200 live rows and 1 000 log lines per second ingest ([REQ-UI-029](quality.md#3-performance-budget)). At most 12 concurrent animations. Off-screen and hidden-tab animations are paused (`IntersectionObserver`, `visibilitychange`).

| Effect | Where | Spec |
|---|---|---|
| **Boot** | First load of a session | Up to 6 lines `[ ok ] api reachable` appear 70 ms apart (total at most 600 ms) over `bg`. Any key or click skips. Shown once per browser session (`sessionStorage`). Never on deep links to a failure, never on login, never in Calm mode. The app is already loading under it; it never adds wait time. |
| **Stream / typewriter** | New log lines, new deploy steps, activity | Text is revealed by up to 4 characters per frame for the newest line only; older lines are instant. If more than 20 lines arrive in a second, all are instant. Auto scroll only in `follow`. |
| **Row flash** | A table row whose data changed | An overlay of `primary` at 28 % opacity fades out in `slow`. One-shot. |
| **Counter** | Tiles and big numbers | Value tweens in `base` (200 ms). Screen readers get the final value only. |
| **Cursor blink** | Prompt line on hover/focus | Block caret, 1.06 s, `steps(1)`, max 10 blinks. |
| **Live dot** | Status bar | Opacity pulse 2 s while connected; steady when reconnecting (color `warning`). |
| **Spinner** | Active deploy step | Braille frames every 90 ms (text swap, not layout). |
| **Packets** | Traffic bar | See 5.2. |
| **Theme change** | Switch | Instant, or at most `fast` color fade. |
| **Dialogs, palette** | Open/close | Opacity 0 → 1 and `translateY(-4px)` → 0 in `fast`. |

### 5.1 Streaming rules

The log and the activity list append at the bottom (log) or top (activity). The typewriter never blocks input, never delays the search highlight, and stops when the tab is hidden. Pausing the follow stops auto scroll and the typewriter at once.

### 5.2 Traffic bar (blue-green and canary)

- Two lanes: old version (info color packets) and new version (primary color packets), each a bordered track with a filled share (`selection` or primary tint) and a percent at the right. Text under the bars says the state: `Canary 25 %, step 2 of 4` (this text is also `aria-valuetext` of the `role="progressbar"`).
- **Packets**: small 8×6 px blocks move left to right along each track, `translateX` only, 1.6 s linear loop, staggered. The count per lane follows the split (6 packets total: at 25 % the new lane gets 2 and the old lane 4). Blue-green: all packets run in the blue lane until the switch; after the switch all run in the green lane. A switch is one visible jump of the fill in `slow`. A paused canary stops the packets (paused state is also text and `❚❚`).
- Canary gate: values in text (`error rate 0.3 % (max 1 %)`, `p95 212 ms (max 400)`, `● Gate passed`; a failing gate shows `✕ Gate failed` and the assertive announcement of [quality](quality.md#1-accessibility)).
- Calm mode: no packets; the percent, the fill and the text are the whole state.

## 6. Calm mode

<a id="calm"></a>

**REQ-UI-039.** Calm mode is a user switch (`[calm:on|off]` in the status bar, key `c`, palette command "Toggle Calm mode", and in UI-43) and it is **on automatically** when `prefers-reduced-motion: reduce` is set (the button then shows the reason in its title and cannot turn motion back on against the OS setting in v1).

In Calm mode:
- all animations and transitions are off (duration 0), no blink, no pulse, no spinner frames, no packets, no boot, no typewriter, no counter tween, no row flash, no smooth scroll;
- effects (scanlines, glow, curvature) are forced off and the focus glow is replaced by the plain 2 px outline;
- **no information is lost**: a changed row gets a dashed `primary` outline for 2 s (static), live state is the text `Live`, deploy steps show `▲`/`❚❚` glyphs, traffic shows numbers and text;
- the choice is saved in `localStorage` key `polipan.calm`.

Under `prefers-reduced-motion` the same rules apply even if the switch is off (see [REQ-UI-027](quality.md#requirements), which stays and is met by this mode).

## 7. Optional effects

<a id="effects"></a>

**REQ-UI-040.** One switch `[fx:...]` (key `x`, also a palette command and UI-43) cycles: off → scanlines → scanlines + glow → scanlines + glow + curvature. Default **off**. Saved in `localStorage` key `polipan.fx`.

| Effect | Rule |
|---|---|
| Scanlines | A fixed overlay, `pointer-events: none`, `aria-hidden`, 1 px dark line every 3 px at 6 % opacity (never above 8 %). Dark theme only. |
| Glow | A soft text shadow (8 px, 30 % of `primary`) on the page text and a stronger glow on focus and the live dot. Dark theme only. Never on text under 12 px. |
| Curvature | A vignette (inner shadow) and a 6 px frame radius. No warp or blur of text. Not below 1024 px. |
| Limits | All are off in Calm mode, in light theme (scanlines, glow), under `prefers-contrast: more`, under `forced-colors`, and in print. They must not change any measured contrast: the contrast test runs with effects **on and off** on screenshots (at least 4.5:1 sampled on body text). |

## 8. Responsive behavior

<a id="responsive"></a>

Width classes and test widths are as in [responsive](responsive.md#1-breakpoints) (360, 768, 1280, 1920). The terminal look adapts as follows. **REQ-UI-042.**

| Part | Phone (360) | Tablet (768) | Laptop (1280) | Large (1920) |
|---|---|---|---|---|
| Frame | Full screen. Order top to bottom: status bar, content, window bar (bottom nav), prompt line (bottom command bar). | Prompt line top, window bar, content, status bar bottom. Rail is replaced by the window bar. | Same order, breadcrumb visible in the prompt line. | Same as laptop, content centered, max width 1920 (frame) and 1600 for text-heavy pages. |
| Prompt line | Bottom, 56 px, tap opens the palette as a full-screen sheet. No keyboard hint. Safe-area padding. | Top, 36 px, hint `Ctrl K`. | Top with breadcrumb. | Same. |
| Status bar | Top, two lines: chips and live state, then `[theme] [calm] [fx]`. Time, user, nodes count and `[?]` hidden. Buttons 44 px. | One line, all items. | One line, all items. | One line, all items. |
| Data | Cards with mono data: name (bold), state at the right, CPU meter below. Low-priority columns hidden ([REQ-UI-018](responsive.md#requirements)). Row cards 48 px+. | Table, 40 px rows, hide the lowest column. | Dense table, 28 px rows, all columns. | Dense table plus a live activity panel on the right, 4-column panel grid. |
| Panels | One column; title notch kept; padding 1 ch. | Two columns where noted. | `c2` (3:2) and `c3` grids. | Up to 4 columns. |
| Log | Full screen viewer, one column (glyph + text), filters in a sheet or a wrapped tool row, wrap on. | Time + level + text. | Time + level + text, context pane on demand. | Plus detail pane of the selected line. |
| Keys | Touch first: key hints hidden, actions are 44 px buttons; vim keys work with a hardware keyboard. | Hints visible with a keyboard (`pointer: fine`). | Hints visible. | Hints visible. |
| Effects | Scanlines and glow allowed; curvature off. | Curvature off. | All. | All. |

Rules: no horizontal page scroll at 360 px ([REQ-UI-016](responsive.md#requirements)); touch targets 44 px on `pointer: coarse` ([REQ-UI-017](responsive.md#requirements)); a monospace line that is longer than the screen wraps (logs, wrap on) or scrolls inside its own box (code); never shrink the font below 12 px; 16 px for inputs on phone. The 200 % zoom and 400 % reflow checks of [quality](quality.md#1-accessibility) apply.

## 9. Limits the build must keep

<a id="limits"></a>

**REQ-UI-043.** These are gates, in addition to [REQ-UI-029](quality.md#3-performance-budget) and [REQ-UI-030](quality.md#4-ui-test-plan) (not replacing them).

| Area | Limit | Check |
|---|---|---|
| Initial JS | Within 250 KB gzip as before. The skin adds at most 6 KB gzip of JS (boot, effects switch, keyboard layer). No new runtime dependency. | Bundle gate |
| CSS | Within 60 KB gzip. | Bundle gate |
| Fonts | 2 families, woff2, Latin, at most 120 KB total, self-hosted, `font-display: swap`, preloaded. The mono file is the one used above the fold. | Bundle gate, network test |
| Third party | Zero requests to another host (fonts, icons, CDN, analytics). | Playwright network log |
| CSP | Strict: no `'unsafe-inline'` and no `'unsafe-eval'` in `script-src`; no inline `<script>`, no inline event handler; styles from files only (no inline `style` attribute that is needed for layout; dynamic values go through CSS custom properties set by `element.style.setProperty`, which CSP allows for CSSOM). The theme/calm/fx flags are set before first paint by the same-origin `/theme-init.js` ([REQ-UI-015](design-system.md#requirements)). Effects use CSS only, no `eval`, no data URL images. | CSP test |
| Accessibility | axe: zero serious or critical issues in both themes, with effects on and off, at 360, 768, 1280 and 1920 px. Contrast test reads [tokens-terminal.json](../../design/tokens-terminal.json) and recomputes every pair. | axe, Vitest |
| Keyboard only | Every flow of [quality](quality.md#4-ui-test-plan) works without a mouse; focus ring visible in both themes on `bg`, `surface` and `raised`; the palette traps focus and returns it; vim keys never fire in inputs. | Playwright |
| Screen readers | Panel titles are headings; meters, traffic and status have text values; the live log is silent unless "announce" is on; the prompt line and window bar have names; the clock is not live. | Manual plus axe |
| Motion | 60 fps on a mid laptop (no dropped frames over 2 s of stream with 200 live rows); only `transform` and `opacity`; at most 12 concurrent animations; paused when hidden. Reduced motion and Calm mode: zero running animations. | Playwright trace, `getAnimations()` |
| Latency | Interaction (key to visible change, palette open, tab switch) under 100 ms; INP at most 200 ms. | Web-vitals in e2e |
| Boot | At most 600 ms, skippable, once per session, never delays first content. LCP unchanged (2.0 s laptop). | Lighthouse CI |
| Effects | Default off; contrast test passes with effects on. | Screenshot test |
| No loss | Every function of [screens](screens.md) and [parity](parity.md) is still present. | Existing e2e suite runs unchanged on the new skin |

## 10. Migration for the build task

1. Swap the token file (`design-tokens.json` → `tokens-terminal.json`; names are compatible, new names are additive) and set `--radius-*` to the new values. Old `shadow-md` maps to `popover`.
2. Ship fonts and `/theme-init.js` (sets `data-theme`, `data-calm`, `data-fx` before paint).
3. Restyle `ui/` wrappers by the [restyle map](terminal-restyle-map.md). Screens are not touched except where the map says "layout".
4. Add the new parts: StatusBar, PromptLine (opens CommandPalette), WindowBar, Panel, Meter, KeyHint, CursorRow behavior in DataTable, CalmSwitch, FxSwitch.
5. Update visual baselines; run the full e2e on the old and the new skin to prove nothing was lost.
6. A user setting "Skin: Terminal | Classic" is **not** planned. If the team wants a fallback, tokens already allow it (swap file, keep `ui/` wrappers); this is an open remark, not a requirement.

## 11. Requirements

<a id="requirements"></a>

These extend [REQ-UI-014](design-system.md#requirements)..[REQ-UI-033](screens.md#requirements); nothing is renumbered or removed. Where a new requirement is stricter, both apply. Test names contain the id ([REQ-UI-030](quality.md#4-ui-test-plan)).

- **REQ-UI-034** (MUST) The UI uses the terminal visual language of sections 1, 2 and 4: monospace UI font, 1 px lines, square corners, titled panels, text meters, glyph status. Every value comes from [tokens-terminal.json](../../design/tokens-terminal.json). No hard-coded color or size.
- **REQ-UI-035** (MUST) A status bar (org, environment with the production mark, live state, node health, user, clock, theme, Calm, effects, help) and a prompt line that opens the command palette are on every page after login. On phone the status bar is at the top and the prompt line at the bottom.
- **REQ-UI-036** (SHOULD) Vim-style keys (`j`, `k`, `Enter`, `gg`, `G`, new `g` chords, `:`) and key-hint chips exist on top of [REQ-UI-021](navigation.md#shortcuts). They are off in inputs, never run a destructive action, and can be turned off.
- **REQ-UI-037** (MUST) A dark theme "Phosphor" and a light theme "Paper" plus `system` follow-mode are built only from the token file. Every text pair on `bg`, `surface` and `raised` passes 4.5:1, every control border and chart color 3:1. A CI test recomputes the pairs from the token file.
- **REQ-UI-038** (MUST) Motion follows section 5: only `transform` and `opacity`, 120–320 ms (loops only for live state), boot at most 600 ms, skippable and once per session, 60 fps on a mid laptop, never delays data.
- **REQ-UI-039** (MUST) Calm mode (user switch and automatic under `prefers-reduced-motion`) removes all motion and all effects and loses no information (section 6).
- **REQ-UI-040** (SHOULD) Scanlines, glow and curvature exist behind one switch, default off, never reduce legibility, and are off in Calm mode, light theme (scanlines, glow), high contrast and forced colors (section 7).
- **REQ-UI-041** (SHOULD) The data-dense views of section 4 exist: the "top" dashboard with sparklines and meters, the deploy timeline with the traffic bar (blue-green and canary), the log viewer with search and filters, and the metrics panels, with all the rules of [components](components.md) kept.
- **REQ-UI-042** (MUST) The terminal look is responsive as in section 8 at 360, 768, 1280 and 1920 px, with no horizontal page scroll at 360 px and touch targets of 44 px.
- **REQ-UI-043** (MUST) The limits of section 9 (budgets, fonts, no third party, strict CSP, axe, keyboard, 60 fps, 100 ms) are CI gates for the terminal skin.
