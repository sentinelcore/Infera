package main

import "net/http"

// GET /privacy: what Infera keeps and who can see what. Marketplaces show this to buyers
// and ask for it in their security questionnaires, so it must match the code exactly.
func PrivacyPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(privacyHTML))
}

const privacyHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Infera data and privacy</title>
<style>body{font-family:system-ui,sans-serif;background:#0e1412;color:#e3ebe7;margin:0;padding:24px 16px;line-height:1.55}main{max-width:760px;margin:0 auto}
h2{margin-top:28px}p,li{color:#c9d3ce}.w{overflow-x:auto}table{width:100%;border-collapse:collapse;min-width:520px}th,td{text-align:left;padding:8px 6px;border-bottom:1px solid #2f3e37;vertical-align:top}
th{color:#9aaba3;font-size:.85rem}a{color:#4cc398}.note{background:#161f1b;border:1px solid #2f3e37;border-radius:10px;padding:12px 14px}</style></head>
<body><main><h1>Data and privacy</h1>
<p>Infera runs AI requests on independent people's laptops, desktops and phones. This page says exactly what we keep, and who can see what.</p>

<h2>What the Infera servers keep</h2>
<div class="w"><table><tr><th>Data</th><th>Kept?</th><th>How long</th></tr>
<tr><td>Prompts, messages, audio and answers of normal (live) requests</td><td><b>No.</b> They pass through our servers encrypted for one device and are never written to disk or logs.</td><td>–</td></tr>
<tr><td>Batch API requests and results (<code>/v1/batch</code>)</td><td>Yes, so you can fetch results later.</td><td>Deleted 7 days after the job finishes.</td></tr>
<tr><td>Job records: model, token counts or audio seconds, price, timing, which device</td><td>Yes, for billing, payouts and quality.</td><td>Kept with your account.</td></tr>
<tr><td>Signed receipts: a SHA-256 hash of each answer, signed by the device</td><td>Yes (a hash, not the answer).</td><td>Kept with your account.</td></tr>
<tr><td>Server logs</td><td>Method and path only (no bodies, no keys).</td><td>Rotated by our host.</td></tr>
</table></div>
<p>We don't train models on your data and don't sell it.</p>

<h2>What a device owner can see</h2>
<p class="note">Requests are encrypted end to end between our gateway and the Infera app on one device (X25519 + ChaCha20-Poly1305, a fresh key per job). The app doesn't show job content to the owner.
<b>But on today's devices (privacy level <code>sandboxed</code>), the model runs as a normal program on the owner's hardware, so a technically skilled owner could read a request while it runs.</b>
Don't send secrets, personal data or anything confidential until the <code>sealed</code> and <code>confidential</code> levels are available (attested sandboxes and confidential-computing hardware).</p>
<ul>
<li>Each request goes to one device. About 3% of repeatable requests (temperature 0, embeddings) are also run on a second device to check answers.</li>
<li>Devices are identified by a key, not by their owner's name. Owners don't learn who sent a request.</li>
<li>Owners who tamper with results lose earnings and are removed.</li>
</ul>

<h2>Rented capacity</h2>
<p>When no device is free, a request may be served by a rented cloud provider instead. The response header <code>X-Infera-Route</code> says <code>device</code> or <code>fallback</code>.</p>

<h2>Contributors (device owners)</h2>
<p>We keep your email, device names, a hash of each device's own machine ID (to stop duplicate accounts), benchmark results, earnings, and your USDT payout address. Payout addresses are checked against sanctions lists. Identity checks apply above a lifetime payout threshold.</p>

<p><a href="/performance">Performance</a> · <a href="/status">Status</a> · <a href="/">Infera</a></p></main></body></html>`
