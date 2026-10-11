# UI/UX Design Specification

This folder is the full design specification of the Polipan web panel. It extends [11-ui](../11-ui.md). Build agents can make the whole UI from these files, the [OpenAPI file](../openapi.yaml) and the [prototype](../../design/ui-prototype.html).

The stack is fixed in [02-architecture](../02-architecture.md#2-tech-stack-and-reasons): React 18, TypeScript, Vite, TanStack Router and Query, Tailwind CSS, shadcn/ui, REST + SSE + one WebSocket (terminal). This folder adds no new runtime dependency to it.

## Files (read in this order)

| # | File | Content | Requirements defined |
|---|---|---|---|
| 1 | [principles](principles.md) | Users, tone, design principles | - |
| 2 | [design-system](design-system.md) + [design-tokens.json](design-tokens.json) | Tokens, themes, contrast, theme switch rules, type, motion | REQ-UI-014, REQ-UI-015 |
| 3 | [responsive](responsive.md) | Breakpoints, layout per screen, touch, phone limits | REQ-UI-016, REQ-UI-017, REQ-UI-018 |
| 4 | [navigation](navigation.md) | Site map, URLs, switcher, command palette, search, shortcuts | REQ-UI-019, REQ-UI-020, REQ-UI-021 |
| 5 | [screens](screens.md) + [api-map](api-map.md) | All screens: data, actions, states, roles, layout | REQ-UI-022, REQ-UI-023, REQ-UI-025, REQ-UI-033 |
| 6 | [parity](parity.md) | Easypanel parity matrix and extra features | REQ-UI-024 |
| 7 | [components](components.md) | Component library, states, variants, a11y | REQ-UI-026, REQ-UI-031, REQ-UI-032 |
| 8 | [flows](flows.md) | Key flows as diagrams | - |
| 9 | [quality](quality.md) | Accessibility, i18n, performance budget, UI test plan | REQ-UI-027, REQ-UI-028, REQ-UI-029, REQ-UI-030 |
| 10 | [wireframes](wireframes.md) | Low-fidelity wireframes | - |
| 11 | [terminal-style](terminal-style.md) + [terminal-restyle-map](terminal-restyle-map.md) + [tokens-terminal.json](../../design/tokens-terminal.json) | Terminal look and feel: visual direction, motion, Calm mode, effects, responsive, limits, restyle map of every component and screen group | REQ-UI-034..REQ-UI-043 |

The older requirements REQ-UI-001 to REQ-UI-013 stay in [11-ui](../11-ui.md#4-rules-and-requirements). Ids are never renumbered: screens `UI-01`..`UI-30` keep their ids, new screens are `UI-31`..`UI-49`, new requirements are `REQ-UI-014`..`REQ-UI-043` (034..043 are the terminal style).

## Design source files

| File | Use |
|---|---|
| [design-tokens.json](design-tokens.json) | The token file. A copy of `docs/design/tokens.json`. The build reads `design-tokens.json` here; it is the source for the web skeleton. |
| [docs/design/tokens-terminal.json](../../design/tokens-terminal.json) | Tokens of the terminal style (light and dark, measured contrast). Names are compatible with `design-tokens.json`; the build task swaps the file. |
| [docs/design/ui-prototype-terminal.html](../../design/ui-prototype-terminal.html) | Clickable prototype of the terminal style: status bar, prompt line and palette, dashboard, service with live log, deploy with animated blue-green and canary traffic, theme switch, Calm mode, effects, 360 px layout. Open with `#svc` or `#dep` to start on another view. |
| [docs/design/ui-prototype.html](../../design/ui-prototype.html) | Clickable low-fidelity prototype (theme switch, phone layout, command palette, canary deploy view, rollback dialog). Only Overview, Releases, Env and secrets, Logs and Metrics tabs are drawn. The written spec is the source for all other screens. |

## Rules for web build agents

1. Colors, spacing, radius and type come only from the tokens. No hard-coded color or pixel value in a component ([REQ-UI-014](design-system.md#requirements)).
2. Every screen is built from the entry in [screens](screens.md): data, actions, all states, role behavior, layout at four widths.
3. Use only `operationId` values that exist in [openapi.yaml](../openapi.yaml). If an operation you need does not exist, do not invent it: write the gap in your task. Known gaps are in [screens](screens.md#known-api-gaps).
4. Build components in [components](components.md) first. A screen may not define its own button, table or dialog.
5. Name each test with the `REQ-UI-NNN` id (see [quality](quality.md#4-ui-test-plan)).
6. Work in parallel: after the M0 web skeleton, screens do not depend on each other. Agents develop against a mock API generated from `openapi.yaml`, so a screen can be built before its server milestone is done. Milestones and work packages are in [14](../14-roadmap-and-milestones.md).

## Words used in this folder

| Word | Meaning |
|---|---|
| Phone, tablet, laptop, large | The four width classes of [responsive](responsive.md#1-breakpoints). |
| Protected environment | An environment with `protected = true` ([glossary](../01-glossary.md)). |
| Hide / disable rule | A role that can never do an action does not see it. A state or a protected environment that blocks it shows it disabled with a reason ([REQ-UI-004](../11-ui.md#4-rules-and-requirements)). |
| Live | Updated by SSE without reload ([REQ-UI-002](../11-ui.md#4-rules-and-requirements)). |
