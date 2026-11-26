//! The node loop: connection to the hub, idle rules (governor), offers, sealed jobs, receipts.

use crate::config::Config;
use crate::engine::{Engine, Event};
use crate::seal::SandboxKey;
use anyhow::Result;
use base64::{engine::general_purpose::STANDARD as B64, Engine as _};
use ed25519_dalek::{Signer, SigningKey};
use futures_util::{SinkExt, StreamExt};
use serde::Serialize;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::collections::{HashMap, VecDeque};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use tokio::sync::{mpsc, oneshot, watch};
use tokio_tungstenite::tungstenite::Message;

#[derive(Serialize, Clone, Default)]
pub struct OfferView {
    pub job_id: String,
    pub job_type: String,
    pub model: String,
    pub est_pay_usd: f64,
    pub est_minutes: f64,
    pub per_hour_usd: f64,
    pub seconds_left: u64,
    #[serde(skip)]
    pub deadline: Option<Instant>,
}

#[derive(Serialize, Clone, Default)]
pub struct JobView {
    pub job_id: String,
    pub model: String,
    pub est_pay_usd: f64,
    pub running_s: u64,
    #[serde(skip)]
    pub started: Option<Instant>,
}

#[derive(Serialize, Default)]
pub struct Shared {
    pub connected: bool,
    pub online: bool,
    pub mode: String,
    pub status: String,
    pub pause_reason: String,
    /// Machine conditions allow new jobs (online, idle, on power, not suspended), ignoring any running job.
    pub ready: bool,
    #[serde(skip)]
    pub sent: String,
    pub draining: bool,
    pub quit_after_job: bool,
    /// Owner pause: no new jobs until this unix time.
    pub paused_until: u64,
    /// Set by the tray's Quit: finish any running job, then exit.
    pub quit_requested: bool,
    pub ui_port: u16,
    /// The first running job (shown on the local page and tray); see `running` for all of them.
    pub job: Option<JobView>,
    /// Jobs running right now (several when the engine has parallel slots).
    pub jobs_running: usize,
    /// Jobs this device takes at once.
    pub slots: usize,
    #[serde(skip)]
    pub running: std::collections::BTreeMap<String, (JobView, watch::Sender<bool>)>,
    pub offer: Option<OfferView>,
    pub hub: Value,
    pub remote: Value,
    pub bench: Value,
    pub model_id: String,
    /// Auto-approve limits (editable from the local page).
    pub limits: crate::config::Limits,
    pub events: VecDeque<String>,
    #[serde(skip)]
    pub decision: Option<oneshot::Sender<bool>>,
    #[serde(skip)]
    pub out: Option<mpsc::Sender<String>>,
}

impl Shared {
    pub fn log(&mut self, s: impl Into<String>) {
        let line = format!("{} {}", clock(), s.into());
        println!("{line}");
        self.events.push_front(line);
        self.events.truncate(30);
    }
}

pub type State = Arc<Mutex<Shared>>;

fn clock() -> String {
    let t = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_secs();
    format!("{:02}:{:02}:{:02}", (t / 3600) % 24, (t / 60) % 60, t % 60)
}

fn unix() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_secs()
}

pub fn send(state: &State, v: Value) {
    let tx = state.lock().unwrap().out.clone();
    if let Some(tx) = tx {
        let _ = tx.try_send(v.to_string());
    }
}

struct Ctx {
    cfg: Config,
    engine: Arc<Engine>,
    key: SigningKey,
    state: State,
    payloads: Mutex<HashMap<String, oneshot::Sender<(String, String, String)>>>,
    cancels: Mutex<HashMap<String, watch::Sender<bool>>>,
    quit: watch::Sender<bool>,
}

