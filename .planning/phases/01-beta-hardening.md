# Phase 1 – Beta hardening

## Tasks
| # | Task | Req | Status |
|---|---|---|---|
| 1 | Canary test prompts: easy questions with known answers sent to idle devices; failures penalized as wrong results | CP-06 | ✅ |
| 2 | Penalty decay job: +half of each penalty back at 30 days, the rest at 90 | CP-07 | ✅ |
| 3 | Keyboard/mouse idle time: macOS (IOHIDSystem), Windows (GetLastInputInfo), Linux (xprintidle if present) | NODE-07 | ✅ |
| 4 | `infera service install/uninstall/status`: systemd user unit, launchd LaunchAgent, Windows logon task | NODE-08 | ✅ |
| 5 | Tests and docs updated | – | ✅ |

## Design notes
- **Canaries:** run through the normal job path (sealed, offer, receipt) using an internal system account: no charge, no pay. One per idle device every 30 min (configurable). A device fails when the expected answer isn't in the output. Two failures in the last three canaries → `wrong_result` penalty (no debit, since no job value). Each pass adds a `canary_pass` event.
- **Decay:** hourly job over `score_events` with negative deltas; flags `decayed_30` / `decayed_90` prevent double-counting.
- **Input idle:** default 5 minutes without input before new jobs (config `governor.idle_minutes`); when idle time can't be read, fall back to CPU rule only.

## Result
- Canaries: honest engine passed 3/3; wrong-answer engine penalized on the 2nd failure, suspended after the score fell below 30.
- Decay: unit test confirms +half at 30 days, full at 90, no double counting.
- Input idle: macOS IOHIDSystem, Windows GetLastInputInfo (compile-checked), Linux xprintidle.
- Service: systemd user unit / LaunchAgent / logon task; SIGTERM finishes the running job (25 s) before exit, tested with no penalty.
- Not verifiable here: macOS and Windows runtime behaviour (no machines in this environment); needs a tester on each.
