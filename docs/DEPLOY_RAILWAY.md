# Deploy the Infera server on Railway

One-time setup (about 2 minutes); after that every push to the branch redeploys automatically.

1. railway.com → **New Project → Deploy from GitHub repo** → `sentinelcore/Infera`.
2. Service → **Settings**:
   - **Source → Branch:** `ccr-13fb360c-2cnfc4` (later: `main`)
   - **Root Directory:** `control-plane` (Railway then uses `control-plane/Dockerfile` and `railway.json`)
3. Service → **Variables**:
   - `INFERA_INVITE_CODE` – code testers need to sign up
   - `INFERA_ADMIN_TOKEN` – long random string for the Admin tab
   - optional: `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`, `INFERA_PUBLIC_URL`, `INFERA_FALLBACK_URL`/`KEY`/`MODEL` (see `deploy/.env.example`)
4. Service → right-click → **Attach Volume**, mount path `/data` (keeps the database across deploys).
5. **Settings → Networking → Generate Domain**. Open it: you should see the dashboard.
6. In GitHub: **Settings → Secrets and variables → Actions → Variables → New variable** `INFERA_PUBLIC_URL` = that domain (with `https://`). The `live-check` workflow then tests the live site after every deploy and hourly.

The server reads Railway's `PORT` automatically. Nodes connect over `wss://<domain>/ws/node`.