pub async fn run(cfg: Config, engine: Engine, key: SigningKey, state: State) -> Result<()> {
    let (quit_tx, mut quit_rx) = watch::channel(false);
    let ctx = Arc::new(Ctx { cfg, engine: Arc::new(engine), key, state, payloads: Mutex::new(HashMap::new()), cancels: Mutex::new(HashMap::new()), quit: quit_tx });

    // Ctrl-C: first press finishes the current job, then quits. Second press stops now (penalty).
    {
        let ctx = ctx.clone();
        tokio::spawn(async move {
            let mut presses = 0;
            loop {
                if tokio::signal::ctrl_c().await.is_err() {
                    return;
                }
                presses += 1;
                let running = ctx.state.lock().unwrap().job.clone();
                if running.is_none() {
                    send(&ctx.state, json!({"t": "bye", "reason": "drain_complete"}));
                    tokio::time::sleep(Duration::from_millis(300)).await;
                    let _ = ctx.quit.send(true);
                    return;
                }
                if presses == 1 {
                    let mut s = ctx.state.lock().unwrap();
                    s.draining = true;
                    s.quit_after_job = true;
                    s.log("Finishing the current job, then quitting. Press Ctrl-C again to stop now (counts as an owner abort: job pay lost, 2× debit, −10 dependability).");
                } else {
                    stop_now(&ctx.state, true);
                    tokio::time::sleep(Duration::from_millis(500)).await;
                    let _ = ctx.quit.send(true);
                    return;
                }
            }
        });
    }

    #[cfg(unix)]
    {
        let ctx = ctx.clone();
        tokio::spawn(async move {
            let Ok(mut term) = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) else { return };
            term.recv().await;
            let running = ctx.state.lock().unwrap().job.is_some();
            if running {
                {
                    let mut s = ctx.state.lock().unwrap();
                    s.draining = true;
                    s.quit_after_job = true;
                    s.log("System asked Infera to stop: finishing the current job (up to 25 s)");
                }
                tokio::time::sleep(Duration::from_secs(25)).await;
                if ctx.state.lock().unwrap().job.is_some() {
                    stop_now(&ctx.state, true);
                    tokio::time::sleep(Duration::from_millis(500)).await;
                }
            } else {
                send(&ctx.state, json!({"t": "bye", "reason": "drain_complete"}));
                tokio::time::sleep(Duration::from_millis(300)).await;
            }
            let _ = ctx.quit.send(true);
        });
    }

    // Quit requested from the tray: same as Ctrl-C once.
    {
        let ctx = ctx.clone();
        tokio::spawn(async move {
            loop {
                tokio::time::sleep(Duration::from_millis(300)).await;
                let (req, running) = {
                    let s = ctx.state.lock().unwrap();
                    (s.quit_requested, s.job.is_some())
                };
                if !req {
                    continue;
                }
                if running {
                    let mut s = ctx.state.lock().unwrap();
                    if !s.quit_after_job {
                        s.draining = true;
                        s.quit_after_job = true;
                        s.log("Quitting after the current job finishes");
                    }
                } else {
                    send(&ctx.state, json!({"t": "bye", "reason": "drain_complete"}));
                    tokio::time::sleep(Duration::from_millis(300)).await;
                    let _ = ctx.quit.send(true);
                    return;
                }
            }
        });
    }

    tokio::spawn(governor(ctx.clone()));
    tokio::spawn(remote_stats(ctx.clone()));

    let mut backoff = 2u64;
    loop {
        let res = tokio::select! {
            r = session(ctx.clone()) => r,
            _ = quit_rx.changed() => return Ok(()),
        };
        {
            let mut s = ctx.state.lock().unwrap();
            s.connected = false;
            s.out = None;
            match &res {
                Ok(()) => s.log("Disconnected from Infera; reconnecting"),
                Err(e) => s.log(format!("Connection problem: {e}; retrying in {backoff}s")),
            }
        }
        if *quit_rx.borrow() {
            return Ok(());
        }
        tokio::select! {
            _ = tokio::time::sleep(Duration::from_secs(backoff)) => {},
            _ = quit_rx.changed() => return Ok(()),
        }
        backoff = (backoff * 2).min(30);
    }
}

/// Recomputes the job summary and status: idle = can take another job, busy = all slots
/// in use (or working while conditions say "no new jobs"), paused = not taking jobs.
pub fn refresh_jobs(s: &mut Shared) {
    s.job = s.running.values().next().map(|(j, _)| j.clone());
    s.jobs_running = s.running.len();
    let slots = s.slots.max(1);
    s.status = if s.status == "starting" {
        s.status.clone()
    } else if s.running.len() >= slots || (!s.running.is_empty() && !(s.ready && s.online && !s.draining)) {
        "busy".into()
    } else if s.ready && s.online && !s.draining {
        "idle".into()
    } else {
        "paused".into()
    };
}

