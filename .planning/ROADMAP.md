# Roadmap

| Phase | Goal | Requirements | Status |
|---|---|---|---|
| 0 | Beta build: server, node, installers, tests | NODE-01..06, CP-01..05, PAY-01..02, OPS-01 | ✅ done |
| 1 | Beta hardening: trust and install-and-forget | NODE-07, NODE-08, CP-06, CP-07 | ✅ done |
| 2 | Money in and out: Stripe credits; USDT via partners (manual) | CP-08, PAY-03 | ✅ code done; needs Stripe test keys |
| 3 | Tray app with signed installers (Windows, macOS, Linux) | NODE-09 | 🔨 tray done; signed installers next |
| 4 | Scale: Postgres, regions, status page, OpenRouter listing | CP-09 | ⬜ |
| 5 | Android node with offer cards and smolmaxxing | MOB-01, MOB-02 | 🔨 app built; on-phone testing next |
| 6 | iPhone Charge Mode; sealed privacy level | MOB-03, PRIV-01 | ⬜ |
| 7 | Confidential level; on-chain receipts | PRIV-02, CHAIN-01 | ⬜ |

## Phase 1 success criteria
1. A node that returns wrong answers is detected by test prompts and penalized within a day.
2. Old penalties lose half their weight at 30 days and drop off at 90.
3. The node pauses new jobs within 2 s of keyboard or mouse use (macOS, Windows, Linux X11).
4. `infera service install` makes the node start at login on all three desktop OSes; `uninstall` reverses it.
