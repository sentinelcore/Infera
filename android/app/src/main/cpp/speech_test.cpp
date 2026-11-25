// Desktop test for the phone speech engine: speech_test <ggml-model.bin> <16kHz mono 16-bit wav>
#include <cstdio>
#include <cstdint>
#include <fstream>
#include <string>
#include <vector>
#include "infera_whisper.h"

int main(int argc, char **argv) {
    if (infera::json_escape("a\"b\n") != "a\\\"b\\n") { puts("escape failed"); return 1; }
    if (argc < 3) { puts("escape ok"); return 0; }
    std::ifstream f(argv[2], std::ios::binary);
    std::vector<char> raw((std::istreambuf_iterator<char>(f)), {});
    // Find the "data" chunk (WAV files can carry LIST and other chunks first).
    size_t pos = 12, data = 0, len = 0;
    auto u32 = [&](size_t i) { return (uint32_t)(uint8_t)raw[i] | (uint32_t)(uint8_t)raw[i + 1] << 8 | (uint32_t)(uint8_t)raw[i + 2] << 16 | (uint32_t)(uint8_t)raw[i + 3] << 24; };
    while (pos + 8 <= raw.size()) {
        uint32_t sz = u32(pos + 4);
        if (std::string(&raw[pos], 4) == "data") { data = pos + 8; len = sz; break; }
        pos += 8 + sz + (sz & 1);
    }
    if (!data) { puts("bad wav"); return 1; }
    std::vector<float> pcm;
    for (size_t i = data; i + 1 < data + len && i + 1 < raw.size(); i += 2) pcm.push_back((int16_t)((uint8_t)raw[i] | ((uint8_t)raw[i + 1] << 8)) / 32768.0f);
    printf("%.1f s of audio\n", pcm.size() / 16000.0);
    infera::Speech s;
    auto err = s.load(argv[1], 4);
    if (!err.empty()) { printf("load failed: %s\n", err.c_str()); return 1; }
    auto out = s.transcribe(pcm, "en", "");
    printf("%s\n", out.c_str());
    return out.find("ask not") != std::string::npos || out.find("Ask not") != std::string::npos ? 0 : 1;
}