/// Stop the running job immediately. Counts as an owner action on the server.
pub fn stop_now(state: &State, quitting: bool) {
    let jobs: Vec<(String, watch::Sender<bool>)> = {
        let mut s = state.lock().unwrap();
        s.online = false;
        s.running.iter().map(|(id, (_, tx))| (id.clone(), tx.clone())).collect()
    };
    for (id, stop) in jobs {
        send(state, json!({"t": "abort", "job_id": id, "reason": "owner_stop"}));
        let _ = stop.send(true);
        state.lock().unwrap().log(format!("Stopped job {id} (owner abort)"));
    }
    if quitting {
        send(state, json!({"t": "bye", "reason": "owner_quit"}));
    }
}

fn ws_url(server: &str, id: &str, token: &str) -> String {
    let base = server.trim_end_matches('/').replacen("https://", "wss://", 1).replacen("http://", "ws://", 1);
    format!("{base}/ws/node?device_id={id}&token={token}")
}

async fn session(ctx: Arc<Ctx>) -> Result<()> {
    let (ws, _) = tokio_tungstenite::connect_async(ws_url(&ctx.cfg.server, &ctx.cfg.device_id, &ctx.cfg.token)).await?;
    let (mut sink, mut stream) = ws.split();
    let (tx, mut rx) = mpsc::channel::<String>(1024);
    {
        let mut s = ctx.state.lock().unwrap();
        s.connected = true;
        s.out = Some(tx.clone());
        s.log(format!("Connected to {}", ctx.cfg.server));
    }
    let writer = tokio::spawn(async move {
        while let Some(m) = rx.recv().await {
            if sink.send(Message::Text(m)).await.is_err() {
                break;
            }
        }
    });

    let bench = ctx.cfg.bench.clone().unwrap_or_default();
    let mode = ctx.state.lock().unwrap().mode.clone();
    send(&ctx.state, json!({
        "t": "hello", "agent_version": env!("CARGO_PKG_VERSION"),
        "platform": format!("{} {}", std::env::consts::OS, std::env::consts::ARCH),
        "bench": bench, "class": bench.class, "privacy": "sandboxed", "models": served_models(&ctx.cfg),
        "mode": mode, "tps": bench.tokens_per_s, "slots": ctx.cfg.engine.slots(),
        "tools": ctx.cfg.engine.llama_server.is_empty(), // Ollama / LM Studio do tool calling
    }));
    push_state(&ctx.state);

    while let Some(msg) = stream.next().await {
        let msg = msg?;
        let Message::Text(text) = msg else { continue };
        let Ok(m) = serde_json::from_str::<Value>(&text) else { continue };
        let t = m["t"].as_str().unwrap_or("");
        let job_id = m["job_id"].as_str().unwrap_or("").to_string();
        match t {
            "config" => {
                let mut s = ctx.state.lock().unwrap();
                s.hub = m.clone();
            }
            "offer" => {
                tokio::spawn(handle_offer(ctx.clone(), m.clone()));
            }
            "payload" => {
                if let Some(slot) = ctx.payloads.lock().unwrap().remove(&job_id) {
                    let _ = slot.send((m["epk"].as_str().unwrap_or("").into(), m["n"].as_str().unwrap_or("").into(), m["c"].as_str().unwrap_or("").into()));
                }
            }
            "cancel" => {
                if let Some(c) = ctx.cancels.lock().unwrap().get(&job_id) {
                    let _ = c.send(true);
                }
                ctx.payloads.lock().unwrap().remove(&job_id);
            }
            "offer_expired" => {
                let mut s = ctx.state.lock().unwrap();
                if s.offer.as_ref().map(|o| o.job_id == job_id).unwrap_or(false) {
                    s.offer = None;
                    s.decision = None;
                }
            }
            _ => {}
        }
    }
    writer.abort();
    Ok(())
}

fn served_models(cfg: &Config) -> Vec<String> {
    let mut m = vec![cfg.engine.model_id.clone()];
    if !cfg.engine.embed_model.is_empty() && !cfg.engine.url.is_empty() {
        m.push(cfg.engine.embed_model.clone());
    }
    let media = !cfg.engine.media_url.is_empty() || !cfg.engine.media_cmd.is_empty();
    if !cfg.engine.stt_model.is_empty() && (!cfg.engine.stt_url.is_empty() || !cfg.engine.stt_server.is_empty() || (media && cfg.engine.media_stt)) {
        m.push(cfg.engine.stt_model.clone());
    }
    for x in [&cfg.engine.tts_model, &cfg.engine.video_model] {
        if media && !x.is_empty() {
            m.push(x.clone());
        }
    }
    m
}

