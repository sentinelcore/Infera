# DePINClaw Architecture

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
 │  Ledger (Postgres) ── signed receipts → points → USD/USDC payouts → on-chain    │
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
| `probe` | First-run benchmark of RAM/VRAM, GPU/NPU type and tokens/sec per model tier. Re-runs after driver or OS changes. |
| `governor` | Decides when the node may work, using the idle rules in §3, and enforces the resource caps. |
| `engine` | Starts and stops the inference backend, loads and unloads models, and streams tokens. |
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
- **Headless install** for servers and always-on desktops: `curl -fsSL https://get.depinclaw.xyz | sh` installs a systemd (Linux) or launchd (macOS) service, and pairs with `depinclaw login`.

### 2.3 Android app
- Kotlin UI plus the Rust core and llama.cpp via JNI.
- **Foreground service** (type `dataSync`/`specialUse`) with a persistent notification. Started by WorkManager under these constraints: charging, unmetered network, battery not low, and device idle.
- Pauses on thermal status at `THERMAL_STATUS_MODERATE` or above, on screen-on with user activity, or when unplugged.
- Restarts after reboot via `BOOT_COMPLETED`, which keeps it install-and-forget.
- Distributed through Google Play, plus a direct APK for beta testers.
- Workloads: 1–3B LLMs, embeddings and Whisper-small transcription.
- Job notifications with Approve / Reject buttons, or auto-approve (see §3.1).

### 2.4 iOS app
- iOS blocks sustained background compute, so the app uses **Charge Mode**: it works while the phone is plugged in, either with the app open (screen dimmed, like a nightstand mode) or via `BGProcessingTask` with `requiresExternalPower = true`, which iOS schedules overnight.
- Install-and-forget on iOS means work happens overnight while charging. Expect lower supply per device than Android.
- Distributed via TestFlight for beta, then the App Store.
- Job notifications with Approve / Reject actions, or auto-approve (see §3.1). Approved jobs run in the next Charge Mode window.

### 2.5 Control plane (Go services, one repo, deployed per region)

| Service | Responsibility |
|---|---|
| `gateway` | OpenAI-compatible API, API keys, per-key rate limits, token metering, Stripe usage billing and prepaid credits. |
| `nodehub` | Terminates node WebSockets and maintains the live registry in Redis (model loaded, state, tokens/sec, region, reputation). |
| `scheduler` | Matches requests to nodes, retries on failure, and can hedge interactive requests across two nodes and take the first response. Falls back to rented capacity when no node meets the SLA. |
| `verifier` | Canary jobs with known outputs (greedy decoding, fixed seed), re-runs 2–5% of jobs on a second node, and updates reputation. |
| `ledger` | Stores signed receipts and computes points, operator earnings and payouts. Postgres is the source of truth; batches are anchored on-chain in a later phase. |
| `dashboard-api` | Backend for the contributor and customer dashboards. |
| `push` | Sends job-offer notifications to phones through FCM (Android) and APNs (iOS) when the app is not in the foreground, and relays the owner's Approve / Reject answer back to the scheduler. |

**Infra:** Fly.io or Hetzner and AWS in 3 regions (US-East, EU-West, AP-Southeast), Cloudflare (DNS, WAF, R2 for models and updates), Postgres (Neon or RDS), Redis (Upstash or ElastiCache), and Grafana/Prometheus with Sentry.

### 2.6 Web
- `depinclaw.xyz`: landing page, waitlist with referral codes, and downloads.
- `app.depinclaw.xyz`: contributor dashboard (devices, uptime, earnings, leaderboard) and customer console (API keys, usage, billing).
- Built in Next.js, deployed on Vercel or Cloudflare Pages.

## 3. Install-and-forget rules (governor)

The node works only when **all** of these are true, and pauses within 1 second when any becomes false.

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

## 3.1 Job approval on phones

On phones the owner chooses how jobs are accepted. Each job offer shows what the job is, how long it will take on this phone, and how much it will earn.

**Approval modes (set in the app, per device)**

| Mode | Behaviour |
|---|---|
| Auto-approve (default) | Every job that passes the governor rules runs without asking. A summary notification is shown at most once a day. |
| Auto-approve with limits | Runs automatically only when the job is within the owner's limits: max duration (e.g. 15 min), min earnings (e.g. $0.02), only while charging, only on Wi-Fi. Jobs outside the limits fall back to "Ask me". |
| Ask me each time | Every job arrives as a notification with Approve / Reject buttons. |

**Notification content**

```
DePINClaw · New job
Transcription · 12 min of audio
Takes about 6 min on this phone · Earns $0.04
Expires in 10 min
[ Approve ]   [ Reject ]
```

**How the numbers are calculated**
- **Time:** the job's expected size (tokens or audio minutes, from the offer) divided by this phone's benchmark speed for that model, plus model load time if the model is not already loaded. Shown rounded up, as "about N min".
- **Earnings:** the job price × owner share (e.g. 70%), shown in the owner's currency. It is the amount credited if the job completes and passes verification.
- The final amount credited is based on actual tokens used and can differ slightly. The receipt screen shows both the estimate and the final figure.

