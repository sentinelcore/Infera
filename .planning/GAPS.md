# Architecture gap audit (ARCHITECTURE.md vs what's built)

Audited 2026-11-24. ✅ built · 🟡 partial · ⬜ not built. Priority: P1 next, P2 soon, P3 later/needs owner.

| # | Area | Architecture says | Status | Priority |
|---|---|---|---|---|
| G1 | Web | `/download` page for testers (unsigned builds until certificates) | ✅ | P1 |
| G2 | Jobs | Async **batch API** with a persistent queue (24 h SLA; phones and slow devices pick jobs when ready) | ✅ | P1 |
| G3 | Gateway | Per-API-key rate limits | ✅ | P1 |
| G4 | Node | Resource caps (max 80% GPU, 50% RAM), GPU temperature, fullscreen/game detection | ⬜ | P1 |
| G5 | Node | Pause for 1 h / until tomorrow; "only between hours X–Y" schedule | ⬜ | P1 |
| G6 | Node | Re-benchmark on driver/OS/hardware change, monthly, or live speed drift > 20%; server moves a device down a class when it under-delivers | 🟡 (only when the model changes) | P1 |
| G7 | Node | `models`: download from our storage on unmetered networks, SHA-256 + signature check, disk quota | ⬜ (owner points at Ollama / a file) | P2 |
| G8 | Node | `updater`: signed update manifest, silent update when idle; staged rollout with auto-halt on crashes | ⬜ | P2 |
| G9 | Offers | Auto-approve limits beyond min pay (max duration, min pay per hour, charging-only, Wi-Fi-only on desktop); scheduler learns declined job types | 🟡 (min pay only) | P2 |
| G10 | Dependability | One-tap appeal; repeated suspensions within 30 days → removal; quit warning with exact cost in tray | 🟡 | P2 |
| G11 | Jobs | **Transcription API** (`/v1/audio/transcriptions`, Whisper on phones and laptops) | ⬜ | P2 |
| G12 | Scheduler | Hedged interactive requests (two devices, first answer wins) | ⬜ | P2 |
| G13 | Trust | Anti-sybil: one pairing per device fingerprint | ⬜ | P2 |
| G14 | Payouts | Wallet ownership proof (signed message or test transfer); identity check at threshold; sanctions screening before each batch | 🟡 (format check + 48 h hold) | P2 |
| G15 | Ops | Metrics endpoint, error tracking, public status page | ⬜ | P2 |
| G16 | Node | Identity key in OS keychain / Android Keystore | 🟡 (file with 0600 / app prefs) | P2 |
| G17 | Engines | smolmaxxing (MoE from flash on phones), MNN-LLM, LiteRT-LM/NPU, Genie, bitnet.cpp, vLLM, MLX native | 🟡 (llama.cpp + any OpenAI-compatible server) | P2 |
| G18 | Benchmark | Heat test, network latency, RAM/flash bandwidth, several model sizes | 🟡 (speed on the chosen model) | P3 |
| G19 | Android | WorkManager start constraints, Play Integrity attestation | 🟡 (foreground service + rules) | P3 |
| G20 | iPhone | Charge Mode app, App Attest | ⬜ | P3 |
| G21 | Privacy | Sealed level (attested OS sandbox), confidential level (pVM, confidential GPUs), sandbox network allowlist | ⬜ (sandboxed level only) | P3 |
| G22 | Payments | Customer stablecoin payments (USDC/x402) | ⬜ | P3 |
| G23 | Scale | Multi-region; shared device registry + message bus (Redis or NATS) across server instances | ⬜ (one instance, in-memory) | P3 |
| G24 | Ledger | On-chain anchoring of receipt batches; token | ⬜ | P3 (legal first) |
| G25 | Installers | Signed `.dmg` / `.msi`, notarized | ⬜ | Owner: Apple/Windows certificates |
| G26 | Payouts | Automated USDT sending | ⬜ | Manual by decision |
