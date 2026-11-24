# Infera Architecture

A network where laptops, desktops and phones serve AI inference on idle capacity. Contributors install once, and the app runs only when the device is idle, then pauses itself. Customers call one OpenAI-compatible API.

## 1. System overview

```
 Customers / apps / OpenRouter
            │  HTTPS (OpenAI-compatible: /v1/chat/completions, /v1/embeddings, /v1/audio/transcriptions)
            ▼
 ┌──────────────────────────── CONTROL PLANE (cloud) ─────────────────────────────┐
 │  API Gateway ── auth, API keys, rate limits, usage metering, Stripe billing   │
 │       │                                                                        │
 │  Scheduler ── picks node by model / latency / reputation; retries; hedging     │
 │       │                       │                                                │
 │  Node Registry (Redis)   Fallback Router ──► rented capacity (Together,        │
 │  live state of nodes                         Fireworks, DeepInfra, Vast/RunPod) │
 │       │                                                                        │
 │  Verifier ── canary prompts, redundant re-runs, reputation scores               │
 │  Ledger (Postgres) ── signed receipts → points → USDT payouts → on-chain        │
 │  Model CDN (Cloudflare R2) ── signed GGUF/MLX model files                       │
 │  Update server ── signed agent/app updates                                     │
 └───────────────▲─────────────────────────────────────▲──────────────────────────┘
                 │ outbound WSS :443 (node dials out;  │
                 │ no port forwarding / NAT config)    │
   ┌─────────────┴───────────┐          ┌──────────────┴──────────────┐
   │ Desktop / Laptop node   │          │ Phone node                  │
   │ Tauri tray app          │          │ Android: Kotlin + fg service│
   │  └ Rust agent core      │          │ iOS: Swift, charging-only   │
   │     └ llama.cpp / MLX   │          │  └ Rust agent core (uniffi) │
   │ Headless: systemd/launchd│         │     └ llama.cpp / MLC        │
   └─────────────────────────┘          └─────────────────────────────┘
```

## 2. Components

### 2.1 Agent core (Rust, shared by every platform)
One library, compiled for Windows, macOS, Linux, Android (JNI via `uniffi`) and iOS (`uniffi` Swift bindings).

| Module | Responsibility |
|---|---|
| `identity` | Ed25519 keypair created on first launch and stored in OS keychain/Keystore. Linked to the user's account with a 6-digit pairing code. |
| `transport` | Persistent outbound WebSocket (TLS, port 443) to the nearest control-plane region. Reconnects with backoff, and a heartbeat every 15s. |
| `probe` | Install-time benchmark that assigns the device class (§3.1). Re-runs after driver, OS or hardware changes, monthly, or when live speed drifts more than 20%. |
| `governor` | Decides when the node may work, using the idle rules in §3, and enforces the resource caps. |
| `engine` | Engine adapters (llama.cpp, llama.cpp + smolmaxxing, MLX, MNN-LLM, LiteRT-LM, Genie, bitnet.cpp, vLLM) behind one interface; the fastest engine per model is picked at benchmark time. See `ON_DEVICE_INFERENCE.md`. |
| `sandbox` | Runs every engine inside the secure job sandbox and holds the per-job decryption key (§3.3). |
| `models` | Downloads models from the CDN on unmetered networks only, verifies SHA-256 plus the signature, and manages a disk quota (default 20 GB desktop, 4 GB phone). |
| `jobs` | Accepts or declines job offers, runs them, streams output, and produces a signed receipt. |
| `updater` | Checks the signed update manifest daily and installs silently when idle. |

**Inference backends**

| Platform | Backend |
|---|---|
| Windows / Linux, NVIDIA/AMD/Intel GPU | llama.cpp `llama-server` sidecar (CUDA / Vulkan builds) |
| macOS Apple Silicon | llama.cpp (Metal) in v1; MLX in v2 |
| CPU-only laptops | llama.cpp CPU, used only for embeddings and small models |
| Android | llama.cpp via JNI (CPU + Vulkan/OpenCL); MLC LLM as an alternative backend |
| iOS | llama.cpp (Metal) |

**Security rule:** nodes run only the bundled inference engine against signed models from our CDN. No containers and no arbitrary customer code. This keeps the app safe to install and keeps app-store reviews simple.

