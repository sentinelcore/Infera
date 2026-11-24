# Infera

A network where laptops, desktops and phones serve AI inference on idle capacity. Contributors install once and earn; developers call one OpenAI-compatible API.

## Beta build

| Part | Path | Run |
|---|---|---|
| Control plane (API, scheduler, ledger, dashboard) | `control-plane/` | `go build -o infera-server . && ./infera-server` or `deploy/docker-compose.yml` |
| Node for laptops and desktops | `agent/` | `cargo build --release` → `infera pair`, `infera engine`, `infera run` |
| Installers | `install.sh`, `install.ps1` | download the node from GitHub releases (built by `.github/workflows/release.yml` on a `v*` tag) |

Setup, tester instructions and known limits: [TESTING.md](TESTING.md).

## Docs

- [Architecture](docs/ARCHITECTURE.md): system design, install-and-forget rules, protocol, payments
- [Execution plan](docs/EXECUTION_PLAN.md): phases, revenue tracks during the build, team, metrics, week-1 checklist
- [Beta launch on X](docs/BETA_LAUNCH_X.md): waves, launch thread, content calendar, growth loops
- [On-device inference research](docs/ON_DEVICE_INFERENCE.md): engines, smolmaxxing, models to support
- [Visual blueprint](docs/blueprint.html): the whole plan as one page with diagrams

`third_party/smolmaxxing` is a git submodule. Clone with `git clone --recurse-submodules`.
