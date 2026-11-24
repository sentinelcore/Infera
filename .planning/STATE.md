# State

**Current phase:** 2 – Money in and out (next)
**Branch:** `ccr-13fb360c-2cnfc4`

## Done
- Phase 0 beta build pushed and tested end to end (see `TESTING.md` §5).

- Phase 1 beta hardening: canaries, penalty decay, keyboard/mouse idle, `infera service`, graceful stop (see `.planning/phases/01-beta-hardening.md`).

## Next
- Phase 2: Stripe prepaid credits (needs Stripe keys) and automated USDT payouts (needs custody/exchange account).

## Blockers
- OPS-02: repository rename not visible on GitHub yet; need the new name to update install URLs.
- Deploy needs the owner's VPS, domain, and a public release tag.

## Decisions
- Name: Infera (EigenGrid dropped: Eigen Labs trademarks).
- Payouts in USDT, Tron TRC-20 default.
- Beta privacy level is `sandboxed` only; developers told not to send sensitive data.
