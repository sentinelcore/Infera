package main

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
)

// Model catalog: the models everyone serves under shared names, so apps can offer a one-tap
// picker instead of asking for URLs. Override with INFERA_CATALOG (same JSON shape).
type CatalogModel struct {
	ID        string  `json:"id"`                        // name customers request, e.g. qwen3-1.7b
	Label     string  `json:"label"`                     // shown in pickers
	Kind      string  `json:"kind"`                      // chat | embeddings
	ParamsB   float64 `json:"params_b"`                  // size, for device classes
	GGUFURL   string  `json:"gguf_url"`                  // phones (llama.cpp)
	GGUFMB    int     `json:"gguf_mb"`                   // download size
	SHA256    string  `json:"sha256,omitempty"`          // of the GGUF; empty = apps use the host's X-Linked-Etag (Hugging Face)
	OllamaTag string  `json:"ollama_tag"`                // desktops (Ollama)
	HFID      string  `json:"hugging_face_id,omitempty"` // original weights, for marketplaces
	Ctx       int     `json:"context_length,omitempty"`  // tokens we serve per request
	Quant     string  `json:"quantization,omitempty"`    // int4 | int8 | fp16
	PhoneRAM  float64 `json:"phone_min_ram_gb,omitempty"`
	DeskRAM   float64 `json:"desktop_min_ram_gb,omitempty"`
	DeskVRAM  float64 `json:"desktop_min_vram_gb,omitempty"`
}

var defaultCatalog = []CatalogModel{
	{ID: "qwen3-0.6b", Label: "Qwen3 0.6B · light, any phone", Kind: "chat", ParamsB: 0.6, HFID: "Qwen/Qwen3-0.6B", Ctx: 2048, Quant: "int8",
		GGUFURL: "https://huggingface.co/Qwen/Qwen3-0.6B-GGUF/resolve/main/Qwen3-0.6B-Q8_0.gguf", GGUFMB: 640, OllamaTag: "qwen3:0.6b", PhoneRAM: 3, DeskRAM: 4},
	{ID: "qwen3-1.7b", Label: "Qwen3 1.7B · balanced (recommended)", Kind: "chat", ParamsB: 1.7, HFID: "Qwen/Qwen3-1.7B", Ctx: 2048, Quant: "int8",
		GGUFURL: "https://huggingface.co/Qwen/Qwen3-1.7B-GGUF/resolve/main/Qwen3-1.7B-Q8_0.gguf", GGUFMB: 1830, OllamaTag: "qwen3:1.7b", PhoneRAM: 6, DeskRAM: 8},
	{ID: "qwen3-4b", Label: "Qwen3 4B · flagship phones", Kind: "chat", ParamsB: 4, HFID: "Qwen/Qwen3-4B", Ctx: 2048, Quant: "int4",
		GGUFURL: "https://huggingface.co/Qwen/Qwen3-4B-GGUF/resolve/main/Qwen3-4B-Q4_K_M.gguf", GGUFMB: 2500, OllamaTag: "qwen3:4b", PhoneRAM: 10, DeskRAM: 12},
	{ID: "qwen3-8b", Label: "Qwen3 8B · GPUs with 8 GB+, Macs with 16 GB+", Kind: "chat", ParamsB: 8, HFID: "Qwen/Qwen3-8B", Ctx: 4096, Quant: "int4",
		OllamaTag: "qwen3:8b", DeskRAM: 16, DeskVRAM: 8},
	{ID: "qwen3-14b", Label: "Qwen3 14B · GPUs with 16 GB+, Macs with 32 GB+", Kind: "chat", ParamsB: 14, HFID: "Qwen/Qwen3-14B", Ctx: 4096, Quant: "int4",
		OllamaTag: "qwen3:14b", DeskRAM: 32, DeskVRAM: 16},
	{ID: "llama-3.1-8b-instruct", Label: "Llama 3.1 8B Instruct · GPUs with 8 GB+, Macs with 16 GB+", Kind: "chat", ParamsB: 8, HFID: "meta-llama/Llama-3.1-8B-Instruct", Ctx: 4096, Quant: "int4",
		OllamaTag: "llama3.1:8b", DeskRAM: 16, DeskVRAM: 8},
	{ID: "gemma-3-4b-it", Label: "Gemma 3 4B · flagship phones, any desktop", Kind: "chat", ParamsB: 4, HFID: "google/gemma-3-4b-it", Ctx: 2048, Quant: "int4",
		OllamaTag: "gemma3:4b", DeskRAM: 8},
	{ID: "gemma-3-12b-it", Label: "Gemma 3 12B · GPUs with 12 GB+, Macs with 16 GB+", Kind: "chat", ParamsB: 12, HFID: "google/gemma-3-12b-it", Ctx: 4096, Quant: "int4",
		OllamaTag: "gemma3:12b", DeskRAM: 16, DeskVRAM: 12},
	{ID: "mistral-nemo", Label: "Mistral Nemo 12B · GPUs with 12 GB+, Macs with 16 GB+", Kind: "chat", ParamsB: 12, HFID: "mistralai/Mistral-Nemo-Instruct-2407", Ctx: 4096, Quant: "int4",
		OllamaTag: "mistral-nemo:12b", DeskRAM: 16, DeskVRAM: 12},
	{ID: "deepseek-r1-distill-llama-8b", Label: "DeepSeek R1 Distill Llama 8B · reasoning, GPUs with 8 GB+", Kind: "chat", ParamsB: 8, HFID: "deepseek-ai/DeepSeek-R1-Distill-Llama-8B", Ctx: 4096, Quant: "int4",
		OllamaTag: "deepseek-r1:8b", DeskRAM: 16, DeskVRAM: 8},
	{ID: "deepseek-r1-distill-qwen-14b", Label: "DeepSeek R1 Distill Qwen 14B · reasoning, GPUs with 16 GB+", Kind: "chat", ParamsB: 14, HFID: "deepseek-ai/DeepSeek-R1-Distill-Qwen-14B", Ctx: 4096, Quant: "int4",
		OllamaTag: "deepseek-r1:14b", DeskRAM: 32, DeskVRAM: 16},
	{ID: "whisper-base", Label: "Whisper Base · speech to text (99 languages)", Kind: "transcription", ParamsB: 0.074, HFID: "openai/whisper-base", Quant: "int8",
		GGUFURL: "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base-q5_1.bin", GGUFMB: 60, PhoneRAM: 3, DeskRAM: 4},
	{ID: "nomic-embed-text", Label: "Nomic Embed · embeddings", Kind: "embeddings", ParamsB: 0.14, HFID: "nomic-ai/nomic-embed-text-v1.5", Ctx: 2048, Quant: "fp16",
		OllamaTag: "nomic-embed-text", DeskRAM: 8},
}

