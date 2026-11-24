//! Infera node – serves AI inference from an idle laptop or desktop.
mod config;
mod engine;
mod idle;
mod node;
mod seal;
mod service;
#[cfg(feature = "tray")]
mod tray;
mod ui;

use anyhow::{bail, Result};
use base64::{engine::general_purpose::STANDARD as B64, Engine as _};
use clap::{Parser, Subcommand};
use std::sync::{Arc, Mutex};

#[derive(Parser)]
#[command(name = "infera", version, about = "Infera node: earn by serving AI inference while your computer is idle")]
struct Cli {
    #[command(subcommand)]
    cmd: Cmd,
}

#[derive(Subcommand)]
enum Cmd {
    /// Pair this device with your account using the 6-digit code from the dashboard
    Pair {
        code: String,
        #[arg(long)]
        server: String,
        #[arg(long, default_value = "")]
        name: String,
    },
    /// Choose the model this device serves
    Engine {
        /// Use an OpenAI-compatible server already running here (llama-server, Ollama, LM Studio)
        #[arg(long, default_value = "")]
        url: String,
        /// Or let Infera start llama.cpp's llama-server itself
        #[arg(long, default_value = "")]
        llama_server: String,
        #[arg(long, default_value = "")]
        gguf: String,
        /// Model name customers will request, e.g. qwen3.5-2b
        #[arg(long)]
        model: String,
        /// Model name to send to the engine, if different (e.g. Ollama tag "qwen3:1.7b")
        #[arg(long, default_value = "")]
        engine_model: String,
        /// Model size in billions of parameters (used for the device class)
        #[arg(long)]
        params_b: f64,
    },
    /// Benchmark this device and show its class
    Bench,
    /// Start serving jobs (Ctrl-C once: finish the current job, then quit)
    Run {
        /// Ask for every job with an offer card instead of auto-approving
        #[arg(long)]
        ask: bool,
    },
    /// Show configuration and class
    Status,
    /// Run with a menu-bar / system-tray icon (desktop app mode)
    Tray {
        #[arg(long)]
        ask: bool,
    },
    /// Start Infera automatically when you log in: install | uninstall | status
    Service { action: String },
}

fn main() -> Result<()> {
    let cli = Cli::parse();
    // The tray must own the main thread (macOS requirement); everything else runs on tokio.
    if let Cmd::Tray { ask } = &cli.cmd {
        let ask = *ask;
        #[cfg(feature = "tray")]
        return tray::main(ask);
        #[cfg(not(feature = "tray"))]
        {
            let _ = ask;
            bail!("this build has no tray support; use `infera run`");
        }
    }
    tokio::runtime::Runtime::new()?.block_on(async_main(cli))
}

async fn async_main(cli: Cli) -> Result<()> {
    match cli.cmd {
        Cmd::Pair { code, server, name } => pair(code, server, name).await,
        Cmd::Engine { url, llama_server, gguf, model, engine_model, params_b } => {
            let mut c = config::load()?;
            c.engine.url = url;
            c.engine.llama_server = llama_server;
            c.engine.gguf = gguf;
            c.engine.model_id = model;
            c.engine.engine_model = engine_model;
            c.engine.params_b = params_b;
            c.bench = None;
            config::save(&c)?;
            println!("Engine saved. Run `infera bench`, then `infera run`.");
            Ok(())
        }
        Cmd::Bench => {
            let mut c = config::load()?;
            let mut e = engine::Engine::start(&c.engine).await?;
            println!("Benchmarking {} … (about a minute)", c.engine.model_id);
            let b = engine::benchmark(&e, &c.engine).await;
            e.stop().await;
            let b = b?;
            print_bench(&b);
            c.bench = Some(b);
            config::save(&c)?;
            Ok(())
        }
        Cmd::Run { ask } => run(ask).await,
        Cmd::Service { action } => service::run(&action),
        Cmd::Tray { .. } => unreachable!("handled before the runtime starts"),
        Cmd::Status => {
            let c = config::load()?;
            println!("Server:   {}\nDevice:   {} ({})\nModel:    {} ({} B params)\nMode:     {}", c.server, c.device_id, c.name, c.engine.model_id, c.engine.params_b, c.mode);
            match &c.bench {
                Some(b) => print_bench(b),
                None => println!("Not benchmarked yet – run `infera bench`."),
            }
            Ok(())
        }
    }
}