### 2.2 Desktop / laptop app
- **Tauri tray app** (Rust backend plus a small web UI) shows status, earnings, a pause toggle and settings.
- **Installers:** `.msi` (Windows, code-signed), `.dmg` (macOS, Developer ID and notarized), `.deb`/`.rpm`/AppImage (Linux).
- **Autostart at login:** Tauri autostart plugin, launchd LaunchAgent on macOS, Run key on Windows, XDG autostart on Linux.
- **Silent auto-update:** Tauri updater with signed manifests.
- **Headless install** for servers and always-on desktops: `curl -fsSL https://get.infera.xyz | sh` installs a systemd (Linux) or launchd (macOS) service, and pairs with `infera login`.

### 2.3 Android app
- Kotlin UI plus the Rust core and llama.cpp via JNI.
- **Foreground service** (type `dataSync`/`specialUse`) with a persistent notification. Started by WorkManager under these constraints: charging, unmetered network, battery not low, and device idle.
- Pauses on thermal status at `THERMAL_STATUS_MODERATE` or above, on screen-on with user activity, or when unplugged.
- Restarts after reboot via `BOOT_COMPLETED`, which keeps it install-and-forget.
- Distributed through Google Play, plus a direct APK for beta testers.
- Workloads: 1–3B LLMs, embeddings and Whisper-small transcription.
- Uber-style job offer cards, or auto-approve (see §3.2).

### 2.4 iOS app
- iOS blocks sustained background compute, so the app uses **Charge Mode**: it works while the phone is plugged in, either with the app open (screen dimmed, like a nightstand mode) or via `BGProcessingTask` with `requiresExternalPower = true`, which iOS schedules overnight.
- Install-and-forget on iOS means work happens overnight while charging. Expect lower supply per device than Android.
- Distributed via TestFlight for beta, then the App Store.
- Uber-style job offer cards, or auto-approve (see §3.2). Offers are sent only while the phone is in Charge Mode.

### 2.5 Control plane (Go services, one repo, deployed per region)

| Service | Responsibility |
|---|---|
| `gateway` | OpenAI-compatible API, API keys, per-key rate limits, token metering, Stripe usage billing and prepaid credits. |
| `nodehub` | Terminates node WebSockets and maintains the live registry in Redis (model loaded, state, tokens/sec, region, reputation). |
| `scheduler` | Matches requests to nodes, retries on failure, and can hedge interactive requests across two nodes and take the first response. Falls back to rented capacity when no node meets the SLA. |
| `verifier` | Canary jobs with known outputs (greedy decoding, fixed seed), re-runs 2–5% of jobs on a second node, and updates reputation. |
| `ledger` | Stores signed receipts and computes points, operator earnings and payouts. Postgres is the source of truth; batches are anchored on-chain in a later phase. |
| `dashboard-api` | Backend for the contributor and customer dashboards. |
| `push` | Sends job-offer notifications through FCM (Android) and APNs (iOS) when the app is not in the foreground, and relays the owner's Accept / Decline back to the scheduler. |

**Infra:** Fly.io or Hetzner and AWS in 3 regions (US-East, EU-West, AP-Southeast), Cloudflare (DNS, WAF, R2 for models and updates), Postgres (Neon or RDS), Redis (Upstash or ElastiCache), and Grafana/Prometheus with Sentry.

### 2.6 Web
- `infera.xyz`: landing page, waitlist with referral codes, and downloads.
- `app.infera.xyz`: contributor dashboard (devices, uptime, earnings, leaderboard) and customer console (API keys, usage, billing).
- Built in Next.js, deployed on Vercel or Cloudflare Pages.

## 3. Install-and-forget rules (governor)

The node accepts **new** jobs only when **all** of these are true. When one becomes false (for example the owner comes back), it stops taking new jobs within 1 second, but a job already accepted runs to completion at low priority. Accepted jobs are the owner's responsibility (§3.4). Only hard limits stop a running job: critical temperature, battery below 15%, or the owner pressing "Stop now" (which counts as an owner abort).

