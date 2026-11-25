# OpenRouter provider application (submitted 2026-11-25)

Entity: Greencircuit Innovations LLP, India · contact hello@valanor.network · security security@valanor.network (alias to create)

| Field | Answer |
|---|---|
| Provider slug | `infera` (fallbacks: `infera-network`, `valanor`) |
| Model types | Open-weight |
| Models | qwen/qwen3-8b, qwen/qwen3-14b, qwen/qwen3-4b, meta-llama/llama-3.1-8b-instruct, google/gemma-3-12b-it, google/gemma-3-4b-it, mistralai/mistral-nemo, deepseek/deepseek-r1-distill-llama-8b, deepseek/deepseek-r1-distill-qwen-14b |
| Output modalities | Text, Embeddings |
| What sets us apart | Low pricing, unique infrastructure, decentralized |
| Hardware ownership | Mix |
| GPU types | Other: consumer NVIDIA RTX, Apple Silicon, Snapdragon 8-series |
| Inference location | IN (add US when the DeepInfra backup is live; add others as devices join) |
| GPU count / tokens per day / RPM | 0 data-centre GPUs · 3M tokens/day · 20 requests/min (10 phones) |
| Base URL | https://infera.valanor.network/v1 |
| Models API | https://infera.valanor.network/v1/models |
| Benchmarks | https://infera.valanor.network/performance |
| ZDR | No |
| Data policy | https://infera.valanor.network/privacy |
| Privacy policy | https://infera.valanor.network/privacy-policy |
| Terms | https://infera.valanor.network/terms |
| SOC 2 | Not certified |
| Documents | Network architecture diagram (to produce) |

## Security questionnaire (10q-v2.2) answers
1. Regulatory standing: No.
2. Data locations: gateway + DB on Railway US-West (San Francisco), TLS at Railway edge; inference on community devices (currently India, can restrict by country); backup DeepInfra (US) when enabled; no CDN/logging/classifier vendors with content.
3. Training: No; can commit contractually with no exclusions.
4. ZDR: No.
5. Retention: live content never stored; billing metadata + output hash kept; batch content 7 days; logs method+path only; no debug sampling.
6. Keys: no KMS; Railway secrets; hashed API keys; ephemeral per-job X25519; device keys in OS key stores.
7. Incidents: 72 h; security@valanor.network.
8. Attestations: No. 9. Pen test: none yet. 10. HIPAA: No; signing entity Greencircuit Innovations LLP.

## To strengthen a later reapplication
- DeepInfra backup live (G31) · own edge data centres · independent pen test of the API · admin audit logging · security@ alias · SOC 2 when revenue allows.
