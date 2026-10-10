# Design Principles and Tone

## 1. Users

| User | Wants | UI must give |
|---|---|---|
| Solo developer | Speed. Few clicks from push to live URL. | Wizard with good defaults, one-click deploy and rollback, command palette. |
| Small team | Safety and clarity: who did what, what is live. | Protected environments, release history, audit log, roles, plan before action. |
| Ops person | State of nodes, alerts, backups, audit. Rescue from a phone. | Live status, node and alert views, logs, restart and rollback on a phone. |

## 2. Principles

1. **Show the real state, live.** State comes from the server through SSE. The user never presses refresh ([REQ-UI-002](../11-ui.md#4-rules-and-requirements)). If live updates stop, the screen says so.
2. **Plan before action.** Deploy, rollback, promote, move and restore first show what will happen (the dry run) and then ask for confirmation.
3. **Production is always visible.** An environment of kind production has a red top border and a "Production" chip on every page that belongs to it. A protected environment of any kind has a red top border, a lock and a "Protected" chip.
4. **Safe by default, fast for experts.** Defaults are safe. The command palette and keyboard shortcuts remove clicks for people who know the panel.
5. **Never color alone.** Every state has an icon and a word ([REQ-UI-013](../11-ui.md#4-rules-and-requirements)).
6. **The phone is for watching and rescue.** Deploy, rollback, restart, logs and alerts work on a phone. Heavy editing is for larger screens ([responsive](responsive.md#5-what-a-phone-may-not-do)).
7. **One place per thing.** One component per job. One word per concept, taken from the [glossary](../01-glossary.md).
8. **Quiet and calm.** No decoration without a job. Motion shows change and stops under reduced-motion.

## 3. Tone of text

- Plain, short, exact. Active voice. The word of the glossary, not a synonym ("release", not "version").
- Say what will happen before it happens: "This starts a new release and keeps the old one running until it is healthy."
- Buttons are verbs with the object when it matters: "Deploy to production", "Roll back to v41".
- Errors say **what failed**, **why**, and **the next step**: "The health check failed on node `nod-eu1`: port 8080 did not answer in 30 s. Check the port in Health settings, then deploy again."
- No jokes, no blame, no exclamation marks, no emoji in the product text.
- Empty states say what the screen is for and give one clear action ([REQ-UI-010](../11-ui.md#4-rules-and-requirements)).
- Numbers: show units, use `Intl` formats ([quality](quality.md#2-internationalization)).
- Destructive actions name the object and the effect: "Delete service `api`. Its 2 volumes are kept as orphan volumes."

## 4. Quality bar ("very comfortable and modern")

| Area | Rule |
|---|---|
| Speed | Interactions respond in 100 ms or show a skeleton. Budget in [quality](quality.md#3-performance-budget). |
| Density | Comfortable by default, compact option for tables on laptop and large. |
| Feedback | Every action shows progress, result and a way back (undo is a rollback, not a toast). |
| Consistency | The same state looks the same on every screen. Same dialog shape for every destructive action. |
| Reachability | Every screen has a stable URL and a palette entry. |
| Forgiveness | Wizards keep data when going back. Leaving with unsaved data asks first ([REQ-UI-031](components.md#requirements)). |
