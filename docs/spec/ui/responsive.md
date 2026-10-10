# Responsive Rules

## 1. Breakpoints

| Class | Width (CSS px) | Typical device | Input |
|---|---|---|---|
| phone | 0–639 | Phone, portrait and landscape | Touch |
| tablet | 640–1023 | Tablet, small laptop window | Touch or mouse |
| laptop | 1024–1535 | Laptop, desktop | Mouse |
| large | 1536+ | Large monitor | Mouse |

The design target widths for tests are 360, 768, 1280 and 1920 px. Layout reacts to container and viewport width with CSS (Tailwind breakpoints `sm` 640, `lg` 1024, `2xl` 1536). Touch behavior reacts to `pointer: coarse`, not to width.

<a id="requirements"></a>

- **REQ-UI-016** (MUST) The layouts of sections 2 and 3 are implemented at the four breakpoints. At 360 px there is no horizontal page scroll. Only code blocks, log lines and tab strips scroll sideways, inside their own box. The UI is also usable in phone landscape (height 360 px) and at 200% zoom.
- **REQ-UI-017** (MUST) On touch input (`pointer: coarse`) every interactive target is at least 44 × 44 px, with at least 8 px between targets. With a mouse the minimum is 32 px.
- **REQ-UI-018** (MUST) Every data table becomes a card list on phone. A card shows the name, the state, two key fields and an action menu. Sorting and filtering stay available as a filter sheet.

## 2. Global layout per breakpoint

| Part | Phone | Tablet | Laptop | Large |
|---|---|---|---|---|
| Navigation | Bottom bar, 5 items: Home, Projects, Deploys, Nodes, More. "More" opens a sheet with Previews, Catalog, Alerts, Logs, Backups, Settings. | Icon rail, 64 px. Labels in tooltips. | Sidebar, 232 px, collapsible to the rail. | Same as laptop. Content max width 1600 px, centered. |
| Top bar | Context chip (current project and environment), search icon, bell, avatar. 56 px. | Same plus theme button. | Context switcher is in the sidebar. Top bar has search field, theme button, bell, avatar. | Same. |
| Context switcher | Full-screen sheet from the top bar. | Popover. | Popover in the sidebar. | Popover. |
| Page content | One column, padding 16 px. | Padding 24 px, 2 columns where noted. | Padding 32 px, up to 3 columns. | Padding 32 px, up to 4 columns, side panels shown. |
| Dialogs | Bottom sheet, full width, max height 90%, drag handle plus a close button (drag is never the only way). | Centered, max 560 px. | Centered, max 640 px. | Centered. |
| Toasts | Above the bottom bar, full width. | Bottom right. | Bottom right. | Bottom right. |
| Tabs | Scrollable strip. Domains, Volumes, Health, Backups and Settings are under "More". | Scrollable strip. | All tabs visible. | All visible. |
| Sticky actions | Primary action bar fixed above the bottom bar on deploy view, wizard and long forms. | Inline at the top of the content. | Inline. | Inline. |

## 3. Layout of every main screen

Each cell says what changes. "Same" means the same as the column to the left. The per-screen detail lines of [screens](screens.md) refine these.

