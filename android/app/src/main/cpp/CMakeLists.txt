cmake_minimum_required(VERSION 3.22)
project(infera_engine CXX C)
set(CMAKE_CXX_STANDARD 17)

# llama.cpp is pinned as a git submodule at the repository root.
set(LLAMA_DIR ${CMAKE_CURRENT_SOURCE_DIR}/../../../../../third_party/llama.cpp)
set(LLAMA_BUILD_COMMON OFF CACHE BOOL "" FORCE)
set(LLAMA_BUILD_TESTS OFF CACHE BOOL "" FORCE)
set(LLAMA_BUILD_EXAMPLES OFF CACHE BOOL "" FORCE)
set(LLAMA_BUILD_SERVER OFF CACHE BOOL "" FORCE)
set(LLAMA_BUILD_TOOLS OFF CACHE BOOL "" FORCE)
set(LLAMA_CURL OFF CACHE BOOL "" FORCE)
set(GGML_NATIVE OFF CACHE BOOL "" FORCE)
# Phones: use the dot-product and fp16 instructions every arm64 phone since ~2018 has
# (Cortex-A55/A75 and later). Without this, quantized models run several times slower.
if (ANDROID_ABI STREQUAL "arm64-v8a")
    set(GGML_CPU_ARM_ARCH "armv8.2-a+dotprod+fp16" CACHE STRING "" FORCE)
endif()
set(BUILD_SHARED_LIBS OFF CACHE BOOL "" FORCE)
add_subdirectory(${LLAMA_DIR} llama-build)

# whisper.cpp (speech to text), compiled against llama.cpp's ggml so both share one copy.
set(WHISPER_DIR ${CMAKE_CURRENT_SOURCE_DIR}/../../../../../third_party/whisper.cpp)
add_library(whisper_lite STATIC ${WHISPER_DIR}/src/whisper.cpp ${WHISPER_DIR}/src/aneforge/whisper-aneforge.cpp infera_whisper.cpp)
target_include_directories(whisper_lite PUBLIC ${WHISPER_DIR}/include ${WHISPER_DIR}/src ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(whisper_lite PUBLIC ggml)
set_target_properties(whisper_lite PROPERTIES POSITION_INDEPENDENT_CODE ON)
target_compile_definitions(whisper_lite PRIVATE WHISPER_VERSION="infera")

add_library(infera_engine SHARED infera_engine.cpp jni_bridge.cpp)
target_include_directories(infera_engine PRIVATE ${LLAMA_DIR}/include)
target_link_libraries(infera_engine PRIVATE llama whisper_lite)
if (ANDROID)
    target_link_libraries(infera_engine PRIVATE log android)
endif()

# Host test (desktop only): cmake -DINFERA_HOST_TEST=ON
option(INFERA_HOST_TEST "Build a desktop test binary" OFF)
if (INFERA_HOST_TEST)
    add_executable(engine_test engine_test.cpp infera_engine.cpp)
    target_include_directories(engine_test PRIVATE ${LLAMA_DIR}/include)
    target_link_libraries(engine_test PRIVATE llama)
    add_executable(speech_test speech_test.cpp)
    target_link_libraries(speech_test PRIVATE whisper_lite)
endif()
