//! Local page at http://127.0.0.1:7777 – status, earnings, offer cards, online toggle, safe stop.

use crate::node::{push_state, stop_now, State};
use axum::{
    extract::State as AxState,
    http::{HeaderMap, StatusCode},
    response::{Html, IntoResponse},
    routing::{get, post},
    Json, Router,
};
use serde::Deserialize;
use serde_json::json;

pub async fn serve(state: State, port: u16) {
    let app = Router::new()
        .route("/", get(|| async { Html(PAGE) }))
        .route("/api/state", get(get_state))
        .route("/api/decide", post(decide))
        .route("/api/online", post(set_online))
        .route("/api/mode", post(set_mode))
        .route("/api/finish-then-offline", post(finish_then_offline))
        .route("/api/stop-now", post(stop))
        .route("/api/pause", post(pause))
        .with_state(state.clone());
    match tokio::net::TcpListener::bind(("127.0.0.1", port)).await {
        Ok(l) => {
            state.lock().unwrap().log(format!("Local page: http://127.0.0.1:{port}"));
            let _ = axum::serve(l, app).await;
        }
        Err(e) => state.lock().unwrap().log(format!("Local page not started (port {port}: {e})")),
    }
}

// Requests must carry X-Infera: 1, which other websites cannot send without a CORS preflight.
fn guard(h: &HeaderMap) -> Result<(), StatusCode> {
    if h.get("x-infera").map(|v| v == "1").unwrap_or(false) {
        Ok(())
    } else {
        Err(StatusCode::FORBIDDEN)
    }
}

async fn get_state(AxState(st): AxState<State>) -> impl IntoResponse {
    let s = st.lock().unwrap();
    Json(serde_json::to_value(&*s).unwrap_or(json!({})))
}

#[derive(Deserialize)]
struct Decide {
    accept: bool,
}
async fn decide(AxState(st): AxState<State>, h: HeaderMap, Json(d): Json<Decide>) -> Result<Json<serde_json::Value>, StatusCode> {
    guard(&h)?;
    let tx = st.lock().unwrap().decision.take();
    if let Some(tx) = tx {
        let _ = tx.send(d.accept);
    }
    Ok(Json(json!({"ok": true})))
}

#[derive(Deserialize)]
struct Online {
    online: bool,
}
async fn set_online(AxState(st): AxState<State>, h: HeaderMap, Json(o): Json<Online>) -> Result<Json<serde_json::Value>, StatusCode> {
    guard(&h)?;
    {
        let mut s = st.lock().unwrap();
        s.online = o.online;
        if s.job.is_none() {
            s.status = if o.online && s.ready && !s.draining { "idle".into() } else { "paused".into() };
        }
        let msg = if o.online { "You are online" } else { "You are offline" };
        s.log(msg);
    }
    push_state(&st);
    Ok(Json(json!({"ok": true})))
}

#[derive(Deserialize)]
struct Mode {
    mode: String,
}
async fn set_mode(AxState(st): AxState<State>, h: HeaderMap, Json(m): Json<Mode>) -> Result<Json<serde_json::Value>, StatusCode> {
    guard(&h)?;
    if m.mode != "auto" && m.mode != "ask" {
        return Err(StatusCode::BAD_REQUEST);
    }
    {
        let mut s = st.lock().unwrap();
        s.mode = m.mode.clone();
        s.log(if m.mode == "auto" { "Auto-approve on" } else { "Ask for every job" });
    }
    push_state(&st);
    Ok(Json(json!({"ok": true})))
}

async fn finish_then_offline(AxState(st): AxState<State>, h: HeaderMap) -> Result<Json<serde_json::Value>, StatusCode> {
    guard(&h)?;
    {
        let mut s = st.lock().unwrap();
        if s.job.is_some() {
            s.draining = true;
            s.log("Finishing the current job, then going offline");
        } else {
            s.online = false;
            s.log("You are offline");
        }
    }
    push_state(&st);
    Ok(Json(json!({"ok": true})))
}

#[derive(Deserialize)]
struct Pause {
    /// minutes to pause; 0 = resume; -1 = until tomorrow (local midnight)
    minutes: i64,
}
async fn pause(AxState(st): AxState<State>, h: HeaderMap, Json(p): Json<Pause>) -> Result<Json<serde_json::Value>, StatusCode> {
    guard(&h)?;
    {
        let mut s = st.lock().unwrap();
        let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs();
        s.paused_until = match p.minutes {
            0 => 0,
            m if m < 0 => crate::limits::next_midnight(),
            m => now + (m as u64) * 60,
        };
        let msg = if s.paused_until == 0 { "Resumed".to_string() } else { format!("Paused for new jobs for {} min", (s.paused_until.saturating_sub(now)) / 60) };
        s.log(msg);
        if s.job.is_none() && s.paused_until > 0 {
            s.status = "paused".into();
        }
    }
    push_state(&st);
    Ok(Json(json!({"ok": true})))
}

