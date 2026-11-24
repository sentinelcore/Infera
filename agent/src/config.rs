use anyhow::{Context, Result};
use ed25519_dalek::SigningKey;
use serde::{Deserialize, Serialize};
use std::path::PathBuf;

#[derive(Serialize, Deserialize, Clone, Debug, Default)]
pub struct Config {
    pub server: String,
    #[serde(default)]
    pub device_id: String,
    #[serde(default)]
    pub token: String,
    #[serde(default)]
    pub name: String,
    /// "auto" (default) or "ask" (offer card for every job)
    #[serde(default = "default_mode")]
    pub mode: String,
    #[serde(default)]
    pub engine: EngineConfig,
    #[serde(default)]
    pub governor: GovernorConfig,
    #[serde(default)]
    pub limits: Limits,
    #[serde(default)]
    pub bench: Option<crate::engine::BenchResult>,
}

fn default_mode() -> String {
    "auto".into()
}

#[derive(Serialize, Deserialize, Clone, Debug, Default)]
pub struct EngineConfig {
    /// OpenAI-compatible endpoint already running on this machine (llama-server, Ollama, LM Studio...).
    #[serde(default)]
    pub url: String,
    /// Or: path to a llama.cpp `llama-server` binary that Infera starts and stops itself.
    #[serde(default)]
    pub llama_server: String,
    #[serde(default)]
    pub gguf: String,
    /// Name customers use for this model, e.g. "qwen3.5-2b".
    #[serde(default)]
    pub model_id: String,
    /// Model name to send to the engine (defaults to model_id).
    #[serde(default)]
    pub engine_model: String,
    /// Optional embedding model served from the same engine (e.g. Ollama "nomic-embed-text").
    #[serde(default)]
    pub embed_model: String,
    #[serde(default)]
    pub embed_engine_model: String,
    /// Size in billions of parameters, used for the device class.
    #[serde(default)]
    pub params_b: f64,
    #[serde(default)]
    pub ctx: u32,
    #[serde(default)]
    pub gpu_layers: i32,
}

#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct GovernorConfig {
    /// Take new jobs only while other programs use less than this much CPU (percent).
    pub max_other_cpu: f32,
    /// Laptops: take new jobs only on mains power.
    pub require_ac: bool,
    pub ui_port: u16,
    /// Take new jobs only after this many minutes without keyboard or mouse input (0 = ignore input).
    #[serde(default = "default_idle_minutes")]
    pub idle_minutes: f64,
    /// Pause new jobs when the GPU is hotter than this (°C; NVIDIA via nvidia-smi).
    #[serde(default = "default_gpu_temp")]
    pub max_gpu_temp: f32,
    /// Pause new jobs when other programs (games, video) use more of the GPU than this (%).
    #[serde(default = "default_gpu_busy")]
    pub max_other_gpu: f32,
    /// Pause new jobs when free memory is below this share of total RAM (%).
    #[serde(default = "default_free_ram")]
    pub min_free_ram: f32,
    /// Only take jobs in these local hours, e.g. "22-7" (overnight). Empty = any time.
    #[serde(default)]
    pub allowed_hours: String,
}

fn default_gpu_temp() -> f32 {
    80.0
}
fn default_gpu_busy() -> f32 {
    20.0
}
fn default_free_ram() -> f32 {
    15.0
}

fn default_idle_minutes() -> f64 {
    5.0
}

impl Default for GovernorConfig {
    fn default() -> Self {
        Self {
            max_other_cpu: 35.0,
            require_ac: true,
            ui_port: 7777,
            idle_minutes: 5.0,
            max_gpu_temp: default_gpu_temp(),
            max_other_gpu: default_gpu_busy(),
            min_free_ram: default_free_ram(),
            allowed_hours: String::new(),
        }
    }
}

#[derive(Serialize, Deserialize, Clone, Debug, Default)]
pub struct Limits {
    /// Auto-approve only jobs paying at least this much (USD); others become offer cards. 0 = no limit.
    #[serde(default)]
    pub min_pay_usd: f64,
}

pub fn dir() -> PathBuf {
    if let Ok(d) = std::env::var("INFERA_HOME") {
        return PathBuf::from(d);
    }
    dirs::home_dir().unwrap_or_else(|| PathBuf::from(".")).join(".infera")
}

pub fn load() -> Result<Config> {
    let p = dir().join("config.toml");
    let s = std::fs::read_to_string(&p).with_context(|| format!("no config at {} – run `infera pair <code> --server <url>` first", p.display()))?;
    Ok(toml::from_str(&s)?)
}

pub fn save(c: &Config) -> Result<()> {
    std::fs::create_dir_all(dir())?;
    let p = dir().join("config.toml");
    std::fs::write(&p, toml::to_string_pretty(c)?)?;
    restrict(&p);
    Ok(())
}

/// Device identity: an Ed25519 key that signs every job receipt.
pub fn identity() -> Result<SigningKey> {
    let p = dir().join("identity.key");
    if let Ok(h) = std::fs::read_to_string(&p) {
        let b = hex::decode(h.trim())?;
        let arr: [u8; 32] = b.try_into().map_err(|_| anyhow::anyhow!("bad identity key"))?;
        return Ok(SigningKey::from_bytes(&arr));
    }
    std::fs::create_dir_all(dir())?;
    let k = SigningKey::generate(&mut rand::rngs::OsRng);
    std::fs::write(&p, hex::encode(k.to_bytes()))?;
    restrict(&p);
    Ok(k)
}

#[cfg(unix)]
fn restrict(p: &std::path::Path) {
    use std::os::unix::fs::PermissionsExt;
    let _ = std::fs::set_permissions(p, std::fs::Permissions::from_mode(0o600));
}
#[cfg(not(unix))]
fn restrict(_p: &std::path::Path) {}