**Rules**
- Only latency-tolerant jobs go to devices in "Ask me" mode: batch chat, embeddings and transcription. Real-time chat never waits on a human.
- An offer expires after 10 minutes by default. No answer counts as a reject.
- Rejecting or ignoring a job carries no penalty and does not affect reputation. The scheduler offers it to the next device.
- An approved job still runs only when the governor allows it (charging, Wi-Fi, cool). On iPhone it runs in the next Charge Mode window.
- When the app is open, the offer appears as an in-app card instead of a system notification.
- Notification actions: Android `NotificationCompat` action buttons; iOS `UNNotificationCategory` with Approve and Reject actions.

```
scheduler ──job_offer──► push service ──FCM / APNs──► phone notification
                                                         │
                         ◄──── job_accept / job_decline ─┘   (or expires → next device)
approved → job queued on device → runs when governor allows → tokens → receipt → earnings
```

## 4. Node ↔ control-plane protocol (JSON over WSS, protobuf later)

```
node → hub   hello        {node_id, pubkey, platform, hw, bench, models_cached, agent_version, attestation?}
hub  → node  config       {models_to_fetch, caps, region}
node → hub   state        {status: idle|busy|paused|offline_soon, loaded_model, temps, battery}   (every 15s)
node → hub   approval_mode {mode: auto|auto_limits|ask, limits:{max_minutes, min_earnings, charging_only, wifi_only}}
hub  → node  job_offer    {job_id, job_type, model, params, input, est_size, price, owner_share, deadline_ms, expires_at}
node         (computes est_minutes from benchmark and est_earnings = price × owner_share; shows notification in ask mode)
node → hub   job_accept | job_decline {reason: user_rejected|expired|limits|governor}
node → hub   tokens       {job_id, seq, delta}            (streamed)
node → hub   job_done     {job_id, usage:{in,out}, latency, receipt_sig}
hub  → node  job_cancel   {job_id}
```

The receipt is the node's Ed25519 signature over `job_id | model | sha256(output) | tokens_in | tokens_out | ts`.

## 5. Model catalog (v1)

| Tier | Devices | Models (GGUF Q4_K_M) | Products |
|---|---|---|---|
| T1 | GPU ≥ 12 GB VRAM, Mac ≥ 32 GB | 14B–32B instruct (Qwen, Mistral Small, Gemma) | Chat |
| T2 | GPU 6–12 GB, Mac 16 GB | 7–9B instruct (Llama 3.x 8B, Qwen 8B, Gemma) | Chat |
| T3 | CPU laptops, phones | 1–4B instruct, embeddings (bge/nomic), Whisper small | Embeddings, transcription, classification, batch |

Each device keeps 1–2 models cached. The scheduler requests a model swap only when demand warrants it.

## 6. Job types and routing

| Job type | Latency need | Served by |
|---|---|---|
| Interactive chat | TTFT < 1.5s | T1/T2 nodes with hedging, otherwise fallback provider |
| Batch chat (async API, 24h SLA) | Minutes–hours | Any node, including phones |
| Embeddings | Seconds | T3 and any idle node |
| Transcription | Minutes | Phones and laptops |

The batch, embedding and transcription APIs are priced 50–80% below interactive. They let unreliable consumer devices earn real money from day one.

## 7. Trust, verification and privacy
- **Identity:** Ed25519 key per device. Optional attestation through Play Integrity (Android), App Attest (iOS), and TPM-backed keys on Windows (v2).
- **Correctness:** canaries, redundant sampling and reputation. Low-reputation nodes get only batch work and their earnings are withheld 7 days before release.
- **Anti-sybil:** one pairing per device fingerprint, earnings proportional to verified work only (not uptime), and KYC at the payout threshold.
- **Privacy:** prompts are visible to the node. The gateway exposes a `privacy` flag: `standard` uses the community network, while `private` routes only to verified or partner nodes, or to fallback providers. Transport is TLS end to end, and nodes never write prompts to disk.

## 8. Payments

```
Customer pays (Stripe card/credits, later x402/USDC)
  → gateway meters tokens → scheduler assigns node → node returns signed receipt
  → ledger: revenue split, e.g. operator 70% / protocol 30% (config value)
  → beta: points + USD balance in Postgres
  → payout: Stripe Connect (fiat) or USDC on Solana/Base, weekly, above a minimum threshold
  → later: on-chain anchoring of receipt batches, token rewards for supply bootstrapping
```

## 9. Repository layout

```
agent/           Rust core: identity, transport, governor, engine, models, jobs, updater
desktop/         Tauri tray app + installers
android/         Kotlin app, JNI bridge
ios/             Swift app
control-plane/   Go: gateway, nodehub, scheduler, verifier, ledger, dashboard-api
web/             Next.js landing, waitlist, dashboards
proto/           message schemas
infra/           Terraform / Fly configs, CI pipelines
docs/            this documentation
```

## 10. CI/CD
- GitHub Actions builds the agent for 6 targets: win-x64, mac-arm64, mac-x64, linux-x64, linux-arm64 and android-arm64.
- Installers are signed (Windows code-signing cert, Apple Developer ID plus notarization) and published to R2, along with a signed update manifest.
- Staged rollout: internal, then 10% of beta, then 100%, with automatic halt if the crash rate exceeds 2%.
- The control plane uses blue/green deploys per region, with migrations through `golang-migrate`.
