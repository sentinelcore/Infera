// Desktop test for the phone engine: engine_test <model.gguf>
#include <cassert>
#include <atomic>
#include <chrono>
#include <cstdio>
#include <thread>
#include <vector>
#include "infera_engine.h"

int main(int argc, char **argv) {
    // UTF-8 boundary handling
    assert(infera::utf8_complete_prefix("abc") == 3);
    assert(infera::utf8_complete_prefix(std::string("a\xE2\x82", 3)) == 1);       // incomplete euro sign
    assert(infera::utf8_complete_prefix(std::string("a\xE2\x82\xAC", 4)) == 4);   // complete
    // Empty think block removal (Qwen3 with thinking off)
    assert(infera::empty_think_prefix("<think>\n\n</think>\n\nHi") == 19);
    assert(infera::empty_think_prefix("Hi <think></think>") == 0);
    assert(infera::empty_think_prefix("<think>x</think>") == 0);
    if (argc < 2) { puts("utf8 ok"); return 0; }
    infera::Engine e;
    auto err = e.load(argv[1], 512, 2, 4);
    if (!err.empty()) { printf("load failed: %s\n", err.c_str()); return 1; }
    printf("params: %llu\n", (unsigned long long)e.n_params());
    std::string out;
    int pieces = 0;
    auto r = e.generate({{"user", "Hello"}}, 16, 0.0f, [&](const std::string &p) { out += p; pieces++; return true; });
    printf("prompt=%d completion=%d pieces=%d text=%.60s\n", r.first, r.second, pieces, out.c_str());
    // early stop
    int seen = 0;
    auto r2 = e.generate({{"user", "Again"}}, 16, 0.7f, [&](const std::string &) { return ++seen < 3; });
    printf("stopped after %d pieces (reported %d)\n", seen, r2.second);
    // Batching: 4 requests at once must all finish, sharing decode steps.
    auto one = [&] {
        auto t = std::chrono::steady_clock::now();
        auto rr = e.generate({{"user", "Tell me a story"}}, 32, 0.0f, [](const std::string &) { return true; });
        return std::make_pair(rr.second, std::chrono::duration<double>(std::chrono::steady_clock::now() - t).count());
    };
    auto solo = one();
    std::vector<std::thread> ts;
    std::atomic<int> total{0};
    auto t0 = std::chrono::steady_clock::now();
    for (int i = 0; i < 4; i++) ts.emplace_back([&] { total += one().first; });
    for (auto &t : ts) t.join();
    double par = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
    printf("solo: %d tokens in %.3fs (%.0f tok/s) | 4 at once: %d tokens in %.3fs (%.0f tok/s)\n", solo.first, solo.second,
           solo.first / solo.second, total.load(), par, total.load() / par);
    if (total.load() != 4 * solo.first) { puts("batched requests lost tokens"); return 1; }
    return (r.second > 0 && seen == 3) ? 0 : 1;
}
