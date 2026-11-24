# Infera Beta: Setup and Testing Guide

This beta covers **laptops and desktops** (Windows, macOS, Linux). Phones come in the next phase.

## What's in this build

| Part | Where | What it does |
|---|---|---|
| Control plane | `control-plane/` (Go, one binary + SQLite) | OpenAI-compatible API, device connections, scheduler, sealed jobs, signed receipts, ledger, dependability and penalties, USDT payout batches, dashboard |
| Node | `agent/` (Rust, one binary `infera`) | Pairing, benchmark and class, idle rules, engine adapter, sealed-job decryption, streaming, signed receipts, offer cards, safe stop, local page at `http://127.0.0.1:7777` |
| Stand-in engine | `tools/mock_engine.py` | Fake model for testing without downloading one |

## 0. Test everything on one computer first (macOS or Linux)

Needs Go 1.24+, Rust and Python 3. From the repository root:

```sh
./scripts/local-test.sh          # uses Ollama if it's running, otherwise a stand-in engine
./scripts/local-test.sh --mock   # always use the stand-in engine
./scripts/local-test.sh --stop   # stop everything
```

It builds the server and node, starts them on localhost, creates a contributor and a developer account, pairs the computer, sends a test request, and prints the dashboard link and keys.

## 1. Deploy the control plane (operator, ~15 minutes)

You need a small VPS (1 vCPU, 1 GB RAM is enough for the beta) with Docker, and a domain.

```sh
git clone https://github.com/sentinelcore/Infera.git && cd Infera
cp deploy/.env.example deploy/.env      # set INFERA_DOMAIN, INFERA_INVITE_CODE, INFERA_ADMIN_TOKEN
docker compose -f deploy/docker-compose.yml --env-file deploy/.env up -d --build
```

Caddy gets an HTTPS certificate automatically. Open `https://<your domain>` to see the dashboard.

To run it without Docker: `cd control-plane && go build -o infera-server . && INFERA_ADMIN_TOKEN=… INFERA_INVITE_CODE=… ./infera-server` (listens on `:8080`).

**Publish the node binaries:** push a tag (`git tag v0.1.0 && git push origin v0.1.0`). The `release` workflow builds `infera` for Windows, macOS (Apple Silicon and Intel) and Linux (x64 and ARM) and attaches them to a GitHub release, which `install.sh` and `install.ps1` download from. Testers can only download them if the repository (or its releases) is public.

## 2. Contributors: share a laptop or desktop

**Install the node**

```sh
# macOS / Linux
curl -fsSL https://control-plane-production-11856175.up.railway.app/install.sh | sh
# Windows (PowerShell)
irm https://raw.githubusercontent.com/sentinelcore/Infera/ccr-13fb360c-2cnfc4/install.ps1 | iex
```

**Pair it**
1. On the dashboard, open **Contribute a device**, sign up with the invite code, and save your contributor key.
2. Click **Get pairing code**, then run `infera pair <code> --server https://<your domain>`.

**Pick a model.** The easiest option is [Ollama](https://ollama.com) (or LM Studio):

```sh
ollama pull qwen3:1.7b
infera engine --url http://127.0.0.1:11434 --model qwen3-1.7b --engine-model qwen3:1.7b --params-b 1.7
```

Or let Infera run llama.cpp itself:

```sh
infera engine --llama-server /path/to/llama-server --gguf /path/to/model.gguf --model qwen3-1.7b --params-b 1.7
```

Use the **same `--model` name** as the other testers so customer requests can reach every device that serves it. Suggested names for the beta:

| `--model` | Ollama tag | Size | For |
|---|---|---|---|
| `qwen3-1.7b` | `qwen3:1.7b` | 1.7B | Any laptop |
| `qwen3-8b` | `qwen3:8b` | 8B | GPUs with 8 GB+, Macs with 16 GB+ |
| `qwen3-14b` | `qwen3:14b` | 14B | GPUs with 16 GB+, Macs with 32 GB+ |

**Run it**

```sh
infera bench      # shows your class (A, B or C); below 10 tokens/s you get no jobs
infera run        # or: infera run --ask   (offer card for every job)
```

Open `http://127.0.0.1:7777` for the local page: earnings, online toggle, offer cards with a 30-second countdown, the safe stop button, and **auto-approve limits** (minimum pay per job, minimum pay per hour, longest job). Jobs outside the limits come as offer cards. On the phone: **Auto-approve limits…** on the main screen.

**Desktop app:** `infera tray` runs the node with a menu-bar (macOS) or system-tray (Windows, Linux) icon: green when online, amber while working, grey when paused. The menu has Go online/offline, Ask before every job, Finish current job then go offline, Open Infera, and Quit. In ask mode, a new offer shows a notification and opens the offer card.

**Install and forget:** `infera service install` starts the node (as the tray app on desktops) every time you log in (systemd on Linux, a LaunchAgent on macOS, Task Scheduler on Windows). `infera service uninstall` removes it. If the computer shuts down or logs out mid-job, the node gets up to 25 seconds to finish the job first.

New jobs start only after 5 minutes without keyboard or mouse use (change `idle_minutes` in `~/.infera/config.toml`; Linux needs `xprintidle` installed on X11, otherwise only CPU load is used).

**Stopping**

| Action | Penalty |
|---|---|
| Decline an offer | None |
| "Finish this job, then go offline" on the local page | None |
| Ctrl-C once in the terminal (finishes the job, then quits) | None |
| "Stop now", or Ctrl-C twice | Job pay lost, 2× its value debited, −10 dependability |
| Computer crashes or Wi-Fi drops mid-job | Job pay lost, −5 dependability (first 2 a week not debited) |

**Get paid:** add a USDT wallet on the dashboard (Tron TRC-20 has the lowest fees). Earnings are held 3–14 days depending on dependability, then paid weekly once your available balance is $10 or more.

## 2b. Contributors: share an Android phone

1. Download the APK: GitHub → Actions → **android** workflow → latest run → artifact **infera-android-debug** (unzip it). Install it on the phone (allow installs from your browser or file manager).
2. Open Infera, enter the server address and a pairing code from the dashboard.
3. Paste a direct link to a small GGUF model (1–3B, e.g. a Qwen3-1.7B or Qwen3.5-2B Q4_K_M file), the shared model name (e.g. `qwen3-1.7b`) and its size, then tap **Download and benchmark**. Phones reaching 8+ tokens/s with 8 GB RAM are class M1; 3+ tokens/s is M2.
4. Tap **Start earning**. Jobs run only while charging, on Wi-Fi, above 50% battery and not warm; with "Ask before every job" on, each offer arrives as a notification with Accept and Decline.

## 3. Developers: call the API

Sign up on the **Developers** tab (beta accounts get $5 of credits), then:

```sh
curl https://<your domain>/v1/chat/completions \
  -H "Authorization: Bearer ik_cust_…" -H "Content-Type: application/json" \
  -d '{"model":"qwen3-1.7b","stream":true,"messages":[{"role":"user","content":"Hello"}]}'
```

Any OpenAI SDK works with `base_url = https://<your domain>/v1`. Extra fields:
- `"infera_batch": true` marks a job that can wait. Devices with lower dependability can take batch jobs.
- `"privacy"` sets the minimum privacy level. Only `sandboxed` is available in this beta (see the privacy limits below).

`GET /v1/models` lists the models currently online. Interactive requests are **hedged**: if the first device hasn't started answering within 2.5 s (`INFERA_HEDGE_MS`), a second device gets the request and the first to answer wins (you're billed once). When no device can take a request, the API returns 503. If `INFERA_FALLBACK_URL` is set, it sends the request to the rented provider instead.

