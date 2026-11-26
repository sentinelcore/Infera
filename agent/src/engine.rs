//! Engine adapter: talks to any OpenAI-compatible local server (llama.cpp `llama-server`,
//! Ollama, LM Studio, MLX server). Optionally starts and stops `llama-server` itself.

use crate::config::EngineConfig;
use anyhow::{anyhow, bail, Result};
use futures_util::StreamExt;
use serde::{Deserialize, Serialize};
use std::process::Stdio;
use std::time::{Duration, Instant};
use tokio::process::{Child, Command};
use tokio::sync::mpsc;

pub struct Engine {
    pub url: String,
    pub model: String,
    pub embed_model: String,
    /// Speech-to-text endpoint base URL and engine model name (empty = not served).
    pub stt_url: String,
    pub stt_model: String,
    child: Option<Child>,
    stt_child: Option<Child>,
    http: reqwest::Client,
}

pub enum Event {
    /// Answer text.
    Delta(String),
    /// Reasoning ("thinking") text from models that stream it separately (Qwen3, DeepSeek-R1 …).
    Reasoning(String),
    Done { tokens_in: u32, tokens_out: u32 },
}

impl Engine {
    pub async fn start(cfg: &EngineConfig) -> Result<Engine> {
        let http = reqwest::Client::builder().connect_timeout(Duration::from_secs(5)).build()?;
        let model = if cfg.engine_model.is_empty() { cfg.model_id.clone() } else { cfg.engine_model.clone() };
        let embed_model = if cfg.embed_engine_model.is_empty() { cfg.embed_model.clone() } else { cfg.embed_engine_model.clone() };
        // Speech: an existing server (stt_url), or a whisper-server Infera starts itself.
        let (mut stt_url, mut stt_child) = (cfg.stt_url.trim_end_matches('/').to_string(), None);
        if stt_url.is_empty() && !cfg.stt_server.is_empty() && !cfg.stt_gguf.is_empty() {
            let port = 18178;
            let threads = std::thread::available_parallelism().map(|n| n.get()).unwrap_or(4).min(8);
            match Command::new(&cfg.stt_server)
                .args(["-m", &cfg.stt_gguf, "--host", "127.0.0.1", "--port", &port.to_string(), "--inference-path", "/v1/audio/transcriptions", "-t", &threads.to_string()])
                .stdout(Stdio::null())
                .stderr(Stdio::null())
                .kill_on_drop(true)
                .spawn()
            {
                Ok(c) => {
                    stt_child = Some(c);
                    stt_url = format!("http://127.0.0.1:{port}");
                }
                Err(e) => eprintln!("transcription off: could not start {}: {e}", cfg.stt_server),
            }
        }
        let stt_model = if cfg.stt_engine_model.is_empty() { cfg.stt_model.clone() } else { cfg.stt_engine_model.clone() };
        if !cfg.url.is_empty() {
            let e = Engine { url: cfg.url.trim_end_matches('/').to_string(), model, embed_model, stt_url, stt_model, child: None, stt_child, http };
            e.wait_ready(Duration::from_secs(10)).await?;
            return Ok(e);
        }
        if cfg.llama_server.is_empty() || cfg.gguf.is_empty() {
            bail!("no engine configured – run `infera engine --url <openai-compatible url> --model <id>` or `infera engine --llama-server <path> --gguf <file> --model <id>`");
        }
        let port = 18089;
        let mut cmd = Command::new(&cfg.llama_server);
        cmd.arg("-m").arg(&cfg.gguf)
            .arg("--host").arg("127.0.0.1")
            .arg("--port").arg(port.to_string())
            // Each parallel slot gets its own context window.
            .arg("-c").arg((if cfg.ctx > 0 { cfg.ctx } else { 4096 } * cfg.slots() as u32).to_string())
            .arg("--parallel").arg(cfg.slots().to_string())
            .arg("-ngl").arg(if cfg.gpu_layers != 0 { cfg.gpu_layers } else { 99 }.to_string())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .kill_on_drop(true);
        let child = cmd.spawn().map_err(|e| anyhow!("could not start llama-server at {}: {e}", cfg.llama_server))?;
        let e = Engine { url: format!("http://127.0.0.1:{port}"), model, embed_model, stt_url, stt_model, child: Some(child), stt_child, http };
        e.wait_ready(Duration::from_secs(180)).await?;
        Ok(e)
    }