| Rule | Desktop / laptop | Android | iOS |
|---|---|---|---|
| Power | On AC power (laptops). Desktops always. | Charging | Charging |
| User idle | No keyboard/mouse input for 5 min (default) | Screen off, or the app's Charge Mode | Charge Mode or BGProcessingTask |
| Not in use | No fullscreen app or game, and own GPU use by other processes under 20% | — | — |
| Thermal | GPU under 80 °C and CPU not throttling | Thermal status below MODERATE | Thermal state nominal or fair |
| Network | Any | Unmetered Wi-Fi | Wi-Fi |
| Caps | Max 80% GPU and 50% RAM, user-adjustable | Max 2 threads, battery above 50% | — |
| Schedule | Optional "only between hours X–Y" | Same | — |

Also built in:
- A one-click "Pause for 1h / until tomorrow" toggle.
- No setup questions after pairing, with defaults chosen from the benchmark.
- Silent updates and automatic recovery from crashes. The supervisor restarts the engine, and five crashes in an hour disables the device's GPU path and reports it.

## 3.1 Benchmark and device classes

Right after install (phones: on the charger), the agent runs a 2–4 minute benchmark:

1. **Hardware:** CPU cores and type (big/little), RAM, GPU model and VRAM, NPU, free disk, OS.
2. **Engines:** which backends work (CUDA, Vulkan, Metal, NPU, CPU) and which engine adapters load.
3. **Speed:** generation and prompt tokens/sec on 1B, 3B and 8B test models (where they fit), embeddings/sec, Whisper real-time factor, RAM bandwidth, and flash read speed.
4. **Heat:** 90-second sustained run to see whether the device slows down.
5. **Network:** latency to the nearest region and download speed.

The classifier then sets the **class**, the job types allowed, the models to download and the engine per model.

| Class | Typical hardware | Must reach | Runs | Jobs |
|---|---|---|---|---|
| A · Powerhouse | GPU 16 GB+ VRAM (RTX 4080/4090/5090), Mac 32 GB+ | ≥ 40 tok/s on 8B | 14–32B dense, 35B+ MoE | Real-time chat (large), batch chat, everything below |
| B · Performer | GPU 8–16 GB (RTX 3060 12 GB, 4070), Mac 16–24 GB | ≥ 25 tok/s on 8B | 7–9B dense, 35B MoE | Real-time chat (mid), batch chat, embeddings, transcription |
| C · Standard | GPU 4–8 GB, CPU-only PCs with 16 GB RAM | ≥ 10 tok/s on 3B | 1–4B, 1-bit 8B, embeddings, Whisper | Batch chat (small), embeddings, transcription |
| M1 · Mobile Plus | Phones with 8 GB+ RAM | ≥ 8 tok/s on 1–3B, steady through the heat test | 1–3B, 1-bit models, Whisper small; opt-in 16–35B MoE from flash | Embeddings, transcription, small batch chat; MoE batch chat if opted in |
| M2 · Mobile Lite | Phones with 4–8 GB RAM | Passes the embeddings test | Embedding and classification models, 0.6–1.7B | Embeddings, classification |

- Devices below the minimum see "Not eligible yet" and why.
- The owner sees the result: class, what it can run, which jobs it will get.
- The server checks the benchmark against known ranges for that hardware, and live job speed overrides benchmark claims. A device that delivers below its class is moved down.
- The scheduler offers a job only to classes allowed to run it, then ranks by live speed, reputation and distance.

## 3.2 Job offers (Uber-style)

Owners who turn off auto-approve get every job as an offer card, like a ride request.

**Modes (per device)**

| Mode | Behaviour |
|---|---|
| Auto-approve (default) | Every job the class allows runs without asking. One earnings summary a day. |
| Auto-approve with limits | Automatic within the owner's limits (longest job, lowest pay per job, lowest pay per hour, charging only, Wi-Fi only). Anything outside arrives as an offer card. |
| Ask for every job | Every job arrives as an offer card. |

**Offer card**

```
● Online                         Today $1.82 · 37 jobs
NEW JOB OFFER                                   ◔ 24s
$0.04
≈ $0.40 per hour
JOB   Transcription
SIZE  12 min of audio
TIME  about 6 min
RUNS  now · charging
[               Accept               ]
               Decline
```

- **Time:** job size ÷ this device's benchmark speed for the model, plus model load time if needed.
- **Earnings:** job price × owner share (e.g. 70%). Per hour = earnings ÷ time. The final credit uses actual work; the receipt shows estimate and final.
- The card never shows the prompt, the output or the customer (§3.3).