pub fn push_state(state: &State) {
    let (status, online, mode) = {
        let mut s = state.lock().unwrap();
        s.sent = format!("{}|{}|{}", s.status, s.online, s.mode);
        (s.status.clone(), s.online, s.mode.clone())
    };
    send(state, json!({"t": "state", "status": status, "online": online, "mode": mode}));
}

async fn handle_offer(ctx: Arc<Ctx>, m: Value) {
    let job_id = m["job_id"].as_str().unwrap_or("").to_string();
    let model = m["model"].as_str().unwrap_or("").to_string();
    let est_pay = m["est_pay_usd"].as_f64().unwrap_or(0.0);
    let ttl = m["ttl_s"].as_u64().unwrap_or(30);
    let est_tokens = m["est_tokens"].as_f64().unwrap_or(0.0);

    let (status, mode) = {
        let s = ctx.state.lock().unwrap();
        (s.status.clone(), s.mode.clone())
    };
    if status != "idle" {
        send(&ctx.state, json!({"t": "decline", "job_id": job_id, "reason": "governor"}));
        return;
    }
    // Video needs the whole GPU: no video next to other jobs, nothing next to a video.
    let busy_gpu = {
        let s = ctx.state.lock().unwrap();
        let video = &ctx.cfg.engine.video_model;
        (m["job_type"].as_str() == Some("video") && !s.running.is_empty()) || (!video.is_empty() && s.running.values().any(|(j, _)| &j.model == video))
    };
    if busy_gpu {
        send(&ctx.state, json!({"t": "decline", "job_id": job_id, "reason": "busy"}));
        return;
    }
    let tps = ctx.cfg.bench.as_ref().map(|b| b.tokens_per_s).unwrap_or(10.0).max(1.0);
    let est_minutes = est_tokens / tps / 60.0;
    let limits = ctx.state.lock().unwrap().limits.clone();
    let outside = limits.outside(est_pay, est_minutes);
    if let (true, Some(why)) = (mode == "auto", &outside) {
        ctx.state.lock().unwrap().log(format!("Offer needs your answer: it {why}"));
    }

    let accepted = if mode == "auto" && outside.is_none() {
        true
    } else {
        // Uber-style offer card on the local page; no answer before the countdown ends = decline.
        let (tx, rx) = oneshot::channel();
        {
            let mut s = ctx.state.lock().unwrap();
            s.offer = Some(OfferView {
                job_id: job_id.clone(),
                job_type: m["job_type"].as_str().unwrap_or("chat").into(),
                model: model.clone(),
                est_pay_usd: est_pay,
                est_minutes,
                per_hour_usd: if est_minutes > 0.0 { est_pay / est_minutes * 60.0 } else { 0.0 },
                seconds_left: ttl,
                deadline: Some(Instant::now() + Duration::from_secs(ttl)),
            });
            s.decision = Some(tx);
            s.log(format!("New job offer: ${est_pay:.4} for about {:.1} min – open http://127.0.0.1:{} to accept", est_minutes.max(0.1), ctx.cfg.governor.ui_port));
        }
        let r = tokio::time::timeout(Duration::from_secs(ttl), rx).await;
        let mut s = ctx.state.lock().unwrap();
        s.offer = None;
        s.decision = None;
        matches!(r, Ok(Ok(true)))
    };
    if !accepted {
        send(&ctx.state, json!({"t": "decline", "job_id": job_id, "reason": "user_declined"}));
        return;
    }
    // Re-check the governor (and free slots) after a human decision.
    if ctx.state.lock().unwrap().status != "idle" {
        send(&ctx.state, json!({"t": "decline", "job_id": job_id, "reason": "governor"}));
        return;
    }

    let sandbox = SandboxKey::new();
    let (ptx, prx) = oneshot::channel();
    ctx.payloads.lock().unwrap().insert(job_id.clone(), ptx);
    let (ctx_tx, cancel_rx) = watch::channel(false);
    ctx.cancels.lock().unwrap().insert(job_id.clone(), ctx_tx.clone());
    {
        let mut s = ctx.state.lock().unwrap();
        let view = JobView { job_id: job_id.clone(), model: model.clone(), est_pay_usd: est_pay, running_s: 0, started: Some(Instant::now()) };
        s.running.insert(job_id.clone(), (view, ctx_tx));
        refresh_jobs(&mut s);
    }
    push_state(&ctx.state);
    send(&ctx.state, json!({"t": "accept", "job_id": job_id, "sandbox_pub": sandbox.public_b64}));

    let result = match tokio::time::timeout(Duration::from_secs(20), prx).await {
        Ok(Ok((epk, n, c))) if matches!(m["job_type"].as_str(), Some("embeddings" | "transcription" | "speech" | "video")) => {
            run_embed(&ctx, &job_id, &model, sandbox, &epk, &n, &c, m["job_type"].as_str().unwrap_or("")).await
        }
        Ok(Ok((epk, n, c))) => run_job(&ctx, &job_id, &model, sandbox, &epk, &n, &c, cancel_rx).await,
        _ => Err(anyhow::anyhow!("job payload never arrived")),
    };
    if let Err(e) = result {
        ctx.state.lock().unwrap().log(format!("Job {job_id} failed: {e}"));
    }

    ctx.cancels.lock().unwrap().remove(&job_id);
    let quit = {
        let mut s = ctx.state.lock().unwrap();
        s.running.remove(&job_id);
        if s.draining && s.running.is_empty() {
            s.draining = false;
            s.online = false;
            s.log("Jobs finished; now offline");
        }
        refresh_jobs(&mut s);
        s.quit_after_job && s.running.is_empty()
    };
    push_state(&ctx.state);
    if quit {
        send(&ctx.state, json!({"t": "bye", "reason": "drain_complete"}));
        tokio::time::sleep(Duration::from_millis(300)).await;
        let _ = ctx.quit.send(true);
    }
}