## 4. Operator: weekly USDT payouts

1. Dashboard → **Admin** → enter the admin token → **Load batch**. It lists owners with $10+ available whose wallet has passed the 48-hour hold. CSV export: `GET /api/admin/payouts?format=csv` with `Authorization: Bearer <admin token>`.
2. Buy the total in USDT on your exchange or OTC desk and send it from the treasury wallet (or upload the CSV to Fireblocks or BitGo as a batch).
3. Paste each transaction hash and click **Mark sent**. This records the payout in the ledger and lowers the owner's balance.

Top up a developer's credits: `POST /api/admin/credits {"account_id":"…","usd":20}` with the admin token.

## 5. Tested in this build

Tested end to end with the stand-in engine (`tools/mock_engine.py`):

- Sign-up, pairing code, device pairing, benchmark and class C
- Non-streamed and streamed requests through a device
- Jobs encrypted to the node's per-job key; output encrypted back; Ed25519 receipt verified; customer charged; owner credited with the 7-day hold and band multiplier
- Offer card in "ask" mode: accept and decline from the local page; local page rejects requests without its header
- "Stop now" mid-job: customer stream ends with an error; job pay forfeited; 2× debit; −10 score; device dropped to the 50–69 band and then got batch jobs only, at 0.85× pay
- "Finish this job, then go offline" and a single Ctrl-C: job completes, no penalty
- Process killed mid-job: recorded as a device failure, −5, not debited
- Real llama.cpp: the node started `llama-server` itself, benchmarked it, and served streamed and non-streamed jobs with llama.cpp's own token counts in the signed receipt (tested with a tiny random-weight model, since this test environment can't download real models)
- USDT payouts: wallet address validation, 48-hour hold, batch CSV, "mark sent" lowers the owner's available balance; admin endpoints reject requests without the token
- Test prompts (canaries): with an honest engine, 3 of 3 passed; with an engine giving wrong answers, the second failure triggered a −15 "wrong result" penalty, and repeated failures led to a 72-hour suspension
- Graceful stop: when the OS asked the node to stop mid-job, it finished the job, then quit with no penalty
- Windows build compiles (keyboard/mouse idle via GetLastInputInfo, Task Scheduler service)
- Unit tests: bands, sealing, receipts, penalties, suspension, penalty decay, canary judging (`cd control-plane && go test ./...`)

## 6. Known limits of this beta

- **Privacy:** jobs are encrypted between the gateway and the node process, but the node hands the prompt to a normal local engine (Ollama, llama-server), so a contributor who inspects their own engine could read it. Every device is therefore at the `sandboxed` level. **Tell beta developers not to send sensitive data.** The attested sandbox (sealed and confidential levels) comes in later phases.
- **Trust:** the device class comes from the node's own benchmark. Test prompts (every 30 minutes per idle device, `INFERA_CANARY_MINUTES`) catch nodes that answer wrongly, but there are no re-runs of customer jobs yet.
- **Idle rules:** CPU load from other programs, mains power, and keyboard/mouse inactivity (macOS, Windows, Linux X11 with `xprintidle`). Fullscreen games aren't detected separately.
- **Tray app, not a full installer:** `infera tray` gives a menu-bar/tray icon, but there's no .dmg/.msi with a signed app bundle yet; installation is the one-line script.
- **Payments:** developers get admin-granted credits (no Stripe yet). USDT payouts are sent by hand from the Admin tab's batch.
- **Not yet built:** the 48-hour hold notice on wallet changes in the node.
