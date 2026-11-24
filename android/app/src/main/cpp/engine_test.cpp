// Desktop test for the phone engine: engine_test <model.gguf>
#include <cassert>
#include <cstdio>
#include "infera_engine.h"

int main(int argc, char **argv) {
    // UTF-8 boundary handling
    assert(infera::utf8_complete_prefix("abc") == 3);
    assert(infera::utf8_complete_prefix(std::string("a\xE2\x82", 3)) == 1);       // incomplete euro sign
    assert(infera::utf8_complete_prefix(std::string("a\xE2\x82\xAC", 4)) == 4);   // complete
    if (argc < 2) { puts("utf8 ok"); return 0; }
    infera::Engine e;
    auto err = e.load(argv[1], 512, 2);
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
    return (r.second > 0 && seen == 3) ? 0 : 1;
}