| Screen | Phone (360) | Tablet (768) | Laptop (1280) | Large (1920) |
|---|---|---|---|---|
| Login, setup, onboarding (UI-01, 02, 31) | Single card, full width. | Card 480 px centered. | Same. | Same. |
| Dashboard (UI-03) | Summary cards 2 per row, lists as cards. | 4 summary cards in a row, lists in 2 columns. | 4 cards, recent deployments and node strip side by side. | Adds a live activity panel on the right. |
| Projects, environments (UI-04, 33) | Service cards, one per row. Environment switch is a select. | Service cards 2 per row. Environments as tabs. | Cards 3 per row or table. | Cards 4 per row. |
| Service wizard (UI-05, 32) | One step per screen, sticky Back/Next bar. | One step, side stepper hidden, top stepper. | Left stepper, form, right summary. | Same with a wider summary. |
| Service detail (UI-06, 09, 11–17) | Header collapses: name, state, Deploy; other actions in the menu. Tab strip scrolls. Cards stack. | Header full, tabs scroll. Overview in 2 columns. | Header, all tabs, Overview in 3 columns. | Overview adds a live metrics column. |
| Deploy dialog and deployment page (UI-07, 08) | Sheet. Deployment page one column, sticky Promote/Pause/Abort bar. | 2 columns: timeline and details. | Timeline, side panel (replicas, traffic). Build log collapsible. | Adds live metrics panel. |
| Releases and release diff (UI-09, 35) | Release cards. Diff is read-only summary, with "Open on a larger screen". | Table. Diff unified. | Table. Diff side by side. | Same, wider diff. |
| Promote (UI-10) | Sheet. Summary and the confirm. The diff table is shown as a read-only list. | Dialog with table. | Dialog with full table. | Same. |
| Variables and secrets (UI-11, 49) | Variable cards. Edit one at a time in a sheet. No bulk import. | Table. Bulk import allowed. | Table with inheritance column. | Same with more columns. |
| Domains, storage, health (UI-12, 13, 14) | Cards. Forms in sheets. | Tables, forms in dialogs. | Tables. | Same. |
| Logs and logs search (UI-15, 37) | Full-screen viewer, filters in a sheet, wrap on by default. | Viewer with filter bar. | Viewer, filter bar, context pane. | Viewer plus detail pane of the selected line. |
| Metrics (UI-16) | One chart per row, range select, table toggle. | 2 charts per row. | 2–3 charts per row. | 3–4 per row. |
| Terminal (UI-30) | "Open on a larger screen" notice. | Terminal full width with on-screen key row (Esc, Tab, Ctrl, arrows). | Terminal. | Terminal. |
| Backups and restore (UI-17, 38, 39) | Policy and run cards. Restore wizard works one step per screen. | Tables. | Tables. | Same. |
| Previews (UI-18, 36) | Cards with PR link, URL, expiry. | Table. | Table. | Same. |
| Nodes (UI-19, 40, 41) | Node cards with bars. Node actions in the menu. | Cards 2 per row. | Table, then node page. | Table with extra columns. |
| Catalog (UI-21) | Template cards, one per row, search on top. | 2 per row. | 3–4 per row. | 5–6 per row. |
| Alerts (UI-22, 48) | Alert cards, ack/mute in the menu. Rule editor is read-only with a notice. | Tables. Editor in a dialog. | Same. | Same. |
| Settings pages (UI-23–29, 43–45) | Settings list, then one page per section. SSO setup, audit diff and config as code are read-only with a notice. | List left, page right. | Same. | Same. |
| Error pages (UI-47) | Centered message and one action. | Same. | Same. | Same. |

## 4. Tables to cards

| Rule | Detail |
|---|---|
| When | Phone width, or a container narrower than 640 px. |
| Card content | Title (name), state badge, two key fields chosen per table (listed in [screens](screens.md)), action menu button (44 px). |
| Selection | A long press or a "Select" button starts multi-select. Bulk actions go in a bottom bar. |
| Sort and filter | One "Filter" button opens a sheet. The active filters show as removable chips. |
| Paging | Infinite scroll with a "Load more" button. Never only on scroll. |
| Same data | The card list uses the same query as the table. No data is dropped except low-priority columns. |

## 5. What a phone may not do

<a id="phone-limits"></a>

Allowed on phone (full function): read every screen; deploy; rollback; canary promote, pause, resume, abort; cancel a deployment; restart, stop, start; search and tail logs; alerts: view and mute; add a node (copy the install command); basic service wizard (image, template, git with defaults).

Desktop or tablet only (phone shows read-only content and a notice "Open on a larger screen" with a link that copies the page URL):

| Function | Why |
|---|---|
| Terminal (UI-30) | Needs a keyboard and space. Tablet gets a key row. |
| Release diff (UI-35) and promote diff editing | Wide tables. |
| Bulk env import, secret bulk edit | Large text input. |
| Config as code (UI-29) | Code editing and long plans. |
| SSO setup (UI-24), alert rule editor (UI-48), git connection setup (UI-27) | Long forms with many fields. |
| Audit diff viewer (UI-26) | Wide diffs. The list and filters work. |
| Wizard advanced step | Many fields. Defaults still work. |

A destructive action on phone uses the same typed confirmation as on desktop.

## 6. Touch and pointer details

- Hover is never required. Every tooltip also shows on focus and on long press.
- No action is drag-only. Reorder lists have move up and down buttons.
- Swipe gestures are shortcuts only (swipe a notification away) and always have a button.
- Sticky bars respect the safe area (`env(safe-area-inset-bottom)`).
- Inputs use `inputmode`, `autocomplete` and `enterkeyhint`, and font size 16 px on phone to avoid zoom on focus.
- Keyboard on phone: the focused field scrolls into view above the sticky bar.
- Pull to refresh is not used. Data is live.
