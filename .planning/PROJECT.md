# Infera

## What it is
A network where laptops, desktops and (later) phones serve AI inference on idle capacity. Contributors install once and earn USDT; developers call one OpenAI-compatible API.

## Who it's for
- **Contributors:** people with idle laptops, gaming PCs, Macs and phones who want passive income with no setup.
- **Developers:** teams needing cheap batch, embedding, transcription and chat inference.

## Core value
Install and forget: the node earns only when the device is idle, never gets in the owner's way, and pays reliably.

## Principles
- Declining is free; accepted jobs must finish (dependability and penalties).
- Customers' data is never visible to owners (sealed jobs → attested sandbox).
- Revenue from day one: rented-capacity fallback until community supply carries the load.
- Every number on a receipt is signed and verifiable.

## Stack
- Control plane: Go, SQLite (→ Postgres), WebSocket hub, embedded dashboard (`control-plane/`)
- Node: Rust single binary, local page on :7777 (`agent/`)
- Engines: any OpenAI-compatible local server; managed llama.cpp; smolmaxxing for phones (`third_party/`)
- Payouts: weekly USDT batches (Tron TRC-20 default)

## Reference docs
`docs/ARCHITECTURE.md` · `docs/EXECUTION_PLAN.md` · `docs/ON_DEVICE_INFERENCE.md` · `docs/BETA_LAUNCH_X.md` · `TESTING.md`
