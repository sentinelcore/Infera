# Requirements

Status: ✅ done · 🔨 in progress · ⬜ not started

## v1 beta (desktops and laptops)

| ID | Requirement | Status |
|---|---|---|
| NODE-01 | Pair a device with a 6-digit code | ✅ |
| NODE-02 | Benchmark on install and assign class A/B/C | ✅ |
| NODE-03 | Engine adapter: OpenAI-compatible local server or managed llama-server | ✅ |
| NODE-04 | Offer cards (ask mode) with 30 s countdown; auto-approve mode | ✅ |
| NODE-05 | Safe stop: finish then offline; Ctrl-C drain; Stop now with penalty warning | ✅ |
| NODE-06 | Idle rules: CPU load from other programs, mains power | ✅ |
| NODE-07 | Idle rules: keyboard/mouse inactivity | ✅ |
| NODE-08 | Start at login as a background service (`infera service install`) | ✅ |
| NODE-09 | Tray app (Tauri) with installers | ⬜ Phase 3 |
| CP-01 | OpenAI-compatible `/v1/chat/completions` with streaming | ✅ |
| CP-02 | Sealed jobs (X25519 + ChaCha20-Poly1305), Ed25519 receipts | ✅ |
| CP-03 | Scheduler by model, privacy, class, dependability, mode | ✅ |
| CP-04 | Rented-capacity fallback | ✅ |
| CP-05 | Ledger with holds, multipliers, debits, suspensions | ✅ |
| CP-06 | Test prompts (canaries) that catch wrong answers | ✅ |
| CP-07 | Penalty decay: half after 30 days, dropped after 90 | ✅ |
| CP-08 | Stripe prepaid credits for developers | ⬜ Phase 2 |
| CP-09 | Postgres + multi-region | ⬜ Phase 4 |
| PAY-01 | USDT wallet with address checks and 48 h hold | ✅ |
| PAY-02 | Weekly payout batch + mark sent | ✅ |
| PAY-03 | Automated USDT sending (Fireblocks/BitGo or exchange API) | ⬜ Phase 2 |
| OPS-01 | Docker + HTTPS deploy, CI, release builds | ✅ |
| OPS-02 | Repo references point at the renamed repository (sentinelcore/Infera) | ✅ |

## v2 (phones, privacy, scale)

| ID | Requirement | Phase |
|---|---|---|
| MOB-01 | Android app: foreground service, offer cards via push, M1/M2 classes | 5 |
| MOB-02 | smolmaxxing engine for MoE-from-flash on phones | 5 |
| MOB-03 | iPhone Charge Mode | 6 |
| PRIV-01 | Attested sandbox: sealed level (App Attest, Play Integrity, SIP) | 6 |
| PRIV-02 | Confidential level (Android pVM, confidential GPUs) | 7 |
| CHAIN-01 | On-chain receipt anchoring | 7 |

## Out of scope for now
Token launch, staking, node licences (need legal review first).
