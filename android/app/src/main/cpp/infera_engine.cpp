#include "infera_engine.h"

#include <algorithm>
#include <cstring>
#include <mutex>

#include "llama.h"

#if defined(__GNUC__)
#pragma GCC diagnostic ignored "-Wdeprecated-declarations"
#endif

namespace infera {

static std::once_flag g_backend;

Engine::~Engine() {
    if (ctx_) llama_free(ctx_);
    if (model_) llama_model_free(model_);
}

std::string Engine::load(const std::string &path, int n_ctx, int n_threads) {
    std::call_once(g_backend, [] { llama_backend_init(); });
    auto mp = llama_model_default_params();
    model_ = llama_model_load_from_file(path.c_str(), mp);
    if (!model_) return "could not load the model file";
    vocab_ = llama_model_get_vocab(model_);
    auto cp = llama_context_default_params();
    cp.n_ctx = n_ctx > 0 ? n_ctx : 4096;
    cp.n_batch = 512;
    if (n_threads > 0) {
        cp.n_threads = n_threads;
        cp.n_threads_batch = n_threads;
    }
    ctx_ = llama_init_from_model(model_, cp);
    if (!ctx_) return "could not create the inference context";
    n_ctx_ = cp.n_ctx;
    return "";
}

uint64_t Engine::n_params() const { return model_ ? llama_model_n_params(model_) : 0; }

size_t utf8_complete_prefix(const std::string &s) {
    size_t n = s.size();
    // Walk back over at most 3 continuation bytes to find the start of the last character.
    size_t i = n;
    int back = 0;
    while (i > 0 && back < 4) {
        unsigned char c = static_cast<unsigned char>(s[i - 1]);
        if ((c & 0xC0) != 0x80) {  // lead byte or ASCII
            size_t need = (c < 0x80) ? 1 : (c >> 5) == 0x6 ? 2 : (c >> 4) == 0xE ? 3 : (c >> 3) == 0x1E ? 4 : 1;
            return (n - (i - 1) >= need) ? n : i - 1;
        }
        --i;
        ++back;
    }
    return n;
}

std::pair<int, int> Engine::generate(const std::vector<Message> &messages, int max_tokens, float temperature,
                                     const std::function<bool(const std::string &)> &on_text) {
    if (!ctx_) return {-1, -1};
    llama_memory_clear(llama_get_memory(ctx_), true);

    // Prompt from the model's own chat template (falls back to ChatML).
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
    std::vector<llama_token> toks(std::max(n, 1));
    if (llama_tokenize(vocab_, prompt.c_str(), (int)prompt.size(), toks.data(), (int)toks.size(), true, true) < 0)
        return {-1, -1};
    const int n_prompt = (int)toks.size();
    if (n_prompt + max_tokens > n_ctx_) max_tokens = std::max(1, n_ctx_ - n_prompt - 1);
    if (n_prompt >= n_ctx_) return {-1, -1};

    auto *smpl = llama_sampler_chain_init(llama_sampler_chain_default_params());
    if (temperature <= 0.0f) {
        llama_sampler_chain_add(smpl, llama_sampler_init_greedy());
    } else {
        llama_sampler_chain_add(smpl, llama_sampler_init_top_k(40));
        llama_sampler_chain_add(smpl, llama_sampler_init_top_p(0.95f, 1));
        llama_sampler_chain_add(smpl, llama_sampler_init_temp(temperature));
        llama_sampler_chain_add(smpl, llama_sampler_init_dist(LLAMA_DEFAULT_SEED));
    }

    // Prompt in chunks of n_batch, then one token at a time.
    for (int i = 0; i < n_prompt; i += 512) {
        int m = std::min(512, n_prompt - i);
        if (llama_decode(ctx_, llama_batch_get_one(toks.data() + i, m)) != 0) {
            llama_sampler_free(smpl);
            return {-1, -1};
        }
    }
    int produced = 0;
    std::string pending;
    char piece[256];
    bool keep_going = true;
    while (produced < max_tokens && keep_going) {
        llama_token id = llama_sampler_sample(smpl, ctx_, -1);
        if (llama_vocab_is_eog(vocab_, id)) break;
        int k = llama_token_to_piece(vocab_, id, piece, sizeof(piece), 0, false);
        if (k > 0) pending.append(piece, k);
        size_t ok = utf8_complete_prefix(pending);
        if (ok > 0) {
            keep_going = on_text(pending.substr(0, ok));
            pending.erase(0, ok);
        }
        produced++;
        if (llama_decode(ctx_, llama_batch_get_one(&id, 1)) != 0) break;
    }
    if (!pending.empty() && keep_going) on_text(pending);
    llama_sampler_free(smpl);
    return {n_prompt, produced};
}

}  // namespace infera
