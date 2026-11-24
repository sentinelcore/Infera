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

## Next
- Run `scripts/local-test.sh` on the owner's Mac mini.
- Install the CI-built APK on an Android phone and test with a small GGUF model.
- Stripe test keys → live checkout test.
- Then: privacy (attested sandbox).

## Blockers
- Deploy needs the owner's VPS, domain, and a public release tag.

## Decisions
- Name: Infera (EigenGrid dropped: Eigen Labs trademarks).
- Payouts in USDT, Tron TRC-20 default.
- Beta privacy level is `sandboxed` only; developers told not to send sensitive data.
