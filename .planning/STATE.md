# State

**Current phase:** 3 and 5 in parallel – tray installers, Android testing
**Branch:** `ccr-13fb360c-2cnfc4`

## Done
- Phase 0 beta build pushed and tested end to end (see `TESTING.md` §5).

- Phase 1 beta hardening: canaries, penalty decay, keyboard/mouse idle, `infera service`, graceful stop (see `.planning/phases/01-beta-hardening.md`).

- Localhost test script (`scripts/local-test.sh`), passing.
- Stripe Checkout credits with signed webhook (unit-tested; needs test keys for a live check).
- Tray app (`infera tray`), used by `infera service install` on desktops.
- Android: llama.cpp engine (tested on Linux), Kotlin core (tested against the server), app + CI APK build.

- Automatic tests on every push: e2e with a real model (Ollama qwen3:0.6b) on Linux and macOS; live-site check once INFERA_PUBLIC_URL is set.
- Reasoning models: reasoning text now flows through sealing, receipts and the API (bug found by the real-model e2e test).
- Test builds: every push refreshes the "Infera test build" pre-release (APK + desktop nodes).
- Railway: `control-plane/railway.json` + `docs/DEPLOY_RAILWAY.md`; server honours `$PORT`.

- **Live on Railway:** https://control-plane-production-11856175.up.railway.app (project `infera`, service `control-plane`, volume at /data). Deployed from the branch by Claude via the Railway API; `live-check` workflow tests it.

- **Database:** Railway Postgres service `postgres` (volume), server uses it via `INFERA_DATABASE_URL` over Railway's private network. SQLite still used for local tests. The few test accounts made on the earlier SQLite deploy were not migrated.

- P1 gap items G1–G6 done: /download, rate limits, batch queue, pause/hours, GPU/RAM/fullscreen rules, re-benchmark + class demotion.

## Next
- P2 gap items (see .planning/GAPS.md).
- Embeddings, re-runs, and landing page + X beta tools all done and deployed.
- Remaining (see ROADMAP): phone testing (owner), Stripe keys (owner), signed models + auto-update, smolmaxxing on Android, iPhone, sealed/confidential privacy, signed installers (needs Apple/Windows certs).
- macOS checks run on GitHub's Mac runners instead of the owner's Mac mini (cloud sessions can't use the Mac yet).
- Install the CI-built APK on an Android phone and test with a small GGUF model.
- Stripe test keys → live checkout test.
- Then: privacy (attested sandbox).

## Blockers
- Deploy needs the owner's VPS, domain, and a public release tag.

## Decisions
- Name: Infera (EigenGrid dropped: Eigen Labs trademarks).
- Payouts in USDT, Tron TRC-20 default.
- Beta privacy level is `sandboxed` only; developers told not to send sensitive data.
