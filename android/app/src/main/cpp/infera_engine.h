// Infera phone engine: a small wrapper over the llama.cpp C API (pinned in third_party/llama.cpp).
#pragma once
#include <cstdint>
#include <functional>
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

class Engine {
  public:
    ~Engine();
    // Loads a GGUF model. Returns an empty string on success, otherwise the reason.
    std::string load(const std::string &path, int n_ctx, int n_threads);
    // Streams the answer. `on_text` receives complete UTF-8 pieces; return false to stop early.
    // Returns {prompt_tokens, completion_tokens}, or {-1, -1} on error.
    std::pair<int, int> generate(const std::vector<Message> &messages, int max_tokens, float temperature,
                                 const std::function<bool(const std::string &)> &on_text);
    uint64_t n_params() const;

  private:
    llama_model *model_ = nullptr;
    llama_context *ctx_ = nullptr;
    const llama_vocab *vocab_ = nullptr;
    int n_ctx_ = 0;
};

// Length of the longest prefix of `s` that ends on a complete UTF-8 character.
size_t utf8_complete_prefix(const std::string &s);

}  // namespace infera