**Dispatch rules**
- Offers go only to devices that are ready right now (governor rules met), so an accepted job runs immediately.
- One device at a time, best match in the class first. 30-second countdown (configurable per job type, 15–60 s). No answer counts as a decline.
- A decline sends the offer to the next best device in the same class. Declining is free and never lowers reputation or dependability. The scheduler learns which job types an owner declines and offers fewer of them. Once accepted, finishing the job is the owner's responsibility (§3.4).
- Real-time chat goes only to auto-approve devices.
- Home screen has a Go online / Go offline toggle, today's earnings and job count.
- Delivery: Android high-priority notification with Accept / Decline actions and a countdown; iOS time-sensitive notification with actions, sent only in Charge Mode; full-screen card when the app is open; tray pop-up on desktops.

## 3.3 Secure job sandbox (privacy)

Every job runs inside a sealed sandbox. The device owner, the app interface, the agent and our relay servers see only metadata (category, size, time, earnings), never the prompt, files, output or customer.

**Sealed job flow**
1. On accept, the sandbox creates a fresh key pair and an attestation proving it runs our signed sandbox and engine on a device that is not rooted, jailbroken or being debugged.
2. The gateway verifies the attestation and encrypts the job to that key (HPKE). The agent and relay only pass ciphertext.
3. The sandbox decrypts, runs the model, encrypts the output to the gateway's key, and wipes memory. Nothing touches disk.

**Privacy levels**

| Level | Devices | Protection | Gets |
|---|---|---|---|
| Confidential | Android phones with protected VMs (Android Virtualization Framework, e.g. recent Pixels); partner servers with confidential-computing GPUs (NVIDIA H100/Blackwell) and CPUs (AMD SEV-SNP, Intel TDX) | Hardware-isolated memory with remote attestation; unreadable even with root | All jobs, including `confidential` |
| Sealed | iPhones; Android phones passing strong Play Integrity; Macs with System Integrity Protection on | OS sandbox, no debugger, encrypted job, attestation that the OS is intact; reading the job would need a jailbreak or root, which attestation detects | `sealed` and `sandboxed` jobs |
| Sandboxed | Windows and Linux PCs with consumer GPUs | Encrypted job, isolated low-privilege process, no logs, no disk writes, memory wiped. Consumer GPUs have no hardware memory isolation, so an admin could in principle read GPU memory | `sandboxed` jobs only: customer-approved non-sensitive data (public data, synthetic data, evals) |

- The customer sets the minimum level per request with the API field `privacy` (`confidential` | `sealed` | `sandboxed`). Higher levels cost more.
- Sandbox lock-down: only signed engines and models; network only to our relay; swap off or encrypted; no crash dumps with job data; refuses to start on rooted, jailbroken or debugged devices.
- Attestation sources: Android key attestation and Play Integrity, AVF protected-VM attestation; App Attest on iPhone and Mac; TPM and measured boot on Windows (for device identity); GPU/CPU attestation reports on partner servers.

## 3.4 Dependability and penalties

Once a device accepts a job, the owner is responsible for it finishing. Every device has a **dependability score** (0–100), separate from the correctness reputation used by the verifier. New devices start at 70 (probation).

**When a job stops midway, the cause decides the penalty**

| Cause | Detected by | Penalty |
|---|---|---|
| **Owner action:** app quit or force-stopped, "Stop now" pressed, device shut down or put to sleep, went offline mid-job, network turned off | Agent shutdown hook, OS power events, heartbeat lost after the app's own quit signal | Job pay forfeited · debit of 2× the job's value · dependability −10 |
| **Accepted but not delivered:** no first token within the deadline, or job not finished by its deadline | Scheduler timers | Job pay forfeited · debit of 1× the job's value · dependability −8 |
| **Device or connection failure:** Wi-Fi drop, crash, out of memory, thermal or low-battery cut-off | Heartbeat loss without a quit signal, crash report, governor log | Job pay forfeited · dependability −5 · no debit for the first 2 per week, 1× debit after that |
| **Wrong result:** failed canary or re-run mismatch | Verifier | Job pay forfeited · debit of 3× · reputation and dependability −15 |
| **Not the owner's fault:** our server outage, a known bug in our app (crash signature matches), customer cancelled | Incident flag, crash signature list | No penalty; owner paid for work done |

