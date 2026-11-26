#!/usr/bin/env bash
# Infera: full local test on one computer (macOS or Linux).
# Builds the server and the node from source, starts everything on localhost,
# creates test accounts, pairs this computer, and sends a test request.
#
#   ./scripts/local-test.sh            # uses Ollama if it's running, else a stand-in engine
#   ./scripts/local-test.sh --mock     # always use the stand-in engine (no model download)
#   ./scripts/local-test.sh --stop     # stop everything
#
# Needs: Go 1.24+, Rust (cargo), Python 3, curl.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$ROOT/.local-test"
PORT=8080
export INFERA_HOME="$WORK/node-home"

stop() {
  for f in "$WORK"/*.pid; do [ -f "$f" ] && kill "$(cat "$f")" 2>/dev/null || true; rm -f "$f"; done
  echo "Stopped."
}
if [ "${1:-}" = "--stop" ]; then stop; exit 0; fi

need() { command -v "$1" >/dev/null || { echo "Missing $1. $2"; exit 1; }; }
need go "Install from https://go.dev/dl/"
need cargo "Install from https://rustup.rs"
need python3 "Install Python 3"
need curl ""

mkdir -p "$WORK"
stop >/dev/null 2>&1 || true
json() { python3 -c "import sys,json;print(json.load(sys.stdin)$1)"; }

echo "1/6 Building the server …"
(cd "$ROOT/control-plane" && go build -o "$WORK/infera-server" .)
echo "2/6 Building the node (first build takes a few minutes) …"
(cd "$ROOT/agent" && cargo build --release -q)
NODE="$ROOT/agent/target/release/infera"

echo "3/6 Starting the server on http://localhost:$PORT …"
rm -f "$WORK"/infera.db*
INFERA_DB="$WORK/infera.db" INFERA_ADDR=":$PORT" INFERA_ADMIN_TOKEN=local-admin INFERA_INVITE_CODE= \
  INFERA_CANARY_MINUTES=5 "$WORK/infera-server" > "$WORK/server.log" 2>&1 &
echo $! > "$WORK/server.pid"
for _ in $(seq 1 30); do curl -sf "localhost:$PORT/healthz" >/dev/null && break; sleep 0.3; done

echo "4/6 Creating test accounts …"
OWN=$(curl -s "localhost:$PORT/api/signup" -d '{"email":"owner@local.test","kind":"owner"}' | json '["api_key"]')
CUST=$(curl -s "localhost:$PORT/api/signup" -d '{"email":"dev@local.test","kind":"customer"}' | json '["api_key"]')
CODE=$(curl -s -X POST "localhost:$PORT/api/pair-code" -H "Authorization: Bearer $OWN" | json '["code"]')

echo "5/6 Pairing this computer and choosing an engine …"
rm -rf "$INFERA_HOME"
"$NODE" pair "$CODE" --server "http://localhost:$PORT" --name "$(hostname)" >/dev/null
MODEL=mock-1b
if [ "${1:-}" != "--mock" ] && curl -sf localhost:11434/api/tags >/dev/null 2>&1; then
  TAG=$(curl -s localhost:11434/api/tags | python3 -c "import sys,json;m=json.load(sys.stdin)['models'];c=[x['name'] for x in m if 'embed' not in x['name'] and 'minilm' not in x['name']];print(c[0] if c else '')")
fi
# Speech and video: the media server in stand-in mode (the real one needs a GPU and PyTorch).
python3 "$ROOT/agent/media/infera_media.py" --mock --port 18179 --tts-id kokoro-82m --video-id wan2.1-t2v-1.3b > "$WORK/media.log" 2>&1 &
echo $! > "$WORK/media.pid"
MEDIA="--media-url http://127.0.0.1:18179 --tts-model kokoro-82m --video-model wan2.1-t2v-1.3b"
if [ -n "${TAG:-}" ]; then
  MODEL=$(echo "$TAG" | tr ':' '-')
  echo "   Using Ollama model $TAG as \"$MODEL\""
  ETAG=$(curl -s localhost:11434/api/tags | python3 -c "import sys,json;m=[x['name'] for x in json.load(sys.stdin)['models'] if 'embed' in x['name'] or 'minilm' in x['name']];print(m[0] if m else '')")
  EMBED=""; [ -n "$ETAG" ] && EMBED=$(echo "$ETAG" | tr ':' '-')
  "$NODE" engine --url http://127.0.0.1:11434 --model "$MODEL" --engine-model "$TAG" --params-b "${INFERA_PARAMS_B:-2}" --embed-model "$EMBED" --embed-engine-model "$ETAG" $MEDIA >/dev/null
else
  echo "   Using the stand-in engine (fake answers). Install Ollama and pull a model to test a real one."
  python3 "$ROOT/tools/mock_engine.py" 18090 0.03 > "$WORK/mock.log" 2>&1 &
  echo $! > "$WORK/mock.pid"
  sleep 0.5
  EMBED=mock-embed
  STT=mock-whisper
  "$NODE" engine --url http://127.0.0.1:18090 --model "$MODEL" --params-b 1 --embed-model "$EMBED" --stt-url http://127.0.0.1:18090 --stt-model "$STT" $MEDIA >/dev/null
fi
# Local testing: don't wait for 5 minutes of keyboard idle.
python3 - "$INFERA_HOME/config.toml" <<'PY'
import sys,re
p=sys.argv[1]; s=open(p).read()
s=re.sub(r"(?m)^idle_minutes = .*$", "idle_minutes = 0.0", s)
s=re.sub(r"(?m)^max_other_cpu = .*$", "max_other_cpu = 95.0", s)
s=re.sub(r"(?m)^require_ac = .*$", "require_ac = false", s)
open(p,"w").write(s)
PY
"$NODE" run > "$WORK/node.log" 2>&1 &
echo $! > "$WORK/node.pid"
for _ in $(seq 1 120); do grep -q "Connected to" "$WORK/node.log" 2>/dev/null && break; sleep 1; done
sleep 3

echo "6/6 Sending a test request through your computer …"
REPLY=$(curl -s "localhost:$PORT/v1/chat/completions" -H "Authorization: Bearer $CUST" \
  -d "{\"model\":\"$MODEL\",\"max_tokens\":400,\"messages\":[{\"role\":\"user\",\"content\":\"Say hello in one sentence.\"}]}")
if ! echo "$REPLY" | python3 -c "import sys,json;d=json.load(sys.stdin);m=d['choices'][0]['message'];c=(m.get('content') or '').strip();r=(m.get('reasoning_content') or '').strip();assert c or r;print('   Reply:', (c or '[reasoning only] '+r)[:200])"; then
  echo "TEST FAILED: $REPLY"; echo "--- node log"; tail -20 "$WORK/node.log"; echo "--- server log"; tail -20 "$WORK/server.log"; exit 1
fi
echo "   Streaming check …"
STREAM=$(curl -sN "localhost:$PORT/v1/chat/completions" -H "Authorization: Bearer $CUST" \
  -d "{\"model\":\"$MODEL\",\"stream\":true,\"max_tokens\":20,\"messages\":[{\"role\":\"user\",\"content\":\"Count to five.\"}]}")
echo "$STREAM" | grep -q '"finish_reason":"stop"' || { echo "TEST FAILED (stream): $STREAM"; exit 1; }
echo "   Earnings check …"
curl -s "localhost:$PORT/api/owner" -H "Authorization: Bearer $OWN" | python3 -c "import sys,json;d=json.load(sys.stdin);h=d['balance']['held'];assert h>0,d;print('   Owner earned (held):',h,'micro-USD')" || { echo "TEST FAILED (earnings)"; exit 1; }
if [ -n "${EMBED:-}" ]; then
  echo "   Embeddings check ($EMBED) …"
  curl -s "localhost:$PORT/v1/embeddings" -H "Authorization: Bearer $CUST" -d "{\"model\":\"$EMBED\",\"input\":[\"idle laptops\",\"serve AI\"]}" \
    | python3 -c "import sys,json;d=json.load(sys.stdin);assert len(d['data'])==2 and len(d['data'][0]['embedding'])>0,d;print('   2 vectors of', len(d['data'][0]['embedding']), 'dims')" || { echo "TEST FAILED (embeddings)"; exit 1; }
fi
if [ -n "${STT:-}" ]; then
  echo "   Transcription check ($STT) …"
  head -c 64000 /dev/urandom > "$WORK/clip.wav"
  curl -s "localhost:$PORT/v1/audio/transcriptions" -H "Authorization: Bearer $CUST" -F "model=$STT" -F "file=@$WORK/clip.wav" \
    | python3 -c "import sys,json;d=json.load(sys.stdin);assert 'Hello' in d.get('text',''),d;print('   Text:', d['text'])" || { echo "TEST FAILED (transcription)"; exit 1; }
fi
STRICT=$([ -n "${TAG:-}" ] && echo 0 || echo 1)  # real small models may answer oddly; the stand-in must be exact
echo "   Code autocomplete check (/v1/completions with suffix) …"
curl -s "localhost:$PORT/v1/completions" -H "Authorization: Bearer $CUST" \
  -d "{\"model\":\"$MODEL\",\"prompt\":\"def add(a, b):\\n    \",\"suffix\":\"\\n\\nprint(add(1, 2))\",\"max_tokens\":16}" \
  | python3 -c "import sys,json;d=json.load(sys.stdin);t=d['choices'][0]['text'];assert d['object']=='text_completion' and d['usage']['completion_tokens']>0 and (t.strip() or $STRICT==0),d;print('   Completion:', repr(t.strip()))" || { echo "TEST FAILED (completions)"; exit 1; }
CSTREAM=$(curl -sN "localhost:$PORT/v1/completions" -H "Authorization: Bearer $CUST" -d "{\"model\":\"$MODEL\",\"prompt\":\"Hi\",\"stream\":true,\"max_tokens\":8}")
echo "$CSTREAM" | grep -q '"object":"text_completion"' || { echo "TEST FAILED (streamed completions): $CSTREAM"; exit 1; }
echo "   Tool calling check (coding agents) …"
TOOLS='"tools":[{"type":"function","function":{"name":"read_file","description":"Read a file from the project","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}]'
TBODY="{\"model\":\"$MODEL\",\"max_tokens\":200,$TOOLS,\"messages\":[{\"role\":\"user\",\"content\":\"Use the read_file tool to open main.py.\"}]}"
curl -s "localhost:$PORT/v1/chat/completions" -H "Authorization: Bearer $CUST" -d "$TBODY" | python3 -c "
import sys,json;d=json.load(sys.stdin);c=d['choices'][0];m=c['message'];tc=m.get('tool_calls') or []
if tc:
    f=tc[0]['function'];json.loads(f['arguments']);assert c['finish_reason']=='tool_calls' and tc[0]['id'],d;print('   Tool call:', f['name'], f['arguments'])
else:
    assert $STRICT==0 and m.get('content'),d;print('   (model answered in text instead of calling the tool)')
" || { echo "TEST FAILED (tool calling)"; exit 1; }
TSTREAM=$(curl -sN "localhost:$PORT/v1/chat/completions" -H "Authorization: Bearer $CUST" -d "${TBODY%\}},\"stream\":true}")
[ "$STRICT" = 0 ] || echo "$TSTREAM" | grep -q '"tool_calls"' || { echo "TEST FAILED (streamed tool calls): $TSTREAM"; exit 1; }
echo "   Text to speech check (/v1/audio/speech) …"
SP=$(curl -s -o "$WORK/speech.out" -w "%{http_code} %{content_type}" "localhost:$PORT/v1/audio/speech" -H "Authorization: Bearer $CUST" \
  -d '{"model":"kokoro-82m","input":"Hello from Infera, running on a computer near you.","voice":"alloy","response_format":"wav"}')
[ "${SP%% *}" = 200 ] && head -c 4 "$WORK/speech.out" | grep -q RIFF || { echo "TEST FAILED (speech): $SP $(head -c 300 "$WORK/speech.out")"; exit 1; }
echo "   Audio: $(wc -c < "$WORK/speech.out") bytes (${SP#* })"
echo "   Text to video check (/v1/videos) …"
VID=$(curl -s "localhost:$PORT/v1/videos" -H "Authorization: Bearer $CUST" -d '{"model":"wan2.1-t2v-1.3b","prompt":"A red kite over a green hill","seconds":"2"}' \
  | python3 -c "import sys,json;d=json.load(sys.stdin);assert d['object']=='video' and d['status'] in ('queued','in_progress'),d;print(d['id'])") || { echo "TEST FAILED (video create)"; exit 1; }
for _ in $(seq 1 30); do
  VS=$(curl -s "localhost:$PORT/v1/videos/$VID" -H "Authorization: Bearer $CUST" | python3 -c "import sys,json;print(json.load(sys.stdin)['status'])")
  [ "$VS" = completed ] || [ "$VS" = failed ] && break; sleep 1
done
[ "$VS" = completed ] || { echo "TEST FAILED (video status $VS)"; curl -s "localhost:$PORT/v1/videos/$VID" -H "Authorization: Bearer $CUST"; exit 1; }
curl -s "localhost:$PORT/v1/videos/$VID/content" -H "Authorization: Bearer $CUST" -o "$WORK/video.mp4"
grep -q ftyp "$WORK/video.mp4" || { echo "TEST FAILED (video content)"; exit 1; }
echo "   Video: $VID completed, $(wc -c < "$WORK/video.mp4") bytes"
echo "   Batch queue check …"
BODY="{\"requests\":[{\"model\":\"$MODEL\",\"max_tokens\":30,\"messages\":[{\"role\":\"user\",\"content\":\"Name a colour.\"}]},{\"model\":\"$MODEL\",\"max_tokens\":30,\"messages\":[{\"role\":\"user\",\"content\":\"Name a fruit.\"}]}"
[ -n "${EMBED:-}" ] && BODY="$BODY,{\"model\":\"$EMBED\",\"input\":\"batched text\"}"
BODY="$BODY]}"
IDS=$(curl -s "localhost:$PORT/v1/batch" -H "Authorization: Bearer $CUST" -d "$BODY" | python3 -c "import sys,json;print(' '.join(json.load(sys.stdin)['ids']))")
for _ in $(seq 1 60); do
  LEFT=0; for id in $IDS; do S=$(curl -s "localhost:$PORT/v1/batch/$id" -H "Authorization: Bearer $CUST" | python3 -c "import sys,json;print(json.load(sys.stdin)['status'])"); [ "$S" = "done" ] || LEFT=$((LEFT+1)); [ "$S" = "failed" ] && { echo "TEST FAILED (batch job $id failed)"; exit 1; }; done
  [ $LEFT -eq 0 ] && break; sleep 2
done
[ $LEFT -eq 0 ] || { echo "TEST FAILED (batch jobs still waiting)"; exit 1; }
echo "   $(echo $IDS | wc -w) batch jobs done"
echo "ALL LOCAL TESTS PASSED"

cat <<INFO

Everything is running on this computer:
  Dashboard        http://localhost:$PORT      (Contribute tab: paste the contributor key; Developers tab: paste the API key)
  Node page        http://127.0.0.1:7777       (offer cards, online toggle, safe stop)
  Contributor key  $OWN
  Developer key    $CUST
  Admin token      local-admin
  Model name       $MODEL
  Logs             $WORK/*.log

Try:
  - Developers tab > Try it          send prompts and watch them run on your node
  - Node page > Ask for every job    then send a prompt and accept the offer card
  - Node page > Stop now mid-job     see the penalty appear on the dashboard

Stop everything:  ./scripts/local-test.sh --stop
INFO
