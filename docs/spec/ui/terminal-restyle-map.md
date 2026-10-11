# Terminal Restyle Map

What changes in each component of [components](components.md) and each screen group of [screens](screens.md) and [responsive](responsive.md#3-layout-of-every-main-screen) when the [terminal style](terminal-style.md) is built. One line each. "Keeps" means the rules of the old file stay as they are. Function, states, roles, API calls and requirement ids never change.

## 1. Components

| Component | Restyle |
|---|---|
| Button | Square, 1 px `border-strong`; primary is a filled `primary` block with bold text; label may start with the key (`[d] Deploy`); loading shows `…` frames (static in Calm mode); keeps states and `aria-busy`. |
| IconButton | Square 1 px box; glyph icons (Lucide, bundled) stay; tooltip also shows the key; keeps `aria-label`. |
| Input, Textarea | 1 px border, no radius, mono text, optional prefix glyph (`$`, `/`, `:`); focus = 2 px outline plus glow (dark, not Calm); keeps labels and errors. |
| Select, Combobox | Same box as Input; list is a bordered panel with `selection` fill on the active option; keeps ARIA combobox and native select on phone. |
| Switch, Checkbox, Radio | Text form: `[x]`, `[ ]`, `(•)`, `( )`, and `[on ]` / `[off]` for switches, drawn with real inputs and CSS; keeps `role="switch"` and the On/Off word. |
| SecretInput | Value shown as `••••••••` mono; state text `Set, v3`; keeps write-only rule, no reveal. |
| KeyValueEditor | Dense rows 28 px, `=` between key and value in `muted`; keeps row labels, bulk paste. |
| StatusBadge | Glyph + word in state color, no pill and no fill: `● Running`, `▲ Deploying`, `✕ Failed`, `■ Stopped`, `⚡ Fast rollback`; keeps icon + word. |
| EnvironmentBadge | Square chip with 1 px border; `Production` has a `danger` border; `Protected` adds a lock glyph; keeps the red top mark of [REQ-UI-020](navigation.md#context-switcher). |
| Tabs | tmux window style: `1:dashboard` with underline and `selection` fill for the current tab; keeps ARIA tabs, arrows, `[` `]`. |
| DataTable | Dense 28 px rows, caps muted headers, cursor row with `>` and `selection` fill, `j`/`k`/`Enter`, key-hint line under the table, row flash on update; keeps real table markup, sort, virtualization. |
| CardList | Card = bordered box with title notch off; mono data, name bold, state right, CPU meter; keeps `ul` of `article`. |
| Card | Becomes Panel (below) where it groups data; plain bordered box elsewhere; keeps one focus target. |
| Dialog, Sheet | Square, `primary` 1 px border, title notch, opens in `fast`; phone sheet keeps the drag handle and a close button; keeps focus trap. |
| ConfirmDialog | Same; the typed-name input is prefixed `>`; destructive button has `danger` border; keeps typed confirmation. |
| Toast | Bordered panel (`primary`), mono, `✕` for errors; keeps timers, live regions and [REQ-UI-032](components.md#requirements). |
| Banner | Full-width bordered strip with a glyph and the next step; `danger` for production; keeps roles. |
| EmptyState | Short mono text plus one key-hint action (`No services yet. [n] Add a service`); no illustration; keeps heading plus action. |
| Skeleton | Rows of `░` blocks (static in Calm mode, no pulse); keeps `aria-busy`. |
| Wizard, Stepper | Steps as `[1]──[2]──[3]` line with the current step in `primary`; keeps ordered list, `aria-current`, data kept on back. |
| CommandPalette | Becomes the prompt line `:`; overlay panel with prefix modes `>` and `/`; keys shown per row; keeps combobox rules of [REQ-UI-019](navigation.md#command-palette). |
| ContextSwitcher | Breadcrumb in the prompt line (`acme / shop / prod`), each part opens the switcher; status bar shows org and env chips; keeps search, kind badge, production mark. |
| Timeline | Step list with glyph, word, time; active step `selection` fill and spinner; keeps ordered list and polite announcements. |
| TrafficBar | Two lanes with fill and moving packets, percent and text state; keeps `role="progressbar"`, gate values in text; no packets in Calm mode. |
| LogViewer | 3-column mono grid, level glyph and word, streaming new lines, toggle filters, highlight with background and underline; keeps virtualization, `role="log"`, announce off by default. |
| MetricChart | 1 px grid, 1.5 px series from `c1`..`c6`, dash styles, text summary, table toggle; keeps all rules of [components](components.md#4-metricchart). |
| Sparkline | 1.5 px polyline in `primary`/`cyan`/`amber`; decorative; number beside it. |
| TerminalPane | Already a terminal: border and title notch like other panels, theme colors from tokens; keeps the `Esc Esc` exit and transcript mode. |
| DiffViewer | `+` / `-` marks stay, `success` and `danger` text on tinted rows; mono; keeps next/previous change. |
| CodeBlock, CopyField | Bordered `surface` block with a `[copy]` button; keeps polite "Copied". |
| Countdown | Text `expires in 52 min`; no change in behavior. |
| RoleGate | No visual change; disabled shows `[d] Deploy` dimmed with a reason tooltip. |
| LiveIndicator | `● Live` / `▲ Reconnecting` in the status bar; dot pulses in `base` motion only (steady in Calm mode). |
| ThemeSwitch | `[theme:system]` button in the status bar cycling System → Light → Dark; also in UI-43 and the palette; keeps the radio group in the menu. |
| Avatar | Square with initials; keeps `alt` rule. |
| Tooltip | Bordered mono box; shows on focus; keeps "never the only copy". |
| **New** StatusBar | Footer with org, env, live state, node health, user, clock, theme/calm/fx buttons, help. See [terminal-style](terminal-style.md#24-signature-parts). |
| **New** PromptLine | Header with `$`, breadcrumb and the command field; opens CommandPalette. |
| **New** WindowBar | Main navigation as windows (phone: bottom nav). Replaces the sidebar and rail. |
| **New** Panel | Bordered box with title notch (`h2`) and optional right hint. |
| **New** Meter | Text meter `[||||    ] 41%` with `role="img"` and label. |
| **New** KeyHint | `kbd` chip row; `aria-hidden` when a button already has `aria-keyshortcuts`. |
| **New** CalmSwitch, FxSwitch | Status bar buttons with `aria-pressed`; see [Calm mode](terminal-style.md#6-calm-mode) and [effects](terminal-style.md#7-optional-effects). |

## 2. Screen groups

Layout rows are in [responsive](responsive.md#3-layout-of-every-main-screen); only the change is listed. The sidebar and top bar of [responsive](responsive.md#2-global-layout-per-breakpoint) are replaced by PromptLine, WindowBar and StatusBar on all widths.

| Screen group (ids) | Restyle |
|---|---|
| Login, setup, onboarding (UI-01, 02, 31) | Boot-style card: `$ login` title, bordered panel, input prefix glyphs; no WindowBar or StatusBar until login (a minimal status line shows version and `Live`); theme follows system. |
| Dashboard (UI-03) | "top" view: overview tiles with sparklines, services table with cursor row and meters, nodes and activity panels. |
| Projects, environments (UI-04, 33) | Service list as dense table with cursor row (cards on phone); environment as tabs `prod | staging | previews`; production mark kept. |
| Add service, wizard (UI-05, 32) | Chooser as a list with key hints (`[g] git  [i] image  [t] template  [d] database`); wizard stepper as a line; summary panel. |
| Service detail (UI-06, 09, 11–17) | Title `$ name`, state, chips, key-hint actions; panels for logs, metrics, info; tab strip as windows. |
| Deploy dialog and deployment page (UI-07, 08, 34) | Steps panel with spinner and times; traffic panel with packets and gate text; sticky action bar on phone. |
| Releases, diff, promote (UI-09, 10, 35) | Release table with cursor row; diff with mono marks; promote table with `+`/`-` rows; phone keeps read-only notice. |
| Variables and secrets (UI-11, 49) | `KEY = value` mono rows, secret rows `KEY = ••••••••  Set, v3`; version panel as a list. |
| Domains, storage, health (UI-12, 13, 14) | Dense tables and forms in panels; certificate state as glyph + word. |
| Logs and logs search (UI-15, 37) | Streaming viewer with toggle filters, `/` search, `follow` toggle; search results as the same grid. |
| Metrics (UI-16) | Panel grid of charts with text summaries and a `table` toggle. |
| Terminal (UI-30) | Same pane, same panel frame and theme; the `Esc Esc` hint sits in the status line; phone keeps the notice. |
| Backups and restore (UI-17, 38, 39) | Tables with state glyphs; restore wizard as line stepper; result as a checklist `● ok`. |
| Previews (UI-18, 36) | Table of previews with expiry countdown text. |
| Nodes (UI-19, 40, 41) | Nodes table with CPU, memory, disk meters; detail adds sparklines; failover history as a log-style list. |
| Catalog (UI-21) | Template cards as bordered boxes with mono name and one-line text (Inter for the description); search with `/`. |
| Alerts (UI-22, 48) | Alerts table with state glyph; rule editor in a panel (read-only on phone); mute by `[m]`. |
| Settings pages (UI-23–29, 43–45) | Settings list as a window list; each page is a panel set; UI-43 gets the Appearance panel: theme, **Calm mode**, **effects**, single-key shortcuts. |
| Notifications and palette (UI-42, 46) | Bell becomes `[3 alerts]` in the status bar with a panel list; palette is the prompt line overlay; help is the shortcuts dialog. |
| Error pages (UI-47) | `$ cat error` style: code, message in mono, one action, key hint. |

## 3. Checklist for the reviewer of the build task

- Every row of both tables is done or has a written reason.
- Existing REQ-UI tests pass on the new skin without changes to their expectations.
- The new REQ-UI-034..043 each have a test named with the id ([quality](quality.md#4-ui-test-plan)).