**How penalties are applied**
1. **Forfeit:** the stopped job pays nothing.
2. **Debit:** taken from pending (held) earnings first, then from the available balance. A balance can go negative; future earnings pay it off before any payout.
3. **Withholding:** earnings are held before they can be paid out, longer for lower scores.
4. **Earning potential:** lower scores get fewer and lower-value offers and a lower pay multiplier.
5. **Suspension:** for repeated owner aborts.

| Dependability | Earnings hold | Pay multiplier | Offers |
|---|---|---|---|
| 90–100 | 3 days | 1.00× | All job types allowed by class, first in line |
| 70–89 (new devices start at 70) | 7 days | 0.95× | All job types, normal priority |
| 50–69 | 14 days | 0.85× | Batch jobs only, low priority |
| 30–49 | 30 days | 0.70× | Low-value batch jobs only |
| Below 30 | Payouts frozen | — | Suspended 72 hours, then back at 30 on probation |

- **Fast suspension:** 3 owner-action aborts in 24 hours triggers a 24-hour suspension. Repeated suspensions within 30 days lead to removal, and pending earnings are forfeited to cover debits.

**Earning dependability back**
- +1 per job completed on time (+0.5 for jobs under 1 minute), up to +5 per day.
- +3 bonus for every 50 completed jobs in a row without an abort.
- Penalties older than 30 days count at half weight; older than 90 days, they are dropped from the score.

**Fairness and protection for owners**
- **Finish then stop:** a "Finish current job, then go offline" button never costs anything.
- **Warning before quitting:** if a job is running, the app warns first, e.g. "A job is running (about 40 s left). Quitting now costs $0.08 and 10 dependability points."
- **Every penalty is visible:** the earnings screen shows the job, the cause, the evidence and the amount. A one-tap appeal re-checks the logs.
- **First-week grace:** in the first 7 days, device-failure stops are never debited.
- Penalty rules are part of the contributor terms accepted at sign-up.

**Ledger effect:** a penalty is a ledger entry (`forfeit`, `debit`, `hold_extension`, `multiplier_change`, `suspension`) linked to the job and its evidence, so every balance can be re-computed from its history.

## 4. Node ↔ control-plane protocol (JSON over WSS, protobuf later)

```
node → hub   hello        {node_id, pubkey, platform, hw, bench_profile, engines, models_cached, agent_version, attestation}
hub  → node  config       {class, privacy_level, allowed_jobs, models_to_fetch, engine_per_model, caps, region}
node → hub   state        {status: idle|busy|paused|offline_soon, loaded_model, temps, battery}   (every 15s)
node → hub   approval_mode {mode: auto|auto_limits|ask, online, limits:{max_minutes, min_earnings, min_per_hour, charging_only, wifi_only}}
hub  → node  job_offer    {job_id, job_type, model, est_size, price, owner_share, offer_ttl_s, privacy}   (metadata only, no content)
node         (computes est_minutes from benchmark, est_earnings = price × owner_share; shows offer card in ask mode)
node → hub   job_accept   {job_id, sandbox_pubkey, attestation}  | job_decline {reason: user_declined|timeout|limits|governor}
hub  → node  job_payload  {job_id, sealed_input}               (encrypted to sandbox_pubkey)
node → hub   tokens       {job_id, seq, sealed_delta}     (streamed, encrypted to gateway)
node → hub   job_done     {job_id, usage:{in,out}, latency, receipt_sig}
hub  → node  job_cancel   {job_id}
```

The receipt is the node's Ed25519 signature over `job_id | model | sha256(output) | tokens_in | tokens_out | ts`.

## 5. Model catalog (v1)

Models per class are listed in §3.1. The full engine and model research, including smolmaxxing (flash-paged MoE models on phones) and 1-bit models, is in `ON_DEVICE_INFERENCE.md`. Each device keeps 1–2 models cached; the scheduler requests a model swap only when demand warrants it. Every model's licence must allow commercial serving.

## 6. Job types and routing

