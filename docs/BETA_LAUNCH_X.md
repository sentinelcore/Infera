# Beta Launch on X

The goal is to recruit contributors (supply) and API users (demand) from X in waves, keeping each wave small enough to support.

## 1. Setup (week 0–1)

- **Profile:** `@Infera`. The bio reads "Your idle laptop, desktop or phone serves AI inference. Install once, earn while you sleep. Beta waitlist ↓", with a link to the waitlist.
- **Pinned post:** the launch thread (§3).
- **Waitlist form** asks for: X handle, device type (NVIDIA GPU / AMD GPU / Mac M-series + RAM / Windows laptop / Android / iPhone), country, and whether they are a contributor, a developer, or both.
- **Referral link per sign-up:** each referral moves the person up the waitlist and earns bonus points at launch.
- **Discord:** channels for `#announcements`, `#install-help`, `#bug-reports`, `#leaderboard` and `#dev-api`.

## 2. Waves

| Wave | When | Size | Who | Selection |
|---|---|---|---|---|
| Alpha | Week 4 | 20 | Team and friends | Direct invites |
| Wave 1 | Week 6 | 100 | NVIDIA GPUs and M-series Macs ≥ 16 GB | Top referrers, plus hardware mix |
| Wave 2 | Week 8 | 500 | + AMD, Linux, CPU laptops | Waitlist order, plus referrers |
| Wave 3 | Week 12 | 2,000 | + Android | Waitlist order |
| Public beta | Week 16 | Open | + iOS (TestFlight) | Open |

Developers get API access at every wave with free starter credits, given in exchange for feedback.

## 3. Launch thread (post in week 1)

> 1/ Your laptop, desktop and phone sit idle ~20 hours a day.
> We're building Infera: a network where idle devices serve AI inference and their owners get paid.
> Install once. It runs when you're away, plugged in and idle. It pauses the moment you're back.
>
> 2/ How it works:
> • Install the app (Windows / Mac / Linux, Android next, iPhone after)
> • It benchmarks your device and picks the right model
> • Developers send AI requests through our API
> • Your device serves them and you earn for each verified job
>
> 3/ Safe by design: it only runs our signed inference engine and signed models. No arbitrary code, no mining.
>
> 4/ Developers: OpenAI-compatible API, cheaper batch and embeddings pricing. Free beta credits.
>
> 5/ Closed beta opens in waves. Early contributors get a points multiplier.
> Join the waitlist: [link]. Refer friends to move up.

## 4. Content calendar (repeat weekly)

| Day | Post |
|---|---|
| Mon | Build-in-public update: what shipped, a screenshot or clip |
| Tue | Explainer: how one part works (governor, verification, routing) |
| Wed | Network stats: devices online, tokens served, countries |
| Thu | Contributor spotlight or leaderboard top 10 |
| Fri | Developer post: use case, code snippet, credits giveaway |
| Weekly | X Space or live Q&A once beta starts |

## 5. Growth loops

- **Referrals:** bonus points for each referred device that stays active 7 days. Rewarding active devices rather than sign-ups limits fake accounts.
- **Leaderboard:** a public top-100 by verified jobs, with shareable cards (device, hours, jobs, rank).
- **"My device earned X this week" card:** generated in-app, one-click post to X.
- **Hardware communities:** post in r/LocalLLaMA, r/homelab, and the Ollama and llama.cpp communities, plus DePIN-focused X accounts and Discords.
- **Collaborations:** co-posts with grant chains' ecosystem accounts, and DePIN and AI creators on X.

## 6. Beta feedback loop

- In-app "Report a problem" sends logs and device info to Discord `#bug-reports` via webhook.
- A 3-question survey after 7 days of running.
- A weekly changelog posted on X and Discord.

## 7. Wave gate metrics (before opening the next wave)

| Metric | Target |
|---|---|
| Install success rate | ≥ 95% |
| Crash-free devices (7 days) | ≥ 98% |
| 7-day node retention | ≥ 60% |
| Canary pass rate | ≥ 99% |
| Support tickets per 100 nodes per week | ≤ 5 |
