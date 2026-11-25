//! `infera setup <code>`: everything a contributor needs in one command — pair, pick the right
//! model for this computer from the server's catalog, download it through Ollama, benchmark,
//! and start at login.

use anyhow::{anyhow, bail, Result};
use futures_util::StreamExt;
use serde::Deserialize;
use std::io::Write;

pub const DEFAULT_SERVER: &str = "https://infera.valanor.network";
const OLLAMA: &str = "http://127.0.0.1:11434";

#[derive(Deserialize, Clone, Debug)]
struct Model {
    id: String,
    label: String,
    kind: String,
    params_b: f64,
    #[serde(default)]
    ollama_tag: String,
    #[serde(default)]
    desktop_min_ram_gb: f64,
    #[serde(default)]
    desktop_min_vram_gb: f64,
    #[serde(default)]
    gguf_url: String,
}

pub async fn run(code: String, server: String, no_service: bool, want: String) -> Result<()> {
    let server = server.trim_end_matches('/').to_string();
    println!("1/5 Pairing with {server} …");
    crate::pair(code, server.clone(), String::new()).await?;

    println!("2/5 Checking this computer …");
    let mut sys = sysinfo::System::new();
    sys.refresh_memory();
    let ram = sys.total_memory() as f64 / 1e9;
    let (gpu, vram) = detect_gpu(ram).await;
    println!("    {:.0} GB RAM · GPU: {gpu}{}", ram, if vram > 0.0 { format!(" ({vram:.0} GB)") } else { String::new() });

    let http = reqwest::Client::new();
    if http.get(format!("{OLLAMA}/api/tags")).send().await.is_err() {
        println!("\nInfera uses Ollama to run models, and it isn't running on this computer.");
        if cfg!(target_os = "macos") {
            println!("Install it from https://ollama.com/download (or: brew install ollama), open it once, then run this command again.");
        } else if cfg!(target_os = "windows") {
            println!("Install it from https://ollama.com/download/windows, then run this command again.");
        } else {
            println!("Install it with:  curl -fsSL https://ollama.com/install.sh | sh   then run this command again.");
        }
        bail!("Ollama not found");
    }

    let catalog: Vec<Model> = {
        #[derive(Deserialize)]
        struct C {
            models: Vec<Model>,
        }
        http.get(format!("{server}/api/catalog")).send().await?.json::<C>().await?.models
    };
    let apple = gpu.starts_with("Apple");
    let fits = |m: &Model| {
        !m.ollama_tag.is_empty()
            && ram >= m.desktop_min_ram_gb
            && (m.desktop_min_vram_gb == 0.0 || vram >= m.desktop_min_vram_gb || (apple && ram >= m.desktop_min_ram_gb))
    };
    // Without a GPU, stay at small models so the device stays fast enough to earn.
    let chat = if !want.is_empty() {
        // Owner's choice (e.g. --model deepseek-r1-distill-llama-8b).
        let m = catalog.iter().find(|m| m.id == want && m.kind == "chat" && !m.ollama_tag.is_empty()).cloned()
            .ok_or_else(|| anyhow!("unknown model {want}; models: {}", catalog.iter().filter(|m| m.kind == "chat").map(|m| m.id.as_str()).collect::<Vec<_>>().join(", ")))?;
        if !fits(&m) {
            println!("    Note: {} may be too big for this computer; the benchmark will tell.", m.label);
        }
        m
    } else {
        // Largest model that fits; on a tie, the one listed first in the catalog.
        catalog
            .iter()
            .enumerate()
            .filter(|(_, m)| m.kind == "chat" && fits(m) && (vram > 0.0 || apple || m.params_b <= 2.0))
            .max_by(|(i, a), (j, b)| a.params_b.partial_cmp(&b.params_b).unwrap().then(j.cmp(i)))
            .map(|(_, m)| m.clone())
            .ok_or_else(|| anyhow!("no model in the catalog fits this computer"))?
    };
    let embed = catalog.iter().find(|m| m.kind == "embeddings" && fits(m)).cloned();
    println!("3/5 Best model for this computer: {}", chat.label);

    pull(&http, &chat.ollama_tag).await?;
    if let Some(e) = &embed {
        println!("    Also serving embeddings: {}", e.label);
        pull(&http, &e.ollama_tag).await?;
    }

    // Transcription (the best earner per hour): Macs get whisper.cpp from Homebrew.
    let speech = catalog.iter().find(|m| m.kind == "transcription" && !m.gguf_url.is_empty()).cloned();
    let stt = match &speech {
        Some(m) if cfg!(target_os = "macos") => setup_whisper(&http, m).await,
        _ => None,
    };

    let mut c = crate::config::load()?;
    // Jobs at once: more on machines with lots of memory (each job needs its own cache).
    c.engine.parallel = if (apple && ram >= 32.0) || vram >= 16.0 { 4 } else { 2 };
    if let (Some(m), Some((server, model))) = (&speech, &stt) {
        c.engine.stt_server = server.clone();
        c.engine.stt_gguf = model.clone();
        c.engine.stt_model = m.id.clone();
        c.engine.stt_url.clear();
        println!("    Also serving transcription: {}", m.label);
    }
    c.engine.url = OLLAMA.into();
    c.engine.llama_server.clear();
    c.engine.gguf.clear();
    c.engine.model_id = chat.id.clone();
    c.engine.engine_model = chat.ollama_tag.clone();
    c.engine.params_b = chat.params_b;
    c.engine.embed_model = embed.as_ref().map(|e| e.id.clone()).unwrap_or_default();
    c.engine.embed_engine_model = embed.as_ref().map(|e| e.ollama_tag.clone()).unwrap_or_default();
    c.bench = None;
    crate::config::save(&c)?;

    println!("4/5 Benchmarking (about a minute) …");
    let mut e = crate::engine::Engine::start(&c.engine).await?;
    let b = crate::engine::benchmark(&e, &c.engine).await;
    e.stop().await;
    let b = b?;
    println!("    {:.1} tokens/s · class {} – {}", b.tokens_per_s, b.class, b.reason);
    c.bench = Some(b);
    crate::config::save(&c)?;

    if no_service {
        println!("5/5 Done. Start earning with:  infera tray   (or infera run)");
        return Ok(());
    }
    println!("5/5 Starting Infera now and at every login …");
    match crate::service::run("install") {
        Ok(()) => println!("\nDone. Infera runs in your menu bar / system tray. Local page: http://127.0.0.1:{}", c.governor.ui_port),
        Err(err) => println!("\nCouldn't set up automatic start ({err}).\nStart it yourself with:  infera tray"),
    }
    Ok(())
}