async fn pair(code: String, server: String, name: String) -> Result<()> {
    let key = config::identity()?;
    let name = if name.is_empty() { hostname() } else { name };
    let platform = format!("{} {}", std::env::consts::OS, std::env::consts::ARCH);
    let r = reqwest::Client::new()
        .post(format!("{}/api/devices/pair", server.trim_end_matches('/')))
        .json(&serde_json::json!({"code": code, "name": name, "pubkey": B64.encode(key.verifying_key().to_bytes()), "platform": platform}))
        .send()
        .await?;
    let status = r.status();
    let v: serde_json::Value = r.json().await?;
    if !status.is_success() {
        bail!("{}", v["error"]["message"].as_str().unwrap_or("pairing failed"));
    }
    let mut c = config::load().unwrap_or_default();
    c.server = server.trim_end_matches('/').to_string();
    c.device_id = v["device_id"].as_str().unwrap_or_default().into();
    c.token = v["token"].as_str().unwrap_or_default().into();
    c.name = name;
    if c.mode.is_empty() {
        c.mode = "auto".into();
    }
    config::save(&c)?;
    println!("Paired as {}. Next: `infera engine …` to pick a model, `infera bench`, then `infera run`.", c.device_id);
    Ok(())
}

async fn run(ask: bool) -> Result<()> {
    run_with(ask, new_state()).await
}

pub fn new_state() -> node::State {
    Arc::new(Mutex::new(node::Shared { online: true, status: "starting".into(), ..Default::default() }))
}

/// Starts the engine, benchmarks if needed, then serves jobs until quit. `state` is shared with the tray.
pub async fn run_with(ask: bool, state: node::State) -> Result<()> {
    let mut c = config::load()?;
    if c.device_id.is_empty() {
        bail!("this device is not paired – run `infera pair <code> --server <url>`");
    }
    if ask {
        c.mode = "ask".into();
    }
    println!("Starting engine for {} …", c.engine.model_id);
    let engine = engine::Engine::start(&c.engine).await?;
    if c.bench.as_ref().map(|b| b.model_id != c.engine.model_id).unwrap_or(true) {
        println!("Benchmarking this device …");
        let b = engine::benchmark(&engine, &c.engine).await?;
        print_bench(&b);
        c.bench = Some(b);
        config::save(&c)?;
    }
    if c.bench.as_ref().map(|b| b.class == "unrated").unwrap_or(true) {
        println!("This device is below the minimum for paid jobs with this model. Try a smaller model with `infera engine`.");
    }
    let key = config::identity()?;
    {
        let mut s = state.lock().unwrap();
        s.mode = c.mode.clone();
        s.status = "paused".into();
        s.model_id = c.engine.model_id.clone();
        s.bench = serde_json::to_value(&c.bench).unwrap_or_default();
        s.ui_port = c.governor.ui_port;
    }
    tokio::spawn(ui::serve(state.clone(), c.governor.ui_port));
    node::run(c, engine, key, state).await
}

fn print_bench(b: &engine::BenchResult) {
    println!(
        "CPU:      {} ({} cores), {:.1} GB RAM\nGPU:      {} ({:.1} GB)\nSpeed:    {:.1} tokens/s on {} ({} B), first token {} ms\nClass:    {} – {}",
        b.cpu, b.cores, b.ram_gb, b.gpu, b.vram_gb, b.tokens_per_s, b.model_id, b.params_b, b.first_token_ms, b.class, b.reason
    );
}

fn hostname() -> String {
    sysinfo::System::host_name().unwrap_or_else(|| "My device".into())
}
