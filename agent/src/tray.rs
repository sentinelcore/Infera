//! Desktop app mode: a menu-bar (macOS) / system-tray (Windows, Linux) icon around the node.
//! The tray owns the main thread; the node runs on a tokio runtime in a background thread.

use crate::node::push_state;
use anyhow::Result;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};
use tao::event::{Event, StartCause};
use tao::event_loop::{ControlFlow, EventLoopBuilder};
use tray_icon::menu::{CheckMenuItem, Menu, MenuEvent, MenuItem, PredefinedMenuItem};
use tray_icon::{Icon, TrayIconBuilder};

#[derive(Clone, Copy, PartialEq)]
enum Look {
    Online,
    Working,
    Paused,
    Problem,
}

fn icon(look: Look) -> Icon {
    // A 32×32 filled circle; the colour shows the state.
    let (r, g, b) = match look {
        Look::Online => (0x17, 0x9a, 0x6e),
        Look::Working => (0xd8, 0x96, 0x1a),
        Look::Paused => (0x8a, 0x96, 0x90),
        Look::Problem => (0xc8, 0x4b, 0x36),
    };
    let n = 32u32;
    let mut px = Vec::with_capacity((n * n * 4) as usize);
    for y in 0..n {
        for x in 0..n {
            let dx = x as f32 - 15.5;
            let dy = y as f32 - 15.5;
            let d = (dx * dx + dy * dy).sqrt();
            let a = ((13.5 - d).clamp(0.0, 1.0) * 255.0) as u8;
            px.extend_from_slice(&[r, g, b, a]);
        }
    }
    Icon::from_rgba(px, n, n).expect("icon")
}

fn usd(micro: i64) -> String {
    let v = micro as f64 / 1e6;
    if micro != 0 && micro.abs() < 10_000 {
        format!("${v:.6}")
    } else {
        format!("${v:.2}")
    }
}

