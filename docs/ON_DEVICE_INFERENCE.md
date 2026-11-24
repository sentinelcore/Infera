# On-Device Inference: Engines, Techniques and Models

Research into the newest ways to run language models on phones, laptops and desktops, and how DePINClaw uses them. Researched November 2026. Most speed figures are self-reported by each project, so the benchmark step (`ARCHITECTURE.md` §3.1) re-measures everything on the contributor's own device before it is used.

## 1. smolmaxxing (added to this repo)

- **Repo:** https://github.com/hyperionaidev/smolmaxxing (MIT licence), added as a git submodule at `third_party/smolmaxxing`.
- **What it is:** a patch set on llama.cpp that runs the biggest model each small device can handle, with every number measured on the device itself.

**Measured results (from the repo)**

| Device | RAM | Everyday model | Biggest model |
|---|---|---|---|
| Samsung Galaxy S24 Ultra (Snapdragon 8 Gen 3) | 12 GB | Qwen3.6-35B-A3B at 12–16 tok/s | same 35B MoE (12.3 GB file) |
| OnePlus Nord CE 2 (Dimensity 900) | 7.4 GB, ~3 GB free | Qwen3.6-35B-A3B at 4.9–5.7 tok/s | Qwen3-Next-80B-A3B at 2.6–3.1 tok/s, ~5 GB peak |
| Infinix GT 20 Pro (Dimensity 8200) | 8 GB | Qwen3.6-35B-A3B at 3.1 tok/s | — |
| Realme 5 Pro (Snapdragon 710) | 6 GB | Qwen3-0.6B at 14.6 tok/s | Qwen3-1.7B at 5.7 tok/s |
| Xiaomi TV Box S (4× A55) | ~1 GB usable | Qwen3.5-2B at 7.7 tok/s | Ling-mini-2.0 16B at 5.1 tok/s |
| iPad Air 2013 (A7) | ~700 MB usable | Bonsai-1.7B (1-bit) at 24–26 tok/s | Ling-mini-2.0 16B at 2.6–4.5 tok/s |

**Techniques we can use**

| Technique | What it does | Use in DePINClaw |
|---|---|---|
| Expert pager | For mixture-of-experts (MoE) models, keeps popular experts in RAM and reads the rest from flash on demand, prefetching the next layer's | Lets 8 GB phones serve 35B MoE models for batch jobs; new benchmark step measures flash speed |
| Cache-aware routing | Nudges the router toward experts already in RAM (+0.5–2% perplexity for +15–20% speed on the phone) | Off for verified jobs by default; allowed only on job types whose customer accepts it, since it changes outputs and affects result checks |
| Dense-model streaming | Streams weights bigger than RAM from flash at full flash speed | Batch-only, for laptops with fast SSDs and little RAM |
| 1-bit and ternary kernels | Lookup-table kernels for 1-bit (Bonsai) and ternary models on plain ARM NEON | Phones and old devices in classes M1/M2 |
| Pruned vocabulary head | Drops rarely used output rows to save RAM and time | Small models only; their notes show it breaks big models (a 35B misspelled words) |
| big.LITTLE thread placement | Runs on big cores only; adding little cores slowed generation | Governor pins threads to performance cores |
| Repack cache on flash | Repacked weights saved once, so models load in seconds | Faster job start after model swap |

**Lessons from their notes that change our plan**
- On small devices, a 2B model at 4 bits beat bigger models squeezed to 1–2 bits. Models trained for 1-bit (Bonsai) are the exception.
- Perplexity alone misses repetition loops, so verification should include long-generation checks.
- Once MoE experts are cached, RAM bandwidth sets the speed. The benchmark must measure memory bandwidth, not just CPU.

## 2. Inference engines

