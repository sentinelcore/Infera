# DePINClaw Execution Plan

The plan runs on two parallel tracks. Track A sells inference from week 2, using rented capacity at first. Track B builds the contributor network and moves paid traffic onto it as nodes come online. The same API and customers serve both tracks, so revenue does not wait for supply.

Timeline: 24 weeks to public launch. Weeks are relative to kickoff.

---

## Phase 0: Foundations and day-one revenue (weeks 0–2)

**Build**
- Monorepo, CI, and a staging and production environment (see `ARCHITECTURE.md` §9–10).
- `gateway` v0: OpenAI-compatible `/v1/chat/completions` and `/v1/embeddings`, API keys, metering, and Stripe prepaid credits.
- Fallback router to 2 rented providers (e.g. Together and DeepInfra), with per-model price tables.
- Landing page with waitlist, referral codes and two sign-up paths: "Contribute a device" and "Get API access".

**Revenue track**
- Launch the paid API as soon as it works, backed 100% by rented capacity and priced at provider cost plus margin.
- Apply for grants and credit programmes: Solana Foundation, Base Builder Grants, peaq, IoTeX, NVIDIA Inception, AWS/GCP/Azure startup credits, and Cloudflare for Startups.
- List the team's own spare GPUs on Vast.ai, Salad or Nosana to cover early infra cost.

**Accounts to open (lead times vary)**
- Apple Developer (Organization), Google Play Console, and a Windows code-signing certificate (EV preferred, for SmartScreen reputation).
- Stripe (plus Stripe Connect for later payouts), and a domain with Cloudflare.
- X account `@DePINClaw`, Discord server, and a Telegram group.

**Exit criteria:** first paid API call; waitlist live; grant applications submitted.

---

## Phase 1: Desktop node alpha (weeks 2–6)

**Build**
- Agent core: identity, transport, probe, governor, engine (llama.cpp sidecar) and models.
- `nodehub`, `scheduler` v1 (route to a node if a warm one exists, otherwise fall back), and `ledger` v0 (receipts and points).
- Tauri tray app for Windows, macOS (Apple Silicon first) and Linux, with autostart and silent auto-update.
- Headless install script for Linux and macOS.
- Contributor dashboard v0: devices, online hours, jobs served and points.
- Verifier v0: canary prompts.

**Revenue track**
- Launch a **Batch API** (async, 24h SLA, 50% cheaper), which nodes serve first.
- Launch an **Embeddings API** at low price, with high volume and a good fit for weak nodes.
- Start outbound sales to 20 AI startups and agent builders for batch workloads: evals, synthetic data, classification and document processing.

**Exit criteria:** 20 internal/friends machines online; 10% of batch traffic served by nodes; canary pass rate ≥ 99%.

---

## Phase 2: Closed beta on X, desktops and laptops (weeks 6–10)

**Build**
- Hardening: crash supervisor, staged rollouts, and per-OS pause triggers (fullscreen and game detection).
- Reputation scoring, plus redundant re-runs on 3% of jobs.
- Leaderboard, referral rewards and points multipliers for early contributors.
- Customer console: usage graphs, invoices and API-key scopes.

**Beta waves** (details in `BETA_LAUNCH_X.md`)
- Wave 1 (week 6): 100 contributors with NVIDIA GPUs or Apple Silicon Macs ≥ 16 GB.
- Wave 2 (week 8): 500 contributors, adding AMD GPUs, Linux and CPU-only laptops for embeddings.

**Revenue track**
- Apply to list on **OpenRouter** as a provider once uptime and latency are stable.
- **Private deployments:** offer companies the same node software on their own office machines for in-house inference, with a setup fee plus monthly licence. Target 2 paid pilots.
- **Design-partner contracts:** prepaid reserved batch capacity with 1–3 customers.

**Exit criteria:** 300+ weekly active nodes; ≥ 40% of batch and embeddings traffic served by nodes; 7-day node retention ≥ 60%; first paid pilot signed.

---

## Phase 3: Android and earnings (weeks 10–15)