    async fn wait_ready(&self, max: Duration) -> Result<()> {
        let t = Instant::now();
        while t.elapsed() < max {
            if let Ok(r) = self.http.get(format!("{}/v1/models", self.url)).send().await {
                if r.status().is_success() {
                    return Ok(());
                }
            }
            tokio::time::sleep(Duration::from_millis(500)).await;
        }
        bail!("engine at {} did not become ready", self.url)
    }

    /// Streams a chat completion. `request` is the customer's OpenAI-style JSON.
    pub async fn chat(&self, mut request: serde_json::Value, tx: mpsc::Sender<Event>) -> Result<()> {
        if let Some(o) = request.as_object_mut() {
            o.insert("model".into(), self.model.clone().into());
            o.insert("stream".into(), true.into());
            o.insert("stream_options".into(), serde_json::json!({"include_usage": true}));
            o.remove("privacy");
            o.remove("infera_batch");
        }
        // Text completion jobs (code autocomplete: "prompt" + optional "suffix") use the engine's
        // /v1/completions; Ollama builds the model's fill-in-the-middle prompt from the suffix.
        let path = if request.get("prompt").is_some() { "/v1/completions" } else { "/v1/chat/completions" };
        let mut resp = self.http.post(format!("{}{path}", self.url)).json(&request).send().await?;
        // Models without fill-in-the-middle support reject "suffix" ("does not support insert"):
        // retry as a plain continuation of the prompt.
        if resp.status() == reqwest::StatusCode::BAD_REQUEST && request.get("suffix").is_some() {
            if let Some(o) = request.as_object_mut() {
                o.remove("suffix");
            }
            resp = self.http.post(format!("{}{path}", self.url)).json(&request).send().await?;
        }
        if !resp.status().is_success() {
            bail!("engine returned {}", resp.status());
        }
        let mut stream = resp.bytes_stream();
        let mut buf = String::new();
        let (mut tin, mut tout, mut pieces) = (0u32, 0u32, 0u32);
        while let Some(chunk) = stream.next().await {
            buf.push_str(&String::from_utf8_lossy(&chunk?));
            while let Some(i) = buf.find('\n') {
                let line = buf[..i].trim().to_string();
                buf.drain(..=i);
                let Some(data) = line.strip_prefix("data:") else { continue };
                let data = data.trim();
                if data == "[DONE]" {
                    continue;
                }
                let Ok(v) = serde_json::from_str::<serde_json::Value>(data) else { continue };
                if let Some(u) = v.get("usage").filter(|u| !u.is_null()) {
                    tin = u["prompt_tokens"].as_u64().unwrap_or(0) as u32;
                    tout = u["completion_tokens"].as_u64().unwrap_or(0) as u32;
                }
                let delta = &v["choices"][0]["delta"];
                let reasoning = delta["reasoning_content"].as_str().or_else(|| delta["reasoning"].as_str()).unwrap_or("");
                if !reasoning.is_empty() {
                    pieces += 1;
                    if tx.send(Event::Reasoning(reasoning.to_string())).await.is_err() {
                        return Ok(());
                    }
                }
                // Chat streams "delta.content"; text completions stream "text".
                if let Some(t) = delta["content"].as_str().or_else(|| v["choices"][0]["text"].as_str()) {
                    if !t.is_empty() {
                        pieces += 1;
                        if tx.send(Event::Delta(t.to_string())).await.is_err() {
                            return Ok(()); // cancelled
                        }
                    }
                }
            }
        }
        if tout == 0 {
            tout = pieces;
        }
        let _ = tx.send(Event::Done { tokens_in: tin, tokens_out: tout }).await;
        Ok(())
    }

