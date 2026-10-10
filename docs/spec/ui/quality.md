# Accessibility, Internationalization, Performance and UI Tests

## 1. Accessibility

Target: WCAG 2.1 AA for everything, and WCAG 2.2 AA for target size and focus where it is stricter ([REQ-UI-008](../11-ui.md#4-rules-and-requirements)).

| Area | Rule |
|---|---|
| Keyboard | Every function works with the keyboard alone. Tab order follows the visual order. No keyboard traps except the terminal, which has a documented exit (`Esc Esc`). Visible focus ring on every focusable element. |
| Landmarks | `header`, `nav` (labeled), `main`, `aside` where used. A "Skip to content" link is the first focusable element. |
| Headings | One `h1` per page. No skipped levels. |
| Route change | Focus moves to the `h1` of the new page, and the document title changes. A polite live region announces "Loaded: <title>". |
| Live regions | Deploy state changes: polite. Deploy failure, canary gate failure and connection loss: assertive, once per event. Log lines: off by default ([components](components.md#3-logviewer)). |
| Names | Icon-only controls have `aria-label`. Status icons are paired with text. Links say where they go. |
| Forms | Labels, `aria-describedby` for help and errors, an error summary at the top of long forms with links to the fields. Required fields are marked with text, not only an asterisk. |
| Color | Never alone. Contrast per [design-system](design-system.md#1-color-tokens-and-contrast). `prefers-contrast: more` and `forced-colors` are supported. |
| Zoom | Works at 200% zoom, and reflows at 400% (equal to 320 px width) without loss of function. |
| Targets | Per [REQ-UI-017](responsive.md#requirements). |
| Alternatives | Charts have a table view and a text summary. Logs can be paused. The terminal has a transcript mode. Diffs use marks plus color. |
| Drag | No function needs dragging. |
| Time | Nothing times out without a warning. Toasts with actions stay until closed. |
| Language | `<html lang>` follows the UI language. |

<a id="requirements"></a>

- **REQ-UI-027** (MUST) Reduced motion is respected. With `prefers-reduced-motion: reduce` all transitions and animations are off (duration 0), skeletons do not pulse, the live indicator does not blink, charts do not animate, and smooth scrolling is off. No information is lost: state changes are still shown, only without motion.

## 2. Internationalization

- **REQ-UI-028** (SHOULD) The UI is ready for more languages while only English ships in v1 ([REQ-UI-009](../11-ui.md#4-rules-and-requirements)): strings use i18next keys named by screen (`deploy.dialog.title`); no string is built by concatenation, plurals and selects use ICU message format; dates, times, numbers, sizes and relative times use `Intl`; times are stored and sent in UTC and shown in the user's timezone with UTC in the tooltip; layout allows 40% longer text; CSS uses logical properties (`margin-inline-start`) so right-to-left can be added; icons that show direction can be mirrored; no text is part of an image. A lint rule fails the build on a hard-coded user-visible string in a component.

| Item | Rule |
|---|---|
| Source of keys | One JSON resource file per language in `web/src/locales/<lang>/` (path chosen by M0, see [02](../02-architecture.md#3-repository-layout-monorepo-to-be-created-by-m0)). English is the source. |
| Missing key | Falls back to English. A CI check lists missing keys per language. |
| Server messages | The API returns an error `code`. The UI maps the code to its own text and shows the server `message` only as detail. |
| Names | Product terms (release, deployment, node) are keys too, so a language can choose its word. |

## 3. Performance budget

- **REQ-UI-029** (MUST) The budgets below are CI gates: a build that is over a budget fails.

| Metric | Budget | How it is checked |
|---|---|---|
| Initial JavaScript | ≤ 250 KB gzip (shell, router, API client, dashboard route) | Bundle-size gate on the Vite build |
| Lazy chunks | Terminal, charts, diff viewer, log viewer, wizard: each ≤ 150 KB gzip | Same gate |
| CSS | ≤ 60 KB gzip | Same gate |
| Fonts | 2 families, woff2, subset Latin plus Latin-ext, ≤ 120 KB total, `font-display: swap`, preloaded | Same gate |
| LCP | ≤ 2.0 s on a mid laptop; ≤ 3.5 s on a mid phone over 4G throttling | Lighthouse CI on dashboard and service page |
| INP | ≤ 200 ms | Lighthouse CI / web-vitals in e2e |
| CLS | ≤ 0.1 | Lighthouse CI |
| Log viewer | 60 fps scrolling at 10 000 lines; ingest 1 000 lines per second without dropping frames | Playwright performance test |
| Tables | Virtualized above 200 rows; first paint of 1 000 rows ≤ 200 ms | Component perf test |
| Live data | One SSE connection per page (multiplexed with `topics`); reconnect with backoff and `Last-Event-ID` | e2e |
| Assets | Hashed file names, `Cache-Control: immutable` for assets, `no-cache` for `index.html`; compressed by the server | e2e header test |
| Requests | No request to a third-party host ([REQ-UI-012](../11-ui.md#4-rules-and-requirements)) | e2e test that fails on any foreign host |
| Data | TanStack Query with stale-while-revalidate; SSE events patch the cache instead of refetching lists | Review |

## 4. UI test plan

<a id="4-ui-test-plan"></a>

- **REQ-UI-030** (MUST) The UI test plan below is implemented. Test names contain the `REQ-UI-NNN` id or the `AC-*` id they check. Visual and accessibility tests run for both themes and at four widths.

| Level | Tool | What | When |
|---|---|---|---|
| Unit and component | Vitest and Testing Library | Every component of [components](components.md) in each state and variant; permission behavior of RoleGate; theme logic (system follow, no flash); i18n lint | Every push |
| Contrast | Vitest reading [design-tokens.json](design-tokens.json) | Every pair of [design-system](design-system.md#1-color-tokens-and-contrast) passes; chart set passes ([REQ-UI-014](design-system.md#requirements)) | Every push |
| Accessibility | axe in every component test and every e2e page | Zero serious or critical issues in light and dark | Every push |
| API contract | Generated client plus mock from `openapi.yaml` | Screens call only existing operations; CI check of [REQ-UI-023](screens.md#requirements) and [REQ-UI-024](parity.md#requirements) | Every push |
| End to end | Playwright against a real server | Scenarios `T-E2E-*` of [13](../13-testing-and-quality.md#4-cross-feature-e2e-scenarios) through the UI; flows of [flows](flows.md) | Merge and nightly |
| Keyboard-only | Playwright without mouse | Deploy, rollback, promote, add node and restore with keyboard only | Merge |
| Visual regression | Playwright screenshots | Key screens × {light, dark} × {360, 768, 1280, 1920} px (list below) | Merge |
| Responsive | Playwright | No horizontal page scroll at 360 px on every screen ([REQ-UI-016](responsive.md#requirements)); touch targets ≥ 44 px on a touch profile ([REQ-UI-017](responsive.md#requirements)); tables become cards | Merge |
| Reduced motion | Playwright with `reducedMotion` | No running animation ([REQ-UI-027](#requirements)) | Merge |
| Theme | Playwright with `colorScheme` | System mode follows the emulated scheme live; choice persists; no flash on reload | Merge |
| Performance | Lighthouse CI, bundle gate | Budgets of section 3 | Merge |
| CSP | Playwright with console and CSP report | No CSP violation on any screen; built `index.html` has no inline script; `script-src` has no `'unsafe-inline'` ([REQ-UI-015](design-system.md#requirements)) | Merge |
| No third-party | Playwright network log | No foreign host ([REQ-UI-012](../11-ui.md#4-rules-and-requirements)) | Merge |
| Live behavior | Playwright with the server paused | Disconnected banner appears and clears; no manual refresh needed ([REQ-UI-002](../11-ui.md#4-rules-and-requirements)) | Merge |

Key screens for visual tests: login, dashboard (with data and empty), project page, wizard (each path, one step each), service overview, deployment page (blue-green and canary, running and failed), releases and rollback dialog, promote dialog with missing variables, variables and secrets, logs, metrics (chart and table view), backups and restore wizard, nodes and node detail, catalog, alerts, audit, members, SSO, tokens, command palette, error pages. Each state class (empty, loading, error, disconnected) is captured at least once per page family.

Data for tests: fixtures generated from the OpenAPI examples, fixed clock, fixed random seed, fonts loaded before capture, animations off. A visual diff above 0.1% of pixels fails and needs a human to approve the new image.

Test names: `REQ-UI-019 palette lists only allowed commands`, `REQ-UI-015 system theme follows live`.
