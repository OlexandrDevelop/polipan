# Component Library

Base: shadcn/ui (Radix primitives) wrapped in a Polipan `ui/` package. Screens use only these wrappers ([README rule 4](README.md#rules-for-web-build-agents)). Every component reads [tokens](design-tokens.json), has all states below, passes axe with zero serious issues in both themes ([quality](quality.md#4-ui-test-plan)), and works with keyboard alone.

## 1. Rules for all components

- Visible focus ring (2 px `primary`). Target size per [REQ-UI-017](responsive.md#requirements).
- Every input has a visible label. Placeholder is never the label. Help and error text are linked with `aria-describedby`. Invalid fields set `aria-invalid`.
- Status is icon plus word ([design-system](design-system.md#4-status-vocabulary)).
- All text goes through i18n keys ([quality](quality.md#2-internationalization)).
- States every component documents: default, hover, focus, active, disabled, loading, error (where it applies). Disabled controls that a user may wonder about carry a tooltip with the reason, reachable by keyboard.
- No motion longer than `base` unless it shows progress. Reduced-motion removes all motion.

## 2. Components

<a id="requirements"></a>

| Component | Variants | States | A11y rules |
|---|---|---|---|
| Button | primary, secondary, ghost, destructive; sizes md, lg (touch) | loading (spinner plus text kept, width fixed), disabled | Native `button`. Loading sets `aria-busy`. Destructive is never the default focus in a dialog. |
| IconButton | ghost, secondary | as Button | Required `aria-label`. Tooltip on focus and hover. |
| Input, Textarea | text, password, number, search | error, readonly, disabled | Label, `autocomplete`, `inputmode`. Password has a show/hide button with `aria-pressed`. |
| Select, Combobox | single, multi, async search | empty, loading, error | ARIA combobox pattern. Typeahead. Native select on phone for plain lists. |
| Switch, Checkbox, Radio | - | indeterminate (checkbox) | Switch has `role="switch"`. State is also text ("On", "Off") when it controls a risk. |
| SecretInput | new, replace | "Set, v3" (read), editing | Write-only input ([REQ-UI-005](../11-ui.md#4-rules-and-requirements)). The input has no reveal button. Owners and admins read a stored value with the "Show" button on the secret row (a separate window, audited). Value cleared after save. Autocomplete off. |
| KeyValueEditor | rows, bulk paste (not on phone) | row error, duplicate key | Each row labeled "Name, row 3". Add and remove by buttons. Paste of `.env` text. |
| StatusBadge | per state of [design-system](design-system.md#4-status-vocabulary), sizes sm, md | live (updates with polite announcement) | Icon plus word. Color is never alone. |
| EnvironmentBadge | production, staging, preview, custom, protected | - | Text "Production" for the kind; lock and text "Protected" for the protected flag (any kind). Same rule as [design-system](design-system.md#4-status-vocabulary). |
| Tabs | line, scroll, with overflow menu | selected, disabled | ARIA tabs. Arrow keys. `[` and `]` shortcuts. Scroll buttons are real buttons. |
| DataTable | sortable, filterable, selectable, column hide, sticky header, row actions | loading skeleton rows, empty, error, selection bar | Real `table` markup, `scope` on headers, `aria-sort`. Row action menu is a button per row. Virtualized above 200 rows keeps `aria-rowcount`. Becomes CardList on phone ([REQ-UI-018](responsive.md#requirements)). |
| CardList | service card, node card, release card | as DataTable | `ul` of `article`. Whole card is not a link when it has inner actions; the title is the link. |
| Card | surface, interactive | hover, focus | Interactive cards have one focus target. |
| Dialog, Sheet | dialog, bottom sheet (phone), side panel | opening, closing, busy | Focus trap, Esc closes, focus returns to the opener, `aria-labelledby` and `aria-describedby`. Scroll lock on body. |
| ConfirmDialog | simple, typed name | confirm disabled until the typed name matches | Typed-name input labeled with the exact text to type. Paste allowed. Destructive button text names the action. |
| Toast | success, info, error | auto close 5 s (error stays until closed) | Polite live region (assertive for errors). Pause timer on hover and focus. Has a close button. Never the only copy of an error ([REQ-UI-032](#requirements)). |
| Banner | info, warning, danger, production | dismissible or fixed | `role="status"` or `alert` by severity. Contains the next step. |
| EmptyState | with action, with link | - | Heading plus one action ([REQ-UI-010](../11-ui.md#4-rules-and-requirements)). |
| Skeleton | line, card, table, chart | - | `aria-hidden`; the container has `aria-busy`. |
| Wizard, Stepper | linear, with summary | step done, current, error, disabled | Steps are an ordered list; the current one has `aria-current="step"`. Back keeps data ([REQ-UI-031](#requirements)). Focus moves to the step heading on change. |
| CommandPalette | - | empty, loading, no result | Combobox pattern, see [navigation](navigation.md#4-command-palette-and-global-search). |
| ContextSwitcher | sidebar, sheet | loading, empty | Listbox with search. Kind badge in each row. |
| Timeline | deploy steps, vertical | step pending, active, done, failed, skipped | Ordered list. The active step has `aria-current`. Each step has icon, word and time. Changes announce politely. |
| TrafficBar | blue-green, canary | gate pass, gate fail, paused | `role="progressbar"` with `aria-valuenow` and text "Canary 25 %, step 2 of 4". Gate values in text. |
| LogViewer | live, search | following, paused, disconnected, empty | See section 3. |
| MetricChart | line, area, bars | loading, no data, error | See section 4. |
| Sparkline | - | no data | Decorative; the number next to it is the text. |
| TerminalPane | - | connecting, connected, closed | See section 5. |
| DiffViewer | side-by-side, unified | no change, large (collapsed) | `+` and `-` marks plus color. Secret values `***`. Copy. Keyboard: next and previous change. |
| CodeBlock, CopyField | - | copied | Copy button announces "Copied" politely. Block is focusable for horizontal scroll. |
| Countdown | - | expired | Text only ("Expires in 52 min"), updates each minute for screen readers, each second visually. |
| RoleGate | hide, disable | - | Implements the [hide/disable rule](screens.md#11-roles-and-permission-behavior). Disabled shows a reason tooltip. |
| LiveIndicator | connected, reconnecting | - | Text "Live" or "Reconnecting". |
| ThemeSwitch | menu, cycle button | - | Radio group with System, Light, Dark. |
| Avatar | image, initials | - | `alt` is the name or empty when the name is next to it. |
| Tooltip | - | - | Shows on focus too. Never holds the only copy of important text. |

- **REQ-UI-031** (MUST) A wizard keeps all entered data when the user goes back or forward. Leaving a wizard or a form with unsaved changes asks for confirmation (browser `beforeunload` and router blocker). A failed submit keeps all data.
- **REQ-UI-032** (MUST) A toast never carries the only copy of an error. Every error also appears in the page (banner or field text) with the reason and a retry or a next step.

## 3. LogViewer

<a id="logviewer"></a>

- **REQ-UI-026** (MUST) The log viewer and the metric charts have accessible alternatives: the log viewer can be paused so a screen reader can read it, and every chart can switch to a data table.

| Item | Rule |
|---|---|
| Rendering | Virtualized list. Smooth at 10 000 lines. Keeps at most 10 000 lines in memory; older lines load from `getServiceLogs` and `searchLogs`. |
| Follow | "Following" by default; scrolling up pauses; a "Jump to latest" button resumes. |
| Live region | `role="log"`. Polite announcements are **off** by default (too noisy) and a "Announce new lines" toggle turns them on. While paused nothing is announced. |
| Lines | Time (user timezone, tooltip UTC), replica, level icon plus word, text, ANSI colors mapped to theme-safe colors. |
| Wrap | Toggle. On by default on phone. |
| Search | Highlight with a background and an underline (not color alone), next and previous match with `Enter` and `Shift+Enter`. |
| Copy | Copy line, copy selection, download visible range. |
| Masking | `***` is shown as is. |
| Gap | A gap marker line when the stream skipped data. |
| Disconnected | Banner and the "Reconnecting" indicator. Lines stay. |
| Keyboard | The viewer is focusable; `j` and `k` move line focus; `Space` toggles follow; `/` focuses search. |

## 4. MetricChart

| Item | Rule |
|---|---|
| Series | Up to 6 per chart. Colors from the validated chart set, plus distinct marker shape and dash style. |
| Axes | Time axis in user timezone. Units on the axis and in the tooltip. |
| Tooltip | On hover, focus and touch. A shared crosshair shows all series at one time. |
| Legend | Buttons with the series name and shape. Toggling a series is by button, not only by click on the line. |
| Release markers | Vertical lines with a label ("v42"), focusable, linked to the release. |
| Table view | A toggle "Show as table" shows the same data in a real table. |
| Range | 1 h, 6 h, 24 h, 7 d, 30 d, 90 d and custom, limited by retention. |
| Empty | "No data in this range." |
| Summary | A text line above the chart gives min, max, last ("CPU: last 32 %, max 71 %"). |

## 5. TerminalPane

- xterm-based. Loaded only on the terminal screen (lazy chunk).
- A visible notice before connect: "This session is recorded in the audit log."
- Keyboard trap warning: the terminal captures keys. `Esc Esc` (two presses within 1 s) moves focus out. The hint is shown under the pane and is read by screen readers.
- On tablet a key row (Esc, Tab, Ctrl, arrows) is shown. On phone the pane is not offered ([responsive](responsive.md#5-what-a-phone-may-not-do)).
- Reconnect does not replay input. Closed state shows the exit code and a "Reconnect" button.
- Font size setting (12–20 px). Colors from the theme.
- Screen reader users get a "Use a read-only transcript" toggle that shows the output as text lines.