/// macOS: install whisper.cpp with Homebrew (if missing) and download the Whisper model.
/// Returns (whisper-server path, model path), or None if it can't be set up.
async fn setup_whisper(http: &reqwest::Client, m: &Model) -> Option<(String, String)> {
    let which = |b: &str| {
        std::process::Command::new("sh").args(["-c", &format!("command -v {b}")]).output().ok().filter(|o| o.status.success())
            .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
    };
    let server = match which("whisper-server") {
        Some(p) => p,
        None => {
            which("brew")?;
            println!("    Installing whisper.cpp (speech to text) with Homebrew …");
            let ok = std::process::Command::new("brew").args(["install", "whisper-cpp"]).status().map(|s| s.success()).unwrap_or(false);
            if !ok {
                println!("    Couldn't install whisper.cpp; skipping transcription.");
                return None;
            }
            which("whisper-server")?
        }
    };
    let dir = crate::config::dir().join("models");
    std::fs::create_dir_all(&dir).ok()?;
    let path = dir.join(format!("{}.bin", m.id));
    if !path.exists() {
        print!("    Downloading {} …", m.label);
        std::io::stdout().flush().ok();
        let bytes = http.get(&m.gguf_url).send().await.ok()?.error_for_status().ok()?.bytes().await.ok()?;
        std::fs::write(&path, &bytes).ok()?;
        println!(" {} MB", bytes.len() / 1_000_000);
    }
    Some((server, path.display().to_string()))
}

async fn pull(http: &reqwest::Client, tag: &str) -> Result<()> {
    print!("    Downloading {tag} …");
    std::io::stdout().flush().ok();
    let resp = http.post(format!("{OLLAMA}/api/pull")).json(&serde_json::json!({"name": tag, "stream": true})).send().await?;
    if !resp.status().is_success() {
        bail!("Ollama couldn't download {tag} ({})", resp.status());
    }
    let mut stream = resp.bytes_stream();
    let mut buf = String::new();
    let mut last = -1i64;
    while let Some(chunk) = stream.next().await {
        buf.push_str(&String::from_utf8_lossy(&chunk?));
        while let Some(i) = buf.find('\n') {
            let line: String = buf.drain(..=i).collect();
            let Ok(v) = serde_json::from_str::<serde_json::Value>(line.trim()) else { continue };
            if let Some(err) = v["error"].as_str() {
                bail!("Ollama: {err}");
            }
            if let (Some(done), Some(total)) = (v["completed"].as_i64(), v["total"].as_i64()) {
                let pct = if total > 0 { done * 100 / total } else { 0 };
                if pct / 5 != last / 5 {
                    print!("\r    Downloading {tag} … {pct}% of {} MB   ", total / 1_000_000);
                    std::io::stdout().flush().ok();
                    last = pct;
                }
            }
        }
    }
    println!("\r    Downloaded {tag}                                  ");
    Ok(())
}

async fn detect_gpu(ram_gb: f64) -> (String, f64) {
    if let Ok(o) = tokio::process::Command::new("nvidia-smi").args(["--query-gpu=name,memory.total", "--format=csv,noheader,nounits"]).output().await {
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
        return ("Apple Silicon".into(), ram_gb);
    }
    ("none".into(), 0.0)
}