func catalog() []CatalogModel {
	if s := os.Getenv("INFERA_CATALOG"); s != "" {
		var c []CatalogModel
		if json.Unmarshal([]byte(s), &c) == nil && len(c) > 0 {
			return c
		}
	}
	return defaultCatalog
}

// GET /api/catalog
func (a *API) Catalog(w http.ResponseWriter, r *http.Request) {
	// Live prices and owner share, so apps can show honest earning estimates.
	writeJSON(w, map[string]any{"models": catalog(), "pricing": map[string]any{
		"in_per_m": a.cfg.DefaultPrice.In, "out_per_m": a.cfg.DefaultPrice.Out, "per_model": a.cfg.Prices,
		"embed_per_m": a.cfg.EmbedPrice, "audio_per_min": a.cfg.AudioPrice, "owner_share": a.cfg.OwnerShare,
		"batch_discount": a.cfg.BatchDiscount,
	}})
}

// GET /pair/{code} — opened on a phone: one tap opens the Infera app, which pairs itself
// with this server and code (no typing). Shows the code as a fallback.
func (a *API) PairPage(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
		scheme = "http"
	}
	server := scheme + "://" + r.Host
	deep := "infera://pair?server=" + url.QueryEscape(server) + "&code=" + url.QueryEscape(code)
	intent := "intent://pair?server=" + url.QueryEscape(server) + "&code=" + url.QueryEscape(code) +
		"#Intent;scheme=infera;package=network.infera.node;S.browser_fallback_url=" + url.QueryEscape(server+"/download") + ";end"
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Pair with Infera</title>
<style>body{font-family:system-ui,sans-serif;background:#0e1412;color:#e3ebe7;display:grid;place-items:center;min-height:100vh;margin:0;padding:16px;text-align:center}
a.b{display:block;margin:14px auto;padding:14px 18px;border-radius:10px;background:#4cc398;color:#0e1412;font-weight:700;text-decoration:none;max-width:320px}
code{font-size:2rem;letter-spacing:.2em;color:#4cc398}p{color:#9aaba3}</style></head><body><div>
<h1>Pair this phone</h1><a class="b" href="%s">Open Infera and pair</a>
<p>Don't have the app yet? <a style="color:#4cc398" href="/download">Download it</a>, then come back to this page.</p>
<p>Or type this code in the app:</p><code>%s</code>
<p style="font-size:.8rem">iPhone / other browsers: <a style="color:#4cc398" href="%s">infera:// link</a></p></div></body></html>`,
		html.EscapeString(intent), html.EscapeString(code), html.EscapeString(deep))
}