    /// Embeddings: returns the result JSON ({"data": [...], "usage": ...}) and the prompt token count.
    pub async fn embed(&self, mut request: serde_json::Value) -> Result<(serde_json::Value, u32)> {
        if self.embed_model.is_empty() {
            bail!("this device doesn't serve embeddings");
        }
        if let Some(o) = request.as_object_mut() {
            o.insert("model".into(), self.embed_model.clone().into());
            o.remove("privacy");
            o.remove("infera_batch");
        }
        let resp = self.http.post(format!("{}/v1/embeddings", self.url)).json(&request).send().await?;
        if !resp.status().is_success() {
            bail!("engine returned {} for embeddings", resp.status());
        }
        let v: serde_json::Value = resp.json().await?;
        let tin = v["usage"]["prompt_tokens"].as_u64().unwrap_or(0) as u32;
        Ok((serde_json::json!({"data": v["data"], "usage": v["usage"]}), tin))
    }

    /// Speech to text. `request`: {"audio_b64", "filename", "language", "prompt"}.
    /// Returns ({"text", "duration", "language", "segments"}, audio seconds).
    pub async fn transcribe(&self, request: serde_json::Value) -> Result<(serde_json::Value, u32)> {
        use base64::{engine::general_purpose::STANDARD as B64, Engine as _};
        if self.stt_url.is_empty() {
            bail!("this device doesn't serve transcription");
        }
        let audio = B64.decode(request["audio_b64"].as_str().unwrap_or(""))?;
        let bytes = audio.len();
        let name = request["filename"].as_str().filter(|s| !s.is_empty()).unwrap_or("audio.wav").to_string();
        let mut form = reqwest::multipart::Form::new()
            .part("file", reqwest::multipart::Part::bytes(audio).file_name(name))
            .text("model", self.stt_model.clone())
            .text("response_format", "verbose_json");
        for k in ["language", "prompt"] {
            if let Some(v) = request[k].as_str().filter(|v| !v.is_empty()) {
                form = form.text(k, v.to_string());
            }
        }
        let resp = self.http.post(format!("{}/v1/audio/transcriptions", self.stt_url)).multipart(form).send().await?;
        if !resp.status().is_success() {
            bail!("speech engine returned {}", resp.status());
        }
        let v: serde_json::Value = resp.json().await?;
        // Audio length: from the engine, else a conservative estimate from the file size.
        let secs = v["duration"].as_f64().map(|d| d.ceil() as u32).unwrap_or(((bytes / 16_000) as u32).max(1)).max(1);
        let mut out = serde_json::json!({"text": v["text"], "duration": secs});
        for k in ["language", "segments"] {
            if !v[k].is_null() {
                out[k] = v[k].clone();
            }
        }
        Ok((out, secs))
    }

    pub async fn stop(&mut self) {
        if let Some(c) = self.child.as_mut() {
            let _ = c.kill().await;
        }
        if let Some(c) = self.stt_child.as_mut() {
            let _ = c.kill().await;
        }
    }
}

#[derive(Serialize, Deserialize, Clone, Debug, Default)]
pub struct BenchResult {
    pub cpu: String,
    pub cores: usize,
    pub ram_gb: f64,
    pub gpu: String,
    pub vram_gb: f64,
    pub model_id: String,
    pub params_b: f64,
    pub tokens_per_s: f64,
    pub first_token_ms: u64,
    pub class: String,
    pub reason: String,
    /// Hardware/driver/OS fingerprint at benchmark time; a change triggers a re-benchmark.
    #[serde(default)]
    pub fingerprint: String,
    /// Unix time of the benchmark; older than 30 days triggers a re-benchmark.
    #[serde(default)]
    pub at: u64,
}

/// CPU + RAM + GPU + driver + OS: changes when hardware, drivers or the OS change.
pub async fn fingerprint() -> String {
    let mut sys = sysinfo::System::new();
    sys.refresh_cpu_all();
    sys.refresh_memory();
    let cpu = sys.cpus().first().map(|c| c.brand().trim().to_string()).unwrap_or_default();
    let ram = sys.total_memory() / 1_000_000_000;
    let gpu = match Command::new("nvidia-smi").args(["--query-gpu=name,driver_version", "--format=csv,noheader"]).output().await {
        Ok(o) if o.status.success() => String::from_utf8_lossy(&o.stdout).lines().next().unwrap_or("").trim().to_string(),
        _ => String::new(),
    };
    let os = sysinfo::System::long_os_version().unwrap_or_default();
    format!("{cpu}|{ram}GB|{gpu}|{os}")
}