pub fn main(ask: bool) -> Result<()> {
    // When started at login on Windows, drop the console window that would otherwise sit next to the tray icon.
    #[cfg(windows)]
    unsafe {
        windows_sys::Win32::System::Console::FreeConsole();
    }
    let state = crate::new_state();
    let done = Arc::new(AtomicBool::new(false));
    let failed: Arc<std::sync::Mutex<Option<String>>> = Arc::new(std::sync::Mutex::new(None));
    {
        let (state, done, failed) = (state.clone(), done.clone(), failed.clone());
        std::thread::spawn(move || {
            let rt = tokio::runtime::Runtime::new().expect("tokio");
            if let Err(e) = rt.block_on(crate::run_with(ask, state.clone())) {
                *failed.lock().unwrap() = Some(e.to_string());
                state.lock().unwrap().log(format!("Infera stopped: {e}"));
            }
            done.store(true, Ordering::SeqCst);
        });
    }

    let event_loop = EventLoopBuilder::new().build();
    let status = MenuItem::new("Starting…", false, None);
    let today = MenuItem::new("Today: –", false, None);
    let toggle = MenuItem::new("Go offline", true, None);
    let ask_item = CheckMenuItem::new("Ask before every job", true, ask, None);
    let open_item = MenuItem::new("Open Infera…", true, None);
    let finish = MenuItem::new("Finish current job, then go offline", false, None);
    let pause1 = MenuItem::new("Pause new jobs for 1 hour", true, None);
    let pause_tom = MenuItem::new("Pause until tomorrow", true, None);
    let resume = MenuItem::new("Resume", false, None);
    let quit = MenuItem::new("Quit Infera", true, None);
    let menu = Menu::new();
    menu.append_items(&[&status, &today, &PredefinedMenuItem::separator(), &toggle, &ask_item, &pause1, &pause_tom, &resume, &finish, &open_item, &PredefinedMenuItem::separator(), &quit])?;

    let mut tray = None;
    let mut look = Look::Paused;
    let mut last_offer = String::new();
    let mut last_open = Instant::now() - Duration::from_secs(600);
    let menu_rx = MenuEvent::receiver();

    event_loop.run(move |event, _, control_flow| {
        *control_flow = ControlFlow::WaitUntil(Instant::now() + Duration::from_millis(700));
        if let Event::NewEvents(StartCause::Init) = event {
            // Create the icon once the event loop is running (needed on macOS and Linux).
            tray = TrayIconBuilder::new().with_menu(Box::new(menu.clone())).with_tooltip("Infera").with_icon(icon(look)).build().ok();
        }

        while let Ok(ev) = menu_rx.try_recv() {
            let page = format!("http://127.0.0.1:{}", state.lock().unwrap().ui_port.max(1));
            if ev.id == toggle.id() {
                {
                    let mut s = state.lock().unwrap();
                    s.online = !s.online;
                    if s.job.is_none() {
                        s.status = if s.online && s.ready { "idle".into() } else { "paused".into() };
                    }
                    let m = if s.online { "You are online" } else { "You are offline" };
                    s.log(m);
                }
                push_state(&state);
            } else if ev.id == ask_item.id() {
                {
                    let mut s = state.lock().unwrap();
                    s.mode = if ask_item.is_checked() { "ask".into() } else { "auto".into() };
                }
                push_state(&state);
            } else if ev.id == finish.id() {
                let mut s = state.lock().unwrap();
                s.draining = true;
                s.log("Finishing the current job, then going offline");
            } else if ev.id == pause1.id() || ev.id == pause_tom.id() || ev.id == resume.id() {
                let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs();
                {
                    let mut s = state.lock().unwrap();
                    s.paused_until = if ev.id == pause1.id() {
                        now + 3600
                    } else if ev.id == pause_tom.id() {
                        crate::limits::next_midnight()
                    } else {
                        0
                    };
                    if s.job.is_none() && s.paused_until > 0 {
                        s.status = "paused".into();
                    }
                    let m = if s.paused_until > 0 { "Paused for new jobs" } else { "Resumed" };
                    s.log(m);
                }
                push_state(&state);
            } else if ev.id == open_item.id() {
                let _ = open::that(&page);
            } else if ev.id == quit.id() {
                let mut s = state.lock().unwrap();
                s.quit_requested = true;
                if s.job.is_some() {
                    s.log("Quitting after the current job finishes");
                }
            }
        }

        // Refresh the menu from the node state.
        let (text, today_text, new_look, online, has_job, offer, mode, port) = {
            let s = state.lock().unwrap();
            let failed = failed.lock().unwrap().clone();
            let (text, lk) = if let Some(e) = failed {
                (format!("Problem: {}", e.chars().take(60).collect::<String>()), Look::Problem)
            } else if let Some(j) = &s.job {
                (format!("Working · earns ~${:.4} · {}s", j.est_pay_usd, j.running_s), Look::Working)
            } else if !s.connected && s.status != "starting" {
                ("Connecting…".to_string(), Look::Paused)
            } else if s.status == "idle" {
                ("● Online · waiting for jobs".to_string(), Look::Online)
            } else if s.status == "starting" {
                ("Starting the engine…".to_string(), Look::Paused)
            } else {
                (format!("Paused · {}", if s.pause_reason.is_empty() { "not ready" } else { &s.pause_reason }), Look::Paused)
            };
            let r = &s.remote;
            let today_text = match (r["today"]["earned_today"].as_i64(), r["today"]["jobs_today"].as_i64()) {
                (Some(e), Some(n)) => format!("Today: {} · {} jobs · dependability {:.0}", usd(e), n, s.hub["dependability"].as_f64().unwrap_or(0.0)),
                _ => "Today: –".to_string(),
            };
            (text, today_text, lk, s.online, s.job.is_some(), s.offer.clone(), s.mode.clone(), s.ui_port)
        };
        status.set_text(&text);
        today.set_text(&today_text);
        toggle.set_text(if online { "Go offline" } else { "Go online" });
        finish.set_enabled(has_job);
        let paused = state.lock().unwrap().paused_until > std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs();
        resume.set_enabled(paused);
        ask_item.set_checked(mode == "ask");
        if new_look != look {
            look = new_look;
            if let Some(t) = &tray {
                let _ = t.set_icon(Some(icon(look)));
            }
        }

        // New offer card: notify, and open the page so the owner can accept (like a ride request).
        if let Some(o) = offer {
            if o.job_id != last_offer {
                last_offer = o.job_id.clone();
                let _ = notify_rust::Notification::new()
                    .summary(&format!("New job · ${:.4}", o.est_pay_usd))
                    .body(&format!("{} · about {:.0} s · {}s to accept", o.job_type.replace('_', " "), (o.est_minutes * 60.0).max(1.0), o.seconds_left))
                    .show();
                if last_open.elapsed() > Duration::from_secs(60) {
                    last_open = Instant::now();
                    let _ = open::that(format!("http://127.0.0.1:{port}"));
                }
            }
        }

        if done.load(Ordering::SeqCst) && failed.lock().unwrap().is_none() {
            tray.take();
            crate::updater::finish();
            *control_flow = ControlFlow::Exit;
        }
    });
}