| Engine | Best on | Why use it | Role in DePINClaw |
|---|---|---|---|
| **llama.cpp** | Everything (CPU, CUDA, Vulkan, Metal) | Portable baseline, GGUF models | Default engine on every platform |
| **llama.cpp + smolmaxxing patch** | ARM phones, TV boxes, low-RAM devices | Expert pager, flash streaming, 1-bit kernels | Android engine for MoE-from-flash and 1-bit models |
| **MLX / MLX Swift** | Apple Silicon Macs, iPhones | Sources consistently name it the fastest option on Apple Silicon | Mac and iPhone engine (replaces llama.cpp there once tested) |
| **MNN-LLM** (Alibaba) | Android CPU and GPU | Reported 8.6× (CPU) and 25.3× (GPU) faster prompt processing than llama.cpp on a Xiaomi 14 (vendor's own test) | Android candidate for long-prompt jobs (embeddings, transcription prompts) |
| **LiteRT-LM** (Google) | Android NPUs: Google Tensor, Qualcomm, MediaTek, Samsung Exynos, Intel | Official NPU path for Gemma models | Android NPU engine for supported phones |
| **Qualcomm Genie / QNN** | Snapdragon NPUs | Vendor-optimised NPU path | Snapdragon flagship phones and Windows on Arm laptops |
| **ExecuTorch** (Meta) | Phones to microcontrollers, many NPU backends | 1.0 since October 2025, small runtime | Fallback NPU path for PyTorch-exported models |
| **bitnet.cpp / T-MAC** (Microsoft) | CPUs running 1–2-bit and ternary models | Lookup-table kernels; T-MAC reports up to 4× throughput and 70% less energy than llama.cpp on low-bit models | CPU-only laptops and older phones |
| **PowerInfer-2** | Phones | Uses sparse activation; reports 11.68 tok/s on a 47B sparse model on a phone | Watch list: needs specially trained sparse models |
| **vLLM / SGLang** | Linux PCs with large NVIDIA GPUs | High batch throughput | Class A Linux desktops serving batch chat |
| **AirLLM** | PCs with small GPUs | Runs 70B on a 4 GB GPU by loading one layer at a time; reviewers say it is possible, not fast | Batch-only option for Class C PCs with spare disk; never real-time |
| **exo** | Several home devices together | Splits one model across devices in a home network | Later: "home cluster" contributors pooling their own devices |

Apple's Foundation Models framework runs Apple's own model for the user's apps. It is not a way to serve customer jobs, so it is out of scope.

## 3. How the agent uses several engines

- The `engine` module becomes a set of **engine adapters**, one per engine above, behind one interface: load model, run, stream tokens, unload.
- During the install benchmark, each available engine is tested on the device, and the fastest one that passes the correctness check is chosen for each model.
- Every engine runs inside the secure sandbox, with signed binaries and signed models only.
- Result checks compare outputs per engine. A job is re-run on the same engine and model type, so engine differences don't count as wrong answers.

## 4. Models to support (open weights)

| Model | Size | Notes from research | Suits |
|---|---|---|---|
| Qwen3.5-0.8B / 2B | 0.8B, 2B | Phone and CPU picks; 0.8B under 1 GB at 4-bit | M1, M2, C |
| Qwen3-0.6B / 1.7B | 0.6B, 1.7B | Fast on older phones | M2 |
| Bonsai 1.7B / 4B / 8B (PrismML, 1-bit) | 8B in about 1.15 GB | Apache 2.0, released March 2026; quality claims are mostly the company's own | M1, M2, C |
| Gemma 4 E2B | 2B effective | Reported 65 tok/s on iPhone 17 Pro, 19 tok/s on Galaxy S25 Ultra (crowd-sourced) | M1, iPhone |
| Ling-mini-2.0 | 16B MoE, 1.4B active | 11–15 tok/s on a mid-range phone with the expert pager | M1, C |
| Qwen3.6-35B-A3B | 35B MoE, 3B active | 4.9–16 tok/s on 8–12 GB phones with the expert pager | M1 (batch), B, A |
| Qwen3-Next-80B-A3B | 80B MoE, 3B active | 2.6–3.1 tok/s on a mid-range phone | Batch only, high-storage phones and PCs |
| Llama / Qwen / Mistral 7–32B dense | 7–32B | Standard desktop models | A, B |
| Embedding models (bge, nomic), Whisper | small | Unchanged | All |

Each model's licence must allow commercial serving before it goes into the catalogue.

## 5. Changes to the plan

1. Benchmark adds: flash read speed, free storage, RAM bandwidth, and a test run on every available engine.
2. Class M1 phones with fast flash and 15 GB+ free storage can opt in to large MoE models (35B) for batch jobs, which pay more per job than small-model work.
3. Phase 1: engine adapter interface plus llama.cpp. Phase 3: smolmaxxing patch, MNN-LLM and LiteRT-LM on Android. Phase 4: MLX on Mac and iPhone, vLLM for Class A Linux.
4. Verification adds long-generation loop checks.

## Sources

- [smolmaxxing](https://github.com/hyperionaidev/smolmaxxing)
- [On-Device LLM Inference: The Definitive 2025-2026 Guide (Octomil)](https://docs.octomil.com/blog/on-device-llm-inference-2025-2026/)
- [On-Device AI in 2026 (AI Magicx)](https://www.aimagicx.com/blog/on-device-ai-models-local-llm-guide-2026)
- [On-Device LLMs: State of the Union, 2026](https://v-chandra.github.io/on-device-llms/)
- [Choosing the best LLM inference engine in 2026 (Bizon)](https://bizon-tech.com/blog/best-llm-inference-engines)
- [Best Local LLMs in 2026 (askloci)](https://askloci.ai/guides/best-local-llms/)
- [Best Small Language Models 2026 (vdf.ai)](https://vdf.ai/blog/best-small-language-models/)
- [AirLLM on Hugging Face blog](https://huggingface.co/blog/lyogavin/airllm)
- [AirLLM's real trade-off (dev.to)](https://dev.to/umesh_malik/run-70b-llm-on-4gb-gpu-airllms-real-tradeoff-19oh)
- [AirLLM on PyPI](https://pypi.org/project/airllm/3.1.0/)
- [LiteRT-LM NPU guide (Google)](https://developers.google.com/edge/litert/next/litert_lm_npu)
- [On-Device AI on Android 2026 (dev.to)](https://dev.to/privatejack/on-device-ai-on-android-2026-litert-gemini-nano-and-edge-deployment-57h0)
- [Is Your NPU Ready for LLMs? (arXiv)](https://arxiv.org/pdf/2607.05475)
- [PowerInfer-2 (arXiv)](https://arxiv.org/html/2406.06282v3)
- [T-MAC (arXiv)](https://arxiv.org/html/2407.00088v2)
- [bitnet.cpp (arXiv)](https://arxiv.org/pdf/2502.11880)
- [PrismML 1-bit Bonsai (The Register)](https://www.theregister.com/2026/04/04/prismml_1bit_llm/)
- [PrismML emerges from stealth (HPCwire)](https://www.hpcwire.com/2026/04/03/prismml-emerges-from-stealth-with-1-bit-llm-family/)
- [exo](https://github.com/exo-explore/exo)
- [Apple Foundation Models framework](https://www.developer.apple.com/machine-learning/)