/// Install-time benchmark (ARCHITECTURE.md §3.1): hardware, then a timed generation on the
/// configured model, then the class.
pub async fn benchmark(engine: &Engine, cfg: &EngineConfig) -> Result<BenchResult> {
    let mut sys = sysinfo::System::new_all();
    sys.refresh_all();
    let cpu = sys.cpus().first().map(|c| c.brand().trim().to_string()).unwrap_or_default();
    let ram_gb = sys.total_memory() as f64 / 1e9;
    let (gpu, vram_gb) = detect_gpu(ram_gb).await;

    let req = serde_json::json!({
        "messages": [{"role": "user", "content": "Write a short paragraph about how rivers shape valleys."}],
        "max_tokens": 128, "temperature": 0
    });
    let (tx, mut rx) = mpsc::channel(512);
    let t0 = Instant::now();
    let model_req = req.clone();
    let run = engine.chat(model_req, tx);
    let mut first: Option<Instant> = None;
    let mut n = 0u32;
    let mut reported = 0u32;
    let collect = async {
        while let Some(ev) = rx.recv().await {
            match ev {
                Event::Delta(_) | Event::Reasoning(_) => {
                    if first.is_none() {
                        first = Some(Instant::now());
                    }
                    n += 1;
                }
                Event::Done { tokens_out, .. } => reported = tokens_out,
            }
        }
    };
    let (r, _) = tokio::join!(run, collect);
    r?;
    let first = first.ok_or_else(|| anyhow!("the engine produced no output"))?;
    let gen_s = first.elapsed().as_secs_f64().max(0.001);
    let toks = reported.max(n).saturating_sub(1) as f64;
    let tps = toks / gen_s;
    let params = cfg.params_b;
    let (class, reason) = classify(tps, params, vram_gb, &gpu, ram_gb);
    Ok(BenchResult {
        cpu,
        cores: sys.cpus().len(),
        ram_gb: (ram_gb * 10.0).round() / 10.0,
        gpu,
        vram_gb: (vram_gb * 10.0).round() / 10.0,
        model_id: cfg.model_id.clone(),
        params_b: params,
        tokens_per_s: (tps * 10.0).round() / 10.0,
        first_token_ms: (first - t0).as_millis() as u64,
        class,
        reason,
        fingerprint: fingerprint().await,
        at: std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0),
    })
}

/// Class thresholds from ARCHITECTURE.md §3.1 (desktop and laptop classes).
pub fn classify(tps: f64, params_b: f64, vram_gb: f64, gpu: &str, ram_gb: f64) -> (String, String) {
    let apple = gpu.starts_with("Apple");
    let big_mem = vram_gb >= 16.0 || (apple && ram_gb >= 32.0);
    if params_b >= 7.0 && tps >= 40.0 && big_mem {
        return ("A".into(), format!("{tps:.0} tok/s on a {params_b}B model with {vram_gb:.0} GB GPU memory"));
    }
    if params_b >= 7.0 && tps >= 25.0 {
        return ("B".into(), format!("{tps:.0} tok/s on a {params_b}B model"));
    }
    if tps >= 10.0 {
        return ("C".into(), format!("{tps:.0} tok/s on a {params_b}B model"));
    }
    ("unrated".into(), format!("{tps:.1} tok/s is below the 10 tok/s minimum; try a smaller model"))
}

async fn detect_gpu(ram_gb: f64) -> (String, f64) {
    if let Ok(o) = Command::new("nvidia-smi").args(["--query-gpu=name,memory.total", "--format=csv,noheader,nounits"]).output().await {
        if o.status.success() {
            let s = String::from_utf8_lossy(&o.stdout);
            if let Some(line) = s.lines().next() {
                let mut it = line.split(',');
                let name = it.next().unwrap_or("").trim().to_string();
                let mb: f64 = it.next().unwrap_or("0").trim().parse().unwrap_or(0.0);
                return (name, mb / 1024.0);
            }
        }
    }
    if cfg!(target_os = "macos") && cfg!(target_arch = "aarch64") {
        return ("Apple Silicon (unified memory)".into(), ram_gb);
    }
    ("none detected".into(), 0.0)
}