/// Embedding, transcription, speech or video job: one sealed result (sent in pieces of up
/// to 1 MB so large videos fit the connection's message limit), signed like any other job.
/// "tokens_in" in the receipt is audio seconds (transcription), input characters (speech)
/// or video seconds (video).
#[allow(clippy::too_many_arguments)]
async fn run_embed(ctx: &Arc<Ctx>, job_id: &str, model: &str, sandbox: SandboxKey, epk: &str, n: &str, c: &str, job_type: &str) -> Result<()> {
    let chan = sandbox.open(job_id, epk)?;
    let request: Value = serde_json::from_slice(&chan.open_down(n, c)?)?;
    let kind = match job_type { "transcription" => "Transcription", "speech" => "Speech", "video" => "Video", _ => "Embeddings" };
    ctx.state.lock().unwrap().log(format!("Running {} job {job_id} ({model}) in the sealed sandbox", kind.to_lowercase()));
    let res = match job_type {
        "transcription" => ctx.engine.transcribe(request).await,
        "speech" => ctx.engine.speak(request).await,
        "video" => ctx.engine.make_video(request).await,
        _ => ctx.engine.embed(request).await,
    };
    let (result, tin) = match res {
        Ok(r) => r,
        Err(e) => {
            send(&ctx.state, json!({"t": "abort", "job_id": job_id, "reason": "engine_error"}));
            return Err(e);
        }
    };
    let out = result.to_string();
    for (i, piece) in out.as_bytes().chunks(1 << 20).enumerate() {
        let (nonce, ct) = chan.seal_up(piece);
        send(&ctx.state, json!({"t": "tok", "job_id": job_id, "seq": i + 1, "n": nonce, "c": ct}));
    }
    let hash = hex::encode(Sha256::digest(out.as_bytes()));
    let receipt = format!("{job_id}|{model}|{hash}|{tin}|0|{}", unix());
    let sig = B64.encode(ctx.key.sign(receipt.as_bytes()).to_bytes());
    send(&ctx.state, json!({"t": "done", "job_id": job_id, "tokens_in": tin, "tokens_out": 0, "receipt": receipt, "sig": sig}));
    let unit = match job_type { "transcription" => "s of audio", "speech" => "characters", "video" => "s of video", _ => "tokens" };
    ctx.state.lock().unwrap().log(format!("{kind} job {job_id} done ({tin} {unit})"));
    Ok(())
}

