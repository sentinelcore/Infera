#include "infera_whisper.h"
#include "whisper.h"
#include <cstdio>

namespace infera {

std::string json_escape(const std::string &s) {
    std::string o;
    o.reserve(s.size() + 8);
    for (unsigned char c : s) {
        switch (c) {
            case '"': o += "\\\""; break;
            case '\\': o += "\\\\"; break;
            case '\n': o += "\\n"; break;
            case '\r': o += "\\r"; break;
            case '\t': o += "\\t"; break;
            default:
                if (c < 0x20) { char b[8]; snprintf(b, sizeof b, "\\u%04x", c); o += b; }
                else o += (char)c;
        }
    }
    return o;
}

Speech::~Speech() { if (ctx_) whisper_free(ctx_); }

std::string Speech::load(const std::string &path, int n_threads) {
    whisper_context_params cp = whisper_context_default_params();
    cp.use_gpu = false;
    ctx_ = whisper_init_from_file_with_params(path.c_str(), cp);
    if (!ctx_) return "could not load the speech model";
    threads_ = n_threads > 0 ? n_threads : 4;
    return "";
}

std::string Speech::transcribe(const std::vector<float> &pcm, const std::string &language, const std::string &prompt) {
    if (!ctx_ || pcm.empty()) return "";
    whisper_full_params p = whisper_full_default_params(WHISPER_SAMPLING_GREEDY);
    p.n_threads = threads_;
    p.print_progress = false;
    p.print_realtime = false;
    p.print_timestamps = false;
    p.print_special = false;
    p.translate = false;
    p.language = language.empty() ? "auto" : language.c_str();
    p.detect_language = false;
    if (!prompt.empty()) p.initial_prompt = prompt.c_str();
    if (whisper_full(ctx_, p, pcm.data(), (int)pcm.size()) != 0) return "";
    std::string text, segs;
    const int n = whisper_full_n_segments(ctx_);
    for (int i = 0; i < n; i++) {
        const char *t = whisper_full_get_segment_text(ctx_, i);
        text += t;
        char b[96];
        snprintf(b, sizeof b, "%s{\"id\":%d,\"start\":%.2f,\"end\":%.2f,\"text\":\"", i ? "," : "", i,
                 whisper_full_get_segment_t0(ctx_, i) / 100.0, whisper_full_get_segment_t1(ctx_, i) / 100.0);
        segs += b;
        segs += json_escape(t);
        segs += "\"}";
    }
    const int lang = whisper_full_lang_id(ctx_);
    const char *lname = lang >= 0 ? whisper_lang_str(lang) : "";
    char d[64];
    snprintf(d, sizeof d, "%.2f", pcm.size() / 16000.0);
    return std::string("{\"text\":\"") + json_escape(text) + "\",\"language\":\"" + (lname ? lname : "") +
           "\",\"duration\":" + d + ",\"segments\":[" + segs + "]}";
}

} // namespace infera
