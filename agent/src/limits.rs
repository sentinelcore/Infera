//! Extra idle rules (ARCHITECTURE.md §3): GPU busy or hot, memory pressure, fullscreen games,
//! owner pauses and allowed hours. Each check returns None when it can't tell, so a missing
//! tool (no nvidia-smi, unsupported OS) never blocks jobs by itself.

use chrono::Timelike;
use std::process::Command;

/// (utilization %, temperature °C) of the first NVIDIA GPU, via nvidia-smi.
pub fn nvidia() -> Option<(f32, f32)> {
    let o = Command::new("nvidia-smi")
        .args(["--query-gpu=utilization.gpu,temperature.gpu", "--format=csv,noheader,nounits"])
        .output()
        .ok()?;
    if !o.status.success() {
        return None;
    }
    let s = String::from_utf8_lossy(&o.stdout);
    let mut it = s.lines().next()?.split(',');
    let util = it.next()?.trim().parse().ok()?;
    let temp = it.next()?.trim().parse().ok()?;
    Some((util, temp))
}

/// True while a fullscreen game or presentation is running (Windows only for now).
#[cfg(windows)]
pub fn fullscreen_busy() -> Option<bool> {
    use windows_sys::Win32::UI::Shell::{SHQueryUserNotificationState, QUNS_BUSY, QUNS_PRESENTATION_MODE, QUNS_RUNNING_D3D_FULL_SCREEN};
    let mut state = 0;
    // SAFETY: state is a valid out pointer.
    if unsafe { SHQueryUserNotificationState(&mut state) } != 0 {
        return None;
    }
    Some(state == QUNS_RUNNING_D3D_FULL_SCREEN || state == QUNS_PRESENTATION_MODE || state == QUNS_BUSY)
}

#[cfg(not(windows))]
pub fn fullscreen_busy() -> Option<bool> {
    None
}

/// "22-7" style window in local time; empty = always allowed. Wraps past midnight.
pub fn within_hours(spec: &str) -> bool {
    let spec = spec.trim();
    if spec.is_empty() {
        return true;
    }
    let Some((a, b)) = spec.split_once('-') else { return true };
    let (Ok(a), Ok(b)) = (a.trim().parse::<u32>(), b.trim().parse::<u32>()) else { return true };
    let h = chrono::Local::now().hour();
    if a == b {
        true
    } else if a < b {
        h >= a && h < b
    } else {
        h >= a || h < b
    }
}

/// Unix time of the next local midnight, for "pause until tomorrow".
pub fn next_midnight() -> u64 {
    let now = chrono::Local::now();
    let secs_today = now.num_seconds_from_midnight() as u64;
    (now.timestamp() as u64) + (86_400 - secs_today)
}

#[cfg(test)]
mod tests {
    #[test]
    fn hours() {
        assert!(super::within_hours(""));
        assert!(super::within_hours("0-0"));
        assert!(super::within_hours("garbage"));
    }
}
