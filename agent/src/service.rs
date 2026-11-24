//! `infera service install|uninstall|status`: start the node at login so it's install-and-forget.
//! Linux: systemd user unit. macOS: launchd LaunchAgent. Windows: a logon task in Task Scheduler.

use anyhow::{bail, Context, Result};
use std::process::Command;

const LABEL: &str = "network.infera.node";

pub fn run(action: &str) -> Result<()> {
    let exe = std::env::current_exe().context("can't find the infera binary")?;
    let exe = exe.to_string_lossy().to_string();
    crate::config::load().context("pair this device first")?;
    match action {
        "install" => install(&exe),
        "uninstall" => uninstall(),
        "status" => status(),
        _ => bail!("use: infera service install | uninstall | status"),
    }
}

fn sh(cmd: &str, args: &[&str]) -> Result<String> {
    let o = Command::new(cmd).args(args).output().with_context(|| format!("couldn't run {cmd}"))?;
    let out = format!("{}{}", String::from_utf8_lossy(&o.stdout), String::from_utf8_lossy(&o.stderr));
    if !o.status.success() {
        bail!("{cmd} {}: {}", args.join(" "), out.trim());
    }
    Ok(out)
}

#[cfg(target_os = "linux")]
fn unit_path() -> std::path::PathBuf {
    dirs::config_dir().unwrap_or_else(|| dirs::home_dir().unwrap().join(".config")).join("systemd/user/infera.service")
}

#[cfg(target_os = "linux")]
fn install(exe: &str) -> Result<()> {
    let home = crate::config::dir();
    let unit = format!(
        "[Unit]\nDescription=Infera node\nAfter=network-online.target\n\n[Service]\nExecStart={exe} run\nEnvironment=INFERA_HOME={}\nRestart=on-failure\nRestartSec=10\nTimeoutStopSec=40\n\n[Install]\nWantedBy=default.target\n",
        home.display()
    );
    let p = unit_path();
    std::fs::create_dir_all(p.parent().unwrap())?;
    std::fs::write(&p, unit)?;
    if let Err(e) = sh("systemctl", &["--user", "daemon-reload"]) {
        let _ = std::fs::remove_file(&p);
        bail!("this system has no systemd user session ({e}). Add `{exe} run` to your desktop's startup applications instead.");
    }
    sh("systemctl", &["--user", "enable", "--now", "infera.service"])?;
    println!("Infera now starts when you log in (systemd user service).");
    println!("To keep it running while you're logged out: loginctl enable-linger $USER");
    println!("Logs: journalctl --user -u infera -f");
    Ok(())
}

#[cfg(target_os = "linux")]
fn uninstall() -> Result<()> {
    let _ = sh("systemctl", &["--user", "disable", "--now", "infera.service"]);
    let _ = std::fs::remove_file(unit_path());
    let _ = sh("systemctl", &["--user", "daemon-reload"]);
    println!("Infera no longer starts at login.");
    Ok(())
}

#[cfg(target_os = "linux")]
fn status() -> Result<()> {
    let o = Command::new("systemctl").args(["--user", "status", "infera.service", "--no-pager"]).output()?;
    println!("{}", String::from_utf8_lossy(&o.stdout));
    Ok(())
}

#[cfg(target_os = "macos")]
fn plist_path() -> std::path::PathBuf {
    dirs::home_dir().unwrap().join(format!("Library/LaunchAgents/{LABEL}.plist"))
}

#[cfg(target_os = "macos")]
fn install(exe: &str) -> Result<()> {
    let home = crate::config::dir();
    let log = home.join("node.log");
    let plist = format!(
        r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>{LABEL}</string>
  <key>ProgramArguments</key><array><string>{exe}</string><string>run</string></array>
  <key>EnvironmentVariables</key><dict><key>INFERA_HOME</key><string>{}</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ExitTimeOut</key><integer>40</integer>
  <key>StandardOutPath</key><string>{}</string>
  <key>StandardErrorPath</key><string>{}</string>
</dict></plist>
"#,
        home.display(),
        log.display(),
        log.display()
    );
    let p = plist_path();
    std::fs::create_dir_all(p.parent().unwrap())?;
    let _ = sh("launchctl", &["unload", &p.to_string_lossy()]);
    std::fs::write(&p, plist)?;
    sh("launchctl", &["load", "-w", &p.to_string_lossy()])?;
    println!("Infera now starts when you log in (LaunchAgent). Logs: {}", log.display());
    Ok(())
}

#[cfg(target_os = "macos")]
fn uninstall() -> Result<()> {
    let p = plist_path();
    let _ = sh("launchctl", &["unload", "-w", &p.to_string_lossy()]);
    let _ = std::fs::remove_file(&p);
    println!("Infera no longer starts at login.");
    Ok(())
}

#[cfg(target_os = "macos")]
fn status() -> Result<()> {
    match sh("launchctl", &["list", LABEL]) {
        Ok(o) => println!("Installed and loaded:\n{o}"),
        Err(_) => println!("Not installed. Run `infera service install`."),
    }
    Ok(())
}

#[cfg(target_os = "windows")]
fn install(exe: &str) -> Result<()> {
    let tr = format!("\"{exe}\" run");
    sh("schtasks", &["/Create", "/TN", "Infera Node", "/TR", &tr, "/SC", "ONLOGON", "/RL", "LIMITED", "/F"])?;
    let _ = sh("schtasks", &["/Run", "/TN", "Infera Node"]);
    println!("Infera now starts when you log in (Task Scheduler: \"Infera Node\").");
    Ok(())
}

#[cfg(target_os = "windows")]
fn uninstall() -> Result<()> {
    let _ = sh("schtasks", &["/End", "/TN", "Infera Node"]);
    let _ = sh("schtasks", &["/Delete", "/TN", "Infera Node", "/F"]);
    println!("Infera no longer starts at login.");
    Ok(())
}

#[cfg(target_os = "windows")]
fn status() -> Result<()> {
    match sh("schtasks", &["/Query", "/TN", "Infera Node", "/V", "/FO", "LIST"]) {
        Ok(o) => println!("{o}"),
        Err(_) => println!("Not installed. Run `infera service install`."),
    }
    Ok(())
}

#[cfg(not(any(target_os = "linux", target_os = "macos", target_os = "windows")))]
fn install(_exe: &str) -> Result<()> {
    bail!("automatic start isn't supported on this OS yet")
}
#[cfg(not(any(target_os = "linux", target_os = "macos", target_os = "windows")))]
fn uninstall() -> Result<()> {
    Ok(())
}
#[cfg(not(any(target_os = "linux", target_os = "macos", target_os = "windows")))]
fn status() -> Result<()> {
    Ok(())
}

#[allow(dead_code)]
fn _label() -> &'static str {
    LABEL
}
