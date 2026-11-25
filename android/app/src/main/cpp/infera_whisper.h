#pragma once
// Speech-to-text engine for phones: whisper.cpp, built on the same ggml as the chat engine.
#include <string>
#include <vector>

struct whisper_context;

namespace infera {

class Speech {
public:
    ~Speech();
    // Loads a ggml Whisper model (e.g. ggml-base.en.bin). Returns "" or an error.
    std::string load(const std::string &path, int n_threads);
    // pcm: 16 kHz mono float samples. Returns JSON {"text","language","duration","segments":[...]}
    // or "" on failure.
    std::string transcribe(const std::vector<float> &pcm, const std::string &language, const std::string &prompt);
private:
    whisper_context *ctx_ = nullptr;
    int threads_ = 4;
};

std::string json_escape(const std::string &s);

} // namespace infera