#[allow(clippy::too_many_arguments)]
async fn run_job(ctx: &Arc<Ctx>, job_id: &str, model: &str, sandbox: SandboxKey, epk: &str, n: &str, c: &str, mut cancel: watch::Receiver<bool>) -> Result<()> {
    let chan = sandbox.open(job_id, epk)?;
    let plain = chan.open_down(n, c)?;
    let request: Value = serde_json::from_slice(&plain)?;
    ctx.state.lock().unwrap().log(format!("Running job {job_id} ({model}) in the sealed sandbox"));

    let (tx, mut rx) = mpsc::channel::<Event>(512);
    let engine = ctx.engine.clone();
    let gen = tokio::spawn(async move { engine.chat(request, tx).await });
    let mut out = String::new();
    let mut seq = 0u32;
    let started = Instant::now();
    loop {
        tokio::select! {
            _ = cancel.changed() => {
                gen.abort();
                ctx.state.lock().unwrap().log(format!("Job {job_id} stopped"));
                return Ok(());
            }
            ev = rx.recv() => match ev {
                Some(Event::Delta(t)) => {
                    out.push_str(&t);
                    seq += 1;
                    let (nonce, ct) = chan.seal_up(t.as_bytes());
                    send(&ctx.state, json!({"t": "tok", "job_id": job_id, "seq": seq, "n": nonce, "c": ct}));
                }
                Some(Event::Reasoning(t)) => {
                    // Reasoning is sealed and hashed into the receipt like answer text, flagged with "r".
                    out.push_str(&t);
                    seq += 1;
                    let (nonce, ct) = chan.seal_up(t.as_bytes());
                    send(&ctx.state, json!({"t": "tok", "job_id": job_id, "seq": seq, "n": nonce, "c": ct, "r": true}));
                }
                Some(Event::ToolCall(t)) => {
                    // A tool call (coding agents): sealed and hashed like text, flagged "k":"tool".
                    out.push_str(&t);
                    seq += 1;
                    let (nonce, ct) = chan.seal_up(t.as_bytes());
                    send(&ctx.state, json!({"t": "tok", "job_id": job_id, "seq": seq, "n": nonce, "c": ct, "k": "tool"}));
                }
                Some(Event::Done { tokens_in, tokens_out }) => {
                    let hash = hex::encode(Sha256::digest(out.as_bytes()));
                    let receipt = format!("{job_id}|{model}|{hash}|{tokens_in}|{tokens_out}|{}", unix());
                    let sig = B64.encode(ctx.key.sign(receipt.as_bytes()).to_bytes());
                    send(&ctx.state, json!({"t": "done", "job_id": job_id, "tokens_in": tokens_in, "tokens_out": tokens_out, "receipt": receipt, "sig": sig}));
                    ctx.state.lock().unwrap().log(format!("Job {job_id} done: {tokens_out} tokens in {:.1}s", started.elapsed().as_secs_f64()));
                    return Ok(());
                }
                None => {
                    let err = match gen.await { Ok(Err(e)) => e.to_string(), _ => "engine stopped".into() };
                    send(&ctx.state, json!({"t": "abort", "job_id": job_id, "reason": "engine_error"}));
                    anyhow::bail!("engine error: {err}");
                }
            }
        }
    }
}