| Job type | Latency need | Served by |
|---|---|---|
| Interactive chat | TTFT < 1.5s | Class A/B devices on auto-approve, with hedging, otherwise fallback provider |
| Batch chat (async API, 24h SLA) | Minutes–hours | Classes A, B, C and M1 (M1 includes opt-in MoE from flash) |
| Embeddings | Seconds | Every class |
| Transcription | Minutes | Classes B, C and M1 |

The batch, embedding and transcription APIs are priced 50–80% below interactive. They let unreliable consumer devices earn real money from day one.

## 7. Trust, verification and privacy
- **Identity:** Ed25519 key per device. Optional attestation through Play Integrity (Android), App Attest (iOS), and TPM-backed keys on Windows (v2).
- **Correctness:** canaries, redundant sampling and reputation. Low-reputation nodes get only batch work and their earnings are withheld 7 days before release.
- **Anti-sybil:** one pairing per device fingerprint, earnings proportional to verified work only (not uptime), and KYC at the payout threshold.
- **Privacy:** every job runs in the secure sandbox and is encrypted to the sandbox's own key (§3.3). The customer picks a minimum privacy level per request.

## 8. Payments

```
Customer pays (Stripe card/credits, later x402/USDC)
  → gateway meters tokens → scheduler assigns node → node returns signed receipt
  → ledger: revenue split, e.g. operator 70% / protocol 30% (config value)
  → beta: points + USD balance in Postgres
  → penalties and holds applied (§3.4)
  → payout: USDT to the owner's wallet, weekly, above a minimum threshold (fiat via Stripe Connect as an option)
  → later: on-chain anchoring of receipt batches, token rewards for supply bootstrapping
```

### 8.1 USDT payouts

Owners are paid in USDT, so payouts work in any country without manual bank transfers.

**Flow**
1. Customers pay in fiat (Stripe) or stablecoins.
2. Treasury buys USDT weekly to cover the payouts due, through an exchange or OTC desk account with API withdrawals.
3. USDT sits in a custody wallet: a hot wallet funded only with the current week's payouts, and the rest in cold storage or multisig.
4. Every week the ledger produces the payout batch: owners whose available (unheld) balance is above the threshold.
5. A payout worker sends the batch, records each transaction hash in the ledger, and notifies the owner with a link to the transaction.

**Owner setup**
- The owner adds a USDT address in the app and picks the network: **Tron (TRC-20)** for the lowest fees, or Ethereum L2s, BNB Chain, Polygon, Solana or TON.
- **Address check:** the owner signs a message with the wallet, or confirms a tiny test transfer.
- Address changes trigger a 48-hour payout hold, to protect against account takeover.

**Rules**
- Minimum payout, e.g. $10, so network fees stay small. Fees are absorbed on Tron and shown up front on other networks.
- Identity check at a cumulative payout threshold, e.g. $600 per year. Wallet addresses are screened against sanctions lists before each batch.
- **Custody and payout tooling options:** Fireblocks or BitGo (custody with batch sends and spending policies), exchange APIs (Kraken, OKX, Binance) for buying and withdrawing, or a self-run wallet with multisig for the cold reserve.
- **Controls:** daily send limits on the hot wallet, two-person approval for batches above a set size, and alerts on failed or unusual sends.
- Get legal advice on money-transmission and stablecoin rules for each launch country before the first real payout.

## 9. Repository layout

```
agent/           Rust core: identity, transport, probe, governor, engine adapters, sandbox, models, jobs, updater
desktop/         Tauri tray app + installers
android/         Kotlin app, JNI bridge
ios/             Swift app
control-plane/   Go: gateway, nodehub, scheduler, verifier, ledger, dashboard-api
web/             Next.js landing, waitlist, dashboards
proto/           message schemas
infra/           Terraform / Fly configs, CI pipelines
docs/            this documentation
third_party/     smolmaxxing (llama.cpp patches for phones and low-RAM devices)
```

## 10. CI/CD
- GitHub Actions builds the agent for 6 targets: win-x64, mac-arm64, mac-x64, linux-x64, linux-arm64 and android-arm64.
- Installers are signed (Windows code-signing cert, Apple Developer ID plus notarization) and published to R2, along with a signed update manifest.
- Staged rollout: internal, then 10% of beta, then 100%, with automatic halt if the crash rate exceeds 2%.
- The control plane uses blue/green deploys per region, with migrations through `golang-migrate`.