async fn stop(AxState(st): AxState<State>, h: HeaderMap) -> Result<Json<serde_json::Value>, StatusCode> {
    guard(&h)?;
    stop_now(&st, false);
    push_state(&st);
    Ok(Json(json!({"ok": true})))
}

const PAGE: &str = r#"<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Infera node</title>
<style>
:root{--bg:#f4f6f4;--surface:#fff;--surface-2:#e9eeeb;--fg:#14201b;--muted:#56655e;--line:#c9d3ce;--accent:#17795a;--accent-soft:#dcefe7;--money:#a86a00;--alert:#b3412e;--alert-soft:#f6e0db}
@media (prefers-color-scheme:dark){:root{--bg:#0e1412;--surface:#161f1b;--surface-2:#1d2924;--fg:#e3ebe7;--muted:#9aaba3;--line:#2f3e37;--accent:#4cc398;--accent-soft:#173a2e;--money:#e6ae45;--alert:#ee8a75;--alert-soft:#3d1f19;color-scheme:dark}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif;padding:0 16px 40px}
.wrap{max-width:520px;margin:0 auto}header{display:flex;justify-content:space-between;align-items:center;padding:18px 0}
.logo{font-weight:700;font-size:1.3rem;letter-spacing:-.02em}.pill{padding:5px 12px;border-radius:999px;border:1px solid var(--line);font-size:.85rem;cursor:pointer;background:var(--surface);color:var(--fg)}
.pill.on{background:var(--accent-soft);border-color:var(--accent);color:var(--accent);font-weight:600}
.card{background:var(--surface);border:1px solid var(--line);border-radius:14px;padding:16px;margin-top:12px}
.row{display:flex;justify-content:space-between;gap:10px;padding:4px 0}.k{color:var(--muted);font-size:.85rem}.v{font-variant-numeric:tabular-nums;font-weight:600}
.offer{border:2px solid var(--accent)}.pay{font-size:2.6rem;font-weight:700;color:var(--money);line-height:1.1}
.ring{float:right;font:600 .95rem ui-monospace,monospace;border:3px solid var(--accent);border-radius:50%;width:52px;height:52px;display:grid;place-items:center}
button.go{width:100%;padding:13px;border:0;border-radius:10px;background:var(--accent);color:var(--bg);font-size:1.05rem;font-weight:600;margin-top:12px;cursor:pointer}
button.lnk{width:100%;padding:9px;border:0;background:none;color:var(--muted);cursor:pointer;font-size:.95rem}
button.sec{padding:9px 12px;border:1px solid var(--line);border-radius:8px;background:var(--surface);color:var(--fg);cursor:pointer;width:100%;margin-top:8px}
button.danger{border-color:var(--alert);color:var(--alert)}
select{padding:7px;border-radius:8px;border:1px solid var(--line);background:var(--bg);color:var(--fg)}
.log{font:12px ui-monospace,monospace;color:var(--muted);white-space:pre-wrap;max-height:180px;overflow:auto}
.warn{background:var(--alert-soft);color:var(--alert);border-radius:8px;padding:8px 10px;font-size:.85rem;margin-top:8px}
button:focus-visible,select:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
</style></head><body><div class="wrap">
<header><div class="logo">Infera</div><button class="pill" id="online">…</button></header>
<div class="card"><div class="row"><span class="k">Today</span><span class="v" id="today">–</span></div>
<div class="row"><span class="k">Available · held</span><span class="v" id="bal">–</span></div>
<div class="row"><span class="k">Status</span><span class="v" id="status">–</span></div>
<div class="row"><span class="k">Class · dependability</span><span class="v" id="dep">–</span></div>
<div class="row"><span class="k">Model</span><span class="v" id="model">–</span></div></div>
<div class="card offer" id="offer" hidden>
 <div class="ring" id="secs">30</div><div class="k">NEW JOB OFFER</div>
 <div class="pay" id="opay">$0</div><div class="k" id="ohour"></div>
 <div class="row" style="margin-top:8px"><span class="k">Job</span><span class="v" id="otype"></span></div>
 <div class="row"><span class="k">Time</span><span class="v" id="otime"></span></div>
 <div class="row"><span class="k">Runs</span><span class="v">now</span></div>
 <button class="go" id="accept">Accept</button><button class="lnk" id="decline">Decline</button>
 <div class="k">Declining is free. Once accepted, the job must finish.</div></div>
<div class="card" id="jobcard" hidden><div class="k">RUNNING JOB · SEALED</div><div class="row"><span id="jobinfo"></span><span class="v" id="jobt"></span></div>
 <button class="sec" id="finish">Finish this job, then go offline</button>
 <button class="sec danger" id="stopnow">Stop now</button>
 <div class="warn" id="stopwarn" hidden></div></div>
<div class="card"><div class="row"><span class="k">Pause new jobs</span><span id="pauseinfo" class="k"></span></div>
 <div style="display:flex;gap:8px;flex-wrap:wrap"><button class="sec" style="width:auto" id="p60">1 hour</button><button class="sec" style="width:auto" id="ptom">Until tomorrow</button><button class="sec" style="width:auto" id="pres">Resume</button></div></div>
<div class="card"><div class="row"><span class="k">Job approval</span><select id="mode"><option value="auto">Auto-approve</option><option value="ask">Ask for every job</option></select></div></div>
<div class="card"><div class="k">Activity</div><div class="log" id="log"></div></div>
</div><script>
const $=id=>document.getElementById(id);
const post=(p,b)=>fetch(p,{method:"POST",headers:{"Content-Type":"application/json","X-Infera":"1"},body:JSON.stringify(b||{})});
const usd=m=>(m<0?"−$":"$")+(Math.abs(m)/1e6).toFixed(m&&Math.abs(m)<1e4?6:2);
let S={};
async function tick(){try{S=await (await fetch("/api/state")).json();}catch(e){return}
 $("online").textContent=S.online?"● Online":"Go online";$("online").className="pill"+(S.online?" on":"");
 const r=S.remote||{},b=r.balance||{},t=r.today||{};
 $("today").textContent=t.earned_today!=null?usd(t.earned_today)+" · "+t.jobs_today+" jobs":"–";
 $("bal").textContent=b.available!=null?usd(b.available)+" · "+usd(b.held):"–";
 $("status").textContent=(S.connected?"":"connecting · ")+S.status+(S.pause_reason?" ("+S.pause_reason+")":"");
 const h=S.hub||{};$("dep").textContent=(h.class||"?")+" · "+(h.dependability!=null?h.dependability.toFixed(1):"–")+(h.band?" ("+h.band.hold_days+"-day hold, "+h.band.multiplier+"× pay)":"");
 $("model").textContent=S.model_id||"–";
 if(document.activeElement!==$("mode"))$("mode").value=S.mode;
 const o=S.offer;$("offer").hidden=!o;
 if(o){$("secs").textContent=o.seconds_left+"s";$("opay").textContent="$"+o.est_pay_usd.toFixed(o.est_pay_usd<0.01?6:4);
  $("ohour").textContent="≈ $"+o.per_hour_usd.toFixed(2)+" per hour";$("otype").textContent=o.job_type.replace("_"," ")+" · "+o.model;
  $("otime").textContent="about "+Math.max(1,Math.ceil(o.est_minutes*60))+" s";}
 const j=S.job;$("jobcard").hidden=!j;
 if(j){$("jobinfo").textContent=j.model+" · earns ~$"+j.est_pay_usd.toFixed(4);$("jobt").textContent=j.running_s+"s";
  $("stopwarn").hidden=false;$("stopwarn").textContent="Stopping now loses this job's pay, debits 2× its value (~$"+(j.est_pay_usd*2).toFixed(4)+") and costs 10 dependability points.";}
 $("pauseinfo").textContent=S.paused_until*1000>Date.now()?"until "+new Date(S.paused_until*1000).toLocaleTimeString([], {hour:"2-digit",minute:"2-digit"}):"";
 $("log").textContent=(S.events||[]).join("\n");}
$("p60").onclick=()=>post("/api/pause",{minutes:60}).then(tick);
$("ptom").onclick=()=>post("/api/pause",{minutes:-1}).then(tick);
$("pres").onclick=()=>post("/api/pause",{minutes:0}).then(tick);
$("online").onclick=()=>post("/api/online",{online:!S.online}).then(tick);
$("mode").onchange=e=>post("/api/mode",{mode:e.target.value}).then(tick);
$("accept").onclick=()=>post("/api/decide",{accept:true}).then(tick);
$("decline").onclick=()=>post("/api/decide",{accept:false}).then(tick);
$("finish").onclick=()=>post("/api/finish-then-offline").then(tick);
$("stopnow").onclick=()=>{if($("stopnow").dataset.armed){post("/api/stop-now").then(tick);delete $("stopnow").dataset.armed;$("stopnow").textContent="Stop now";}
 else{$("stopnow").dataset.armed=1;$("stopnow").textContent="Tap again to stop now and take the penalty";}};
tick();setInterval(tick,1000);
</script></body></html>"#;