/// Governor (ARCHITECTURE.md §3): new jobs only while online, on mains power and while other
/// programs leave the machine mostly idle. A running job always continues.
async fn governor(ctx: Arc<Ctx>) {
    let mut sys = sysinfo::System::new();
    let me = sysinfo::get_current_pid().ok();
    let mut last_sent = Instant::now() - Duration::from_secs(60);
    let mut tick: u64 = 0;
    let mut last_gpu: Option<(f32, f32)> = None;
    loop {
        sys.refresh_cpu_usage();
        sys.refresh_processes(sysinfo::ProcessesToUpdate::All, true);
        let cores = sys.cpus().len().max(1) as f32;
        let total = sys.global_cpu_usage();
        let mut ours = 0.0f32;
        for (pid, p) in sys.processes() {
            if Some(*pid) == me || (p.parent().is_some() && p.parent() == me) {
                ours += p.cpu_usage();
            }
        }
        let others = (total - ours / cores).max(0.0);
        let ac = on_ac_power();
        let input_idle = crate::idle::input_idle();
        sys.refresh_memory();
        let free_ram = if sys.total_memory() > 0 { sys.available_memory() as f32 * 100.0 / sys.total_memory() as f32 } else { 100.0 };
        let gpu = if tick % 3 == 0 { crate::limits::nvidia() } else { last_gpu };
        last_gpu = gpu;
        tick += 1;
        let fullscreen = crate::limits::fullscreen_busy().unwrap_or(false);
        let gcfg = &ctx.cfg.governor;

        let changed = {
            let mut s = ctx.state.lock().unwrap();
            if let Some(o) = s.offer.as_mut() {
                if let Some(d) = o.deadline {
                    o.seconds_left = d.saturating_duration_since(Instant::now()).as_secs();
                }
            }
            for (j, _) in s.running.values_mut() {
                if let Some(st) = j.started {
                    j.running_s = st.elapsed().as_secs();
                }
            }
            let suspended = s.hub["suspended_until"].as_u64().map(|u| u > unix()).unwrap_or(false);
            let paused_by_owner = s.paused_until > unix();
            let reason = if !s.online {
                "you are offline"
            } else if paused_by_owner {
                "paused by you"
            } else if !crate::limits::within_hours(&gcfg.allowed_hours) {
                "outside your allowed hours"
            } else if s.draining {
                "finishing up"
            } else if suspended {
                "suspended by Infera (see dashboard)"
            } else if ctx.cfg.governor.require_ac && ac == Some(false) {
                "on battery"
            } else if ctx.cfg.governor.idle_minutes > 0.0
                && input_idle.map(|d| d.as_secs_f64() < ctx.cfg.governor.idle_minutes * 60.0).unwrap_or(false)
            {
                "you're using the computer"
            } else if fullscreen {
                "a game or fullscreen app is running"
            } else if s.job.is_none() && gpu.map(|(u, _)| u > gcfg.max_other_gpu).unwrap_or(false) {
                "the GPU is busy with another program"
            } else if gpu.map(|(_, t)| t >= gcfg.max_gpu_temp).unwrap_or(false) {
                "the GPU is hot"
            } else if free_ram < gcfg.min_free_ram {
                "memory is low"
            } else if others > ctx.cfg.governor.max_other_cpu {
                "computer is in use"
            } else {
                ""
            };
            s.ready = reason.is_empty();
            s.pause_reason = if s.job.is_some() { String::new() } else { reason.into() };
            refresh_jobs(&mut s);
            let changed = format!("{}|{}|{}", s.status, s.online, s.mode) != s.sent;
            changed
        };
        if changed || last_sent.elapsed() > Duration::from_secs(30) {
            push_state(&ctx.state);
            last_sent = Instant::now();
        }
        tokio::time::sleep(Duration::from_secs(2)).await;
    }
}

fn on_ac_power() -> Option<bool> {
    #[cfg(target_os = "linux")]
    {
        let dir = std::fs::read_dir("/sys/class/power_supply").ok()?;
        let mut saw_mains = false;
        for e in dir.flatten() {
            let p = e.path();
            let kind = std::fs::read_to_string(p.join("type")).unwrap_or_default();
            if kind.trim() == "Mains" {
                saw_mains = true;
                if std::fs::read_to_string(p.join("online")).unwrap_or_default().trim() == "1" {
                    return Some(true);
                }
            }
        }
        if saw_mains {
            Some(false)
        } else {
            None
        }
    }
    #[cfg(target_os = "macos")]
    {
        let o = std::process::Command::new("pmset").args(["-g", "batt"]).output().ok()?;
        Some(String::from_utf8_lossy(&o.stdout).contains("AC Power"))
    }
    #[cfg(not(any(target_os = "linux", target_os = "macos")))]
    {
        None
    }
}

/// Earnings and dependability for the local page.
async fn remote_stats(ctx: Arc<Ctx>) {
    let http = reqwest::Client::new();
    loop {
        let url = format!("{}/api/device/me?device_id={}", ctx.cfg.server.trim_end_matches('/'), ctx.cfg.device_id);
        if let Ok(r) = http.get(url).bearer_auth(&ctx.cfg.token).send().await {
            if let Ok(v) = r.json::<Value>().await {
                ctx.state.lock().unwrap().remote = v;
            }
        }
        tokio::time::sleep(Duration::from_secs(15)).await;
    }
}
