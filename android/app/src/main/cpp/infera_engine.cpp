#include "infera_engine.h"

#include <algorithm>
#include <cstring>
#include <fstream>
#if defined(__linux__)
#include <sched.h>
#endif

#include "llama.h"

#if defined(__GNUC__)
#pragma GCC diagnostic ignored "-Wdeprecated-declarations"
#endif

namespace infera {

static std::once_flag g_backend;

// One request being answered.
struct Slot {
    std::vector<llama_token> prompt;
    int max_tokens = 0;
    llama_sampler *smpl = nullptr;
    int seq = -1;
    int n_past = 0;
    int produced = 0;
    llama_token last = 0;
    bool started = false;
    std::string pending;  // bytes not yet forming a whole UTF-8 character
    // Shared with the calling thread:
    std::mutex m;
    std::condition_variable cv;
    std::deque<std::string> out;
    bool done = false, error = false, cancel = false;
};

Engine::~Engine() {
    {
        std::lock_guard<std::mutex> l(mu_);
        stop_ = true;
    }
    cv_.notify_all();
    if (worker_.joinable()) worker_.join();
    if (ctx_) llama_free(ctx_);
    if (model_) llama_model_free(model_);
}

std::string Engine::load(const std::string &path, int n_ctx, int n_threads, int n_parallel) {
    std::call_once(g_backend, [] { llama_backend_init(); });
    auto mp = llama_model_default_params();
    model_ = llama_model_load_from_file(path.c_str(), mp);
    if (!model_) return "could not load the model file";
    vocab_ = llama_model_get_vocab(model_);
    n_par_ = std::max(1, std::min(n_parallel, 8));
    n_ctx_ = n_ctx > 0 ? n_ctx : 4096;
    auto cp = llama_context_default_params();
    cp.n_seq_max = n_par_;
    cp.n_ctx = n_ctx_ * n_par_;  // each request gets n_ctx positions
    cp.n_batch = 512;
    cp.n_ubatch = 512;
    cp.kv_unified = false;
    if (n_threads > 0) {
        cp.n_threads = n_threads;
        cp.n_threads_batch = n_threads;
    }
    // 16-bit attention cache. (An 8-bit cache with flash attention halves the memory but was
    // measured slower on a Snapdragon 8 Elite: 15.5 -> 7.5 tok/s together with the i8mm build.)
    cp.type_k = GGML_TYPE_F16;
    cp.type_v = GGML_TYPE_F16;
    ctx_ = llama_init_from_model(model_, cp);
    if (!ctx_ && cp.n_ctx > 2048u * n_par_) {  // not enough memory: shorter windows
        cp.n_ctx = 2048u * n_par_;
        ctx_ = llama_init_from_model(model_, cp);
    }
    if (!ctx_) return "could not create the inference context";
    n_ctx_ = (int)llama_n_ctx_seq(ctx_);
    seq_used_.assign(n_par_, false);
    worker_ = std::thread([this] { loop(); });
    return "";
}

uint64_t Engine::n_params() const { return model_ ? llama_model_n_params(model_) : 0; }

size_t utf8_complete_prefix(const std::string &s) {
    size_t n = s.size();
    size_t i = n;
    int back = 0;
    while (i > 0 && back < 4) {
        unsigned char c = static_cast<unsigned char>(s[i - 1]);
        if ((c & 0xC0) != 0x80) {
            size_t need = (c < 0x80) ? 1 : (c >> 5) == 0x6 ? 2 : (c >> 4) == 0xE ? 3 : (c >> 3) == 0x1E ? 4 : 1;
            return (n - (i - 1) >= need) ? n : i - 1;
        }
        --i;
        ++back;
    }
    return n;
}

size_t empty_think_prefix(const std::string &head) {
    // Skip leading whitespace, then "<think>", whitespace, "</think>", whitespace.
    size_t i = 0;
    auto ws = [&] { while (i < head.size() && (head[i] == ' ' || head[i] == '\n' || head[i] == '\r' || head[i] == '\t')) i++; };
    ws();
    if (head.compare(i, 7, "<think>") != 0) return 0;
    i += 7;
    ws();
    if (head.compare(i, 8, "</think>") != 0) return 0;
    i += 8;
    ws();
    return i;
}

// True while `head` could still turn out to be "<think>" ws "</think>" (so output waits).
static bool could_be_empty_think(const std::string &head) {
    const std::string pat = "<think></think>";
    size_t k = 0;
    for (char c : head) {
        if (c == ' ' || c == '\n' || c == '\r' || c == '\t') {
            if (k == 0 || k == 7) continue;  // whitespace allowed before and between the tags
            return false;
        }
        if (k >= pat.size() || c != pat[k]) return false;
        k++;
    }
    return true;
}

std::vector<int> Engine::tokenize_chat(const std::vector<Message> &messages) {
    std::vector<llama_chat_message> chat;
    for (auto &m : messages) chat.push_back({m.role.c_str(), m.content.c_str()});
    const char *tmpl = llama_model_chat_template(model_, nullptr);
    std::vector<char> buf(4096);
    int len = llama_chat_apply_template(tmpl, chat.data(), chat.size(), true, buf.data(), (int)buf.size());
    if (len > (int)buf.size()) {
        buf.resize(len + 1);
        len = llama_chat_apply_template(tmpl, chat.data(), chat.size(), true, buf.data(), (int)buf.size());
    }
    std::string prompt;
    if (len < 0) {  // unknown template: ChatML
        for (auto &m : messages) prompt += "<|im_start|>" + m.role + "\n" + m.content + "<|im_end|>\n";
        prompt += "<|im_start|>assistant\n";
    } else {
        prompt.assign(buf.data(), len);
    }
    int n = -llama_tokenize(vocab_, prompt.c_str(), (int)prompt.size(), nullptr, 0, true, true);
    std::vector<int> toks(std::max(n, 1));
    if (llama_tokenize(vocab_, prompt.c_str(), (int)prompt.size(), toks.data(), (int)toks.size(), true, true) < 0) return {};
    return toks;
}

std::pair<int, int> Engine::generate(const std::vector<Message> &messages, int max_tokens, float temperature,
                                     const std::function<bool(const std::string &)> &on_text) {
    if (!ctx_) return {-1, -1};
    return run(tokenize_chat(messages), max_tokens, temperature, on_text);
}

std::pair<int, int> Engine::generate_text(const std::string &text, int max_tokens, float temperature,
                                          const std::function<bool(const std::string &)> &on_text) {
    if (!ctx_) return {-1, -1};
    // Raw text (no chat template); special tokens such as <|fim_prefix|> are parsed.
    int n = -llama_tokenize(vocab_, text.c_str(), (int)text.size(), nullptr, 0, false, true);
    std::vector<int> toks(std::max(n, 1));
    if (llama_tokenize(vocab_, text.c_str(), (int)text.size(), toks.data(), (int)toks.size(), false, true) < 0) return {-1, -1};
    return run(toks, max_tokens, temperature, on_text);
}

std::pair<int, int> Engine::run(const std::vector<int> &toks, int max_tokens, float temperature,
                                const std::function<bool(const std::string &)> &on_text) {
    auto s = std::make_shared<Slot>();
    if (toks.empty()) return {-1, -1};
    s->prompt.assign(toks.begin(), toks.end());
    const int n_prompt = (int)s->prompt.size();
    if (n_prompt >= n_ctx_) return {-1, -1};
    s->max_tokens = std::max(1, std::min(max_tokens, n_ctx_ - n_prompt - 1));
    s->smpl = llama_sampler_chain_init(llama_sampler_chain_default_params());
    if (temperature <= 0.0f) {
        llama_sampler_chain_add(s->smpl, llama_sampler_init_greedy());
    } else {
        llama_sampler_chain_add(s->smpl, llama_sampler_init_top_k(40));
        llama_sampler_chain_add(s->smpl, llama_sampler_init_top_p(0.95f, 1));
        llama_sampler_chain_add(s->smpl, llama_sampler_init_temp(temperature));
        llama_sampler_chain_add(s->smpl, llama_sampler_init_dist(LLAMA_DEFAULT_SEED));
    }
    {
        std::lock_guard<std::mutex> l(mu_);
        waiting_.push_back(s);
    }
    cv_.notify_all();

    // Deliver pieces on the caller's thread (JNI callbacks need an attached Java thread).
    std::string head;  // start of the answer, to drop an empty <think></think> block
    bool head_done = false, keep = true, trim_ws = false;
    while (true) {
        std::unique_lock<std::mutex> l(s->m);
        s->cv.wait(l, [&] { return !s->out.empty() || s->done; });
        while (!s->out.empty()) {
            std::string piece = std::move(s->out.front());
            s->out.pop_front();
            l.unlock();
            if (keep) {
                if (!head_done) {
                    head += piece;
                    // Wait until we can tell whether the answer starts with an empty think block.
                    size_t drop = empty_think_prefix(head);
                    bool undecided = drop == 0 && head.size() < 40 && could_be_empty_think(head);
                    if (drop == 0 && undecided && !s->done) { l.lock(); continue; }
                    head_done = true;
                    piece = head.substr(drop);
                    trim_ws = drop > 0;
                    head.clear();
                }
                if (trim_ws) {  // blank lines after a dropped think block
                    size_t f = piece.find_first_not_of(" \n\r\t");
                    piece = f == std::string::npos ? "" : piece.substr(f);
                    if (!piece.empty()) trim_ws = false;
                }
                if (!piece.empty() && !on_text(piece)) {
                    keep = false;
                    std::lock_guard<std::mutex> g(s->m);
                    s->cancel = true;
                }
            }
            l.lock();
        }
        if (s->done) break;
    }
    if (keep && !head_done && !head.empty()) on_text(head.substr(empty_think_prefix(head)));
    llama_sampler_free(s->smpl);
    if (s->error) return {-1, -1};
    return {n_prompt, s->produced};
}

// Worker: admits waiting requests (prefill), then decodes one token for every active
// request per step, in a single llama_decode call.
// Pins the calling thread (and the compute threads it later creates, which inherit the
// mask) to the fast cores: those whose top frequency is within 70% of the fastest core.
static void pin_to_fast_cores() {
#if defined(__linux__)
    std::vector<long> freq;
    for (int i = 0; i < 64; i++) {
        std::ifstream f("/sys/devices/system/cpu/cpu" + std::to_string(i) + "/cpufreq/cpuinfo_max_freq");
        long v = 0;
        if (!(f >> v)) break;
        freq.push_back(v);
    }
    if (freq.size() < 2) return;
    long top = *std::max_element(freq.begin(), freq.end());
    cpu_set_t set;
    CPU_ZERO(&set);
    int n = 0;
    for (size_t i = 0; i < freq.size(); i++)
        if (freq[i] * 10 >= top * 7) { CPU_SET(i, &set); n++; }
    if (n > 0 && n < (int)freq.size()) sched_setaffinity(0, sizeof(set), &set);
#endif
}

void Engine::loop() {
    pin_to_fast_cores();
    llama_batch step = llama_batch_init(n_par_, 0, 1);
    llama_batch pre = llama_batch_init(512, 0, 1);
    auto mem = llama_get_memory(ctx_);
    char piece[256];

    auto finish = [&](const std::shared_ptr<Slot> &s, bool error) {
        if (s->seq >= 0) {
            llama_memory_seq_rm(mem, s->seq, -1, -1);
            std::lock_guard<std::mutex> l(mu_);
            seq_used_[s->seq] = false;
        }
        std::lock_guard<std::mutex> g(s->m);
        if (!s->pending.empty() && !error) s->out.push_back(s->pending);
        s->error = error;
        s->done = true;
        s->cv.notify_all();
    };
    // Turns a sampled token into output; returns false when the request is finished.
    auto emit = [&](const std::shared_ptr<Slot> &s, llama_token id) {
        bool cancel;
        {
            std::lock_guard<std::mutex> g(s->m);
            cancel = s->cancel;
        }
        if (cancel || llama_vocab_is_eog(vocab_, id) || s->produced >= s->max_tokens) return false;
        int k = llama_token_to_piece(vocab_, id, piece, sizeof(piece), 0, false);
        if (k > 0) s->pending.append(piece, k);
        s->produced++;
        size_t ok = utf8_complete_prefix(s->pending);
        if (ok > 0) {
            std::lock_guard<std::mutex> g(s->m);
            s->out.push_back(s->pending.substr(0, ok));
            s->cv.notify_all();
            s->pending.erase(0, ok);
        }
        s->last = id;
        return s->produced < s->max_tokens;
    };

    while (true) {
        std::vector<std::shared_ptr<Slot>> admit;
        {
            std::unique_lock<std::mutex> l(mu_);
            cv_.wait(l, [&] { return stop_ || !waiting_.empty() || !active_.empty(); });
            if (stop_) break;
            for (int q = 0; q < n_par_ && !waiting_.empty(); q++) {
                if (seq_used_[q]) continue;
                auto s = waiting_.front();
                waiting_.pop_front();
                s->seq = q;
                seq_used_[q] = true;
                admit.push_back(s);
            }
        }
        // Prefill new requests (prompt processing is compute-bound; one request at a time).
        for (auto &s : admit) {
            llama_memory_seq_rm(mem, s->seq, -1, -1);
            const int n = (int)s->prompt.size();
            bool ok = true;
            for (int i = 0; i < n && ok; i += 512) {
                int m = std::min(512, n - i);
                pre.n_tokens = m;
                for (int j = 0; j < m; j++) {
                    pre.token[j] = s->prompt[i + j];
                    pre.pos[j] = i + j;
                    pre.n_seq_id[j] = 1;
                    pre.seq_id[j][0] = s->seq;
                    pre.logits[j] = (i + j == n - 1);
                }
                ok = llama_decode(ctx_, pre) == 0;
            }
            if (!ok) { finish(s, true); continue; }
            s->n_past = n;
            llama_token id = llama_sampler_sample(s->smpl, ctx_, pre.n_tokens - 1);
            if (emit(s, id)) {
                std::lock_guard<std::mutex> l(mu_);
                active_.push_back(s);
            } else {
                finish(s, false);
            }
        }
        std::vector<std::shared_ptr<Slot>> act;
        {
            std::lock_guard<std::mutex> l(mu_);
            act = active_;
        }
        if (act.empty()) continue;
        // One step: the last token of every active request, decoded together.
        step.n_tokens = (int)act.size();
        for (int i = 0; i < (int)act.size(); i++) {
            step.token[i] = act[i]->last;
            step.pos[i] = act[i]->n_past;
            step.n_seq_id[i] = 1;
            step.seq_id[i][0] = act[i]->seq;
            step.logits[i] = 1;
        }
        bool ok = llama_decode(ctx_, step) == 0;
        std::vector<std::shared_ptr<Slot>> keep;
        for (int i = 0; i < (int)act.size(); i++) {
            auto &s = act[i];
            if (!ok) { finish(s, true); continue; }
            s->n_past++;
            llama_token id = llama_sampler_sample(s->smpl, ctx_, i);
            if (s->n_past + 1 >= n_ctx_ || !emit(s, id)) finish(s, false);
            else keep.push_back(s);
        }
        {
            std::lock_guard<std::mutex> l(mu_);
            active_ = keep;
        }
    }
    llama_batch_free(step);
    llama_batch_free(pre);
}

}  // namespace infera
