"""A stand-in OpenAI-compatible engine for testing Infera without a model.
Streams a fixed answer word by word. Usage: python3 tools/mock_engine.py [port] [delay_s]"""
import json, sys, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 18090
DELAY = float(sys.argv[2]) if len(sys.argv) > 2 else 0.02
ANSWER = ("Idle laptops and desktops have spare compute most of the day, so they can run small "
          "language models and answer requests while their owners are away. " * 3).split(" ")

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        body = json.dumps({"object": "list", "data": [{"id": "mock"}]}).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers(); self.wfile.write(body)
    def do_POST(self):
        req = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        n = min(int(req.get("max_tokens") or 64), len(ANSWER))
        self.send_response(200); self.send_header("Content-Type", "text/event-stream"); self.end_headers()
        for w in ANSWER[:n]:
            ch = {"choices": [{"index": 0, "delta": {"content": w + " "}}]}
            self.wfile.write(f"data: {json.dumps(ch)}\n\n".encode()); self.wfile.flush(); time.sleep(DELAY)
        u = {"choices": [], "usage": {"prompt_tokens": 20, "completion_tokens": n}}
        self.wfile.write(f"data: {json.dumps(u)}\n\ndata: [DONE]\n\n".encode())

ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()