**Build**
- Android app: Rust core via uniffi, llama.cpp JNI, a foreground service with WorkManager constraints, and boot restart.
- Phone workloads: embeddings, Whisper-small **Transcription API**, and 1–3B batch chat.
- Earnings v1: points convert to a USD balance in the ledger from real job revenue (operator share of e.g. 70%, a config value).
- Payouts v1: Stripe Connect (fiat) and USDC on Solana or Base, weekly, above the threshold, with KYC at the payout threshold.
- Play Integrity attestation for Android nodes.

**Revenue track**
- Launch the Transcription API (priced per audio minute).
- **Paid tier in the app:** a private on-device assistant (chat with local models, no cloud) as a monthly subscription. It uses the same engine and adds non-network revenue.

**Exit criteria:** Android on Play Store (closed testing track to open testing); 1,000+ weekly active nodes across platforms; first real payouts sent.

---

## Phase 4: iOS, public beta and OpenRouter (weeks 15–20)

**Build**
- iOS app with Charge Mode (foreground while charging, and `BGProcessingTask` overnight), via TestFlight.
- MLX backend on macOS for higher tokens/sec.
- Hedged interactive routing, so T1/T2 nodes serve real-time chat.
- `privacy: private` routing tier.
- Public status page and network stats page (nodes online, tokens served).

**Revenue track**
- Live on OpenRouter as a provider.
- Volume pricing tiers and annual prepaid plans.
- **Node licences:** decide on and, if approved by legal counsel, sell a limited number of licences that carry reward multipliers.

**Exit criteria:** public beta (no waitlist); 5,000+ registered devices; nodes serve ≥ 50% of total paid tokens.

---

## Phase 5: Public launch and on-chain (weeks 20–24+)

**Build**
- On-chain anchoring of receipt batches (Merkle root per epoch) on the chosen chain (Solana or Base).
- Token design and legal review. Token rewards top up operator earnings only while demand ramps; real job revenue remains the base.
- TPM / App Attest-backed attestation tiers, and staking and slashing for high-value job tiers.
- App Store release on iOS, and Android open release.

**Revenue track**
- Enterprise reserved-capacity contracts.
- Chain ecosystem partnerships and co-marketing (grants, featured app placement).

**Exit criteria:** public launch; positive gross margin on node-served traffic; monthly payouts running automatically.

---

## Revenue tracks by phase (summary)

| Phase | Revenue sources live |
|---|---|
| 0 | Paid API on rented capacity, grants and credits, own GPUs on Vast/Salad/Nosana |
| 1 | + Batch API, Embeddings API, direct startup sales |
| 2 | + OpenRouter application, private deployment pilots, design-partner prepaid contracts |
| 3 | + Transcription API, paid on-device assistant tier |
| 4 | + OpenRouter live, volume and annual plans, node licences (if approved) |
| 5 | + Enterprise reserved capacity, chain partnerships, token |

---

## Team (minimum)

| Role | Owns |
|---|---|
| Rust / systems engineer | Agent core, desktop app, llama.cpp integration |
| Backend engineer (Go) | Gateway, nodehub, scheduler, ledger, infra |
| Mobile engineer | Android first, then iOS |
| Full-stack engineer | Web, dashboards, Stripe, payouts |
| Founder / growth | X, Discord, sales, grants, partnerships |

---

## Metrics dashboard (track weekly)

| Area | Metric |
|---|---|
| Supply | Registered devices, weekly active nodes, available node-hours, 7- and 30-day node retention, crash rate |
| Quality | Canary pass rate, redundant-run agreement, p50/p95 TTFT, job failure rate |
| Demand | Paying customers, tokens served, % served by nodes vs fallback |
| Money | Revenue, gross margin (node-served vs fallback), operator payouts, cost per 1M tokens |
| Growth | Waitlist size, referral rate, X followers and engagement, Discord members |

---

## Week-1 checklist

- [ ] Create monorepo skeleton (`agent/`, `desktop/`, `control-plane/`, `web/`, `infra/`)
- [ ] Register domain, Cloudflare, X, Discord and Telegram handles
- [ ] Open Apple Developer, Google Play and Stripe accounts; order the code-signing certificate
- [ ] Gateway v0 with one fallback provider and Stripe credits
- [ ] Landing page with waitlist and referral codes
- [ ] Submit grant and credit applications
- [ ] Post the announcement thread on X (see `BETA_LAUNCH_X.md`)
