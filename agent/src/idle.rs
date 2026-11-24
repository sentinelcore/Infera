//! Time since the last keyboard or mouse input. None when the OS doesn't tell us
//! (e.g. Linux without X11/xprintidle); the governor then relies on CPU load alone.

use std::time::Duration;

#[cfg(target_os = "macos")]
pub fn input_idle() -> Option<Duration> {
    let o = std::process::Command::new("ioreg").args(["-c", "IOHIDSystem", "-d", "4"]).output().ok()?;
    let s = String::from_utf8_lossy(&o.stdout);
    let line = s.lines().find(|l| l.contains("\"HIDIdleTime\""))?;
    let ns: u64 = line.rsplit('=').next()?.trim().parse().ok()?;
    Some(Duration::from_nanos(ns))
}

#[cfg(target_os = "windows")]
pub fn input_idle() -> Option<Duration> {
    use windows_sys::Win32::System::SystemInformation::GetTickCount;
    use windows_sys::Win32::UI::Input::KeyboardAndMouse::{GetLastInputInfo, LASTINPUTINFO};
    let mut info = LASTINPUTINFO { cbSize: std::mem::size_of::<LASTINPUTINFO>() as u32, dwTime: 0 };
    // SAFETY: info is a valid, correctly sized LASTINPUTINFO.
    if unsafe { GetLastInputInfo(&mut info) } == 0 {
        return None;
    }
    let now = unsafe { GetTickCount() };
    Some(Duration::from_millis(now.wrapping_sub(info.dwTime) as u64))
}

#[cfg(target_os = "linux")]
pub fn input_idle() -> Option<Duration> {
    // Works on X11 desktops with xprintidle installed (apt install xprintidle).
    std::env::var_os("DISPLAY")?;
    let o = std::process::Command::new("xprintidle").output().ok()?;
    if !o.status.success() {
        return None;
    }
    let ms: u64 = String::from_utf8_lossy(&o.stdout).trim().parse().ok()?;
    Some(Duration::from_millis(ms))
}

#[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
pub fn input_idle() -> Option<Duration> {
    None
}
