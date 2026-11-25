// Infera phone engine: a small wrapper over the llama.cpp C API (pinned in third_party/llama.cpp).
#pragma once
#include <condition_variable>
#include <cstdint>
#include <deque>
#include <functional>
#include <memory>
#include <mutex>
#include <thread>
#include <string>
#include <utility>
#include <vector>

struct llama_model;
struct llama_context;
struct llama_vocab;

namespace infera {

struct Message {
    std::string role, content;
};

struct Slot;

// Batched engine: up to n_parallel requests share each decode step (one pass over the
// weights serves every active request), which multiplies tokens per second on phones,
// where decoding is limited by memory bandwidth. generate() is safe to call from several
// threads at once; each call blocks until its own answer is finished.
class Engine {
  public:
    ~Engine();
    // Loads a GGUF model. n_ctx is per request. Returns "" on success, otherwise the reason.
    std::string load(const std::string &path, int n_ctx, int n_threads, int n_parallel = 1);
    // Streams the answer. `on_text` receives complete UTF-8 pieces; return false to stop early.
    // Returns {prompt_tokens, completion_tokens}, or {-1, -1} on error.
    std::pair<int, int> generate(const std::vector<Message> &messages, int max_tokens, float temperature,
                                 const std::function<bool(const std::string &)> &on_text);
    uint64_t n_params() const;
    int n_parallel() const { return n_par_; }

  private:
    void loop();
    std::vector<int> tokenize_chat(const std::vector<Message> &messages);
    llama_model *model_ = nullptr;
    llama_context *ctx_ = nullptr;
    const llama_vocab *vocab_ = nullptr;
    int n_ctx_ = 0;  // per request
    int n_par_ = 1;
    std::mutex mu_;
    std::condition_variable cv_;
    std::deque<std::shared_ptr<Slot>> waiting_;
    std::vector<std::shared_ptr<Slot>> active_;
    std::vector<bool> seq_used_;
    bool stop_ = false;
    std::thread worker_;
};

// Removes an empty "<think>\n\n</think>\n\n" block that Qwen3 emits when thinking is off.
// `head` is the start of the answer seen so far; returns how many bytes to drop.
size_t empty_think_prefix(const std::string &head);

// Length of the longest prefix of `s` that ends on a complete UTF-8 character.
size_t utf8_complete_prefix(const std::string &s);

}  // namespace infera
