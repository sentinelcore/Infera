//! Silent auto-update (ARCHITECTURE.md §2.1 `updater`). Every 6 hours, while no job is running,
//! the node reads the test-build manifest; if a newer build exists for this platform it downloads
//! the binary, checks its SHA-256 against the manifest, swaps it in and restarts.
//! Dev builds (no INFERA_BUILD at compile time) never update themselves.
//! Next step (GAPS G8): an Ed25519-signed manifest instead of a checksum from the same host.

use crate::node::State;
use anyhow::{bail, Context, Result};
use sha2::{Digest, Sha256};
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

pub static RESTART: AtomicBool = AtomicBool::new(false);

pub fn build() -> u64 {
    option_env!("INFERA_BUILD").and_then(|s| s.parse().ok()).unwrap_or(0)
}

const MANIFEST: &str = "https://github.com/sentinelcore/Infera/releases/download/test-build/infera-manifest.json";

fn asset_name() -> Option<&'static str> {
    match (std::env::consts::OS, std::env::consts::ARCH) {
        ("linux", "x86_64") => Some("infera-linux-x64"),
        ("windows", "x86_64") => Some("infera-windows-x64.exe"),
        _ => None,
    }
}

/// Runs forever; when an update is installed it sets RESTART and asks the node to quit after
/// the current job.
pub async fn run(state: State, enabled: bool) {
    let mine = build();
    let Some(asset) = asset_name() else { return };
    if !enabled || mine == 0 {
        return;
    }
    tokio::time::sleep(Duration::from_secs(120)).await;
    loop {
        let busy = state.lock().unwrap().job.is_some();
        if !busy {
            match check_and_install(mine, asset).await {
                Ok(Some(v)) => {
                    let mut s = state.lock().unwrap();
                    s.log(format!("Updated to build {v}; restarting after any running job"));
                    RESTART.store(true, Ordering::SeqCst);
                    s.quit_requested = true;
                    return;
                }
                Ok(None) => {}
                Err(e) => state.lock().unwrap().log(format!("Update check failed: {e}")),
            }
        }
        tokio::time::sleep(Duration::from_secs(6 * 3600)).await;
    }
}

async fn check_and_install(mine: u64, asset: &str) -> Result<Option<u64>> {
    let http = reqwest::Client::builder().timeout(Duration::from_secs(600)).build()?;
    let m: serde_json::Value = http.get(MANIFEST).send().await?.error_for_status()?.json().await?;
    let latest = m["build"].as_u64().unwrap_or(0);
    if latest <= mine {
        return Ok(None);
    }
    let want = m["assets"][asset]["sha256"].as_str().context("manifest has no checksum for this platform")?.to_string();
    let url = format!("https://github.com/sentinelcore/Infera/releases/download/test-build/{asset}");
    let bytes = http.get(url).send().await?.error_for_status()?.bytes().await?;
    let got = hex::encode(Sha256::digest(&bytes));
    if got != want {
        bail!("checksum mismatch; update skipped");
    }
    let exe = std::env::current_exe()?;
    let new = exe.with_extension("new");
    std::fs::write(&new, &bytes)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&new, std::fs::Permissions::from_mode(0o755))?;
        std::fs::rename(&new, &exe)?; // replacing a running binary is fine on Unix
    }
    #[cfg(windows)]
    {
        let old = exe.with_extension("old.exe");
        let _ = std::fs::remove_file(&old);
        std::fs::rename(&exe, &old)?; // a running .exe can be renamed, not overwritten
        std::fs::rename(&new, &exe)?;
    }
    Ok(Some(latest))
}

/// Called when the process is about to exit. If an update was installed: under a service manager
/// (INFERA_SERVICE=1) exit with 75 so it restarts us; otherwise start the new binary ourselves.
pub fn finish() {
    if !RESTART.load(Ordering::SeqCst) {
        return;
    }
    if std::env::var_os("INFERA_SERVICE").is_some() {
        std::process::exit(75);
    }
    if let Ok(exe) = std::env::current_exe() {
        let args: Vec<String> = std::env::args().skip(1).collect();
        let _ = std::process::Command::new(exe).args(args).spawn();
    }
    std::process::exit(0);
}
