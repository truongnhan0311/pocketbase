import hmac, hashlib, json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
SECRET = b"test-secret"
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers["Content-Length"]))
        ts = self.headers.get("X-ExtDB-Timestamp", "")
        sig = self.headers.get("X-ExtDB-Signature", "")
        ok = hmac.compare_digest(sig, hmac.new(SECRET, ts.encode() + b"." + body, hashlib.sha256).hexdigest())
        with open(sys.argv[1], "a") as f:
            f.write(json.dumps({"sig_ok": ok, "api_key": self.headers.get("api-key"), "event": self.headers.get("X-ExtDB-Event"), "body": json.loads(body)}) + "\n")
        self.send_response(200); self.end_headers(); self.wfile.write(b'{"ok":true}')
    def log_message(self, *a): pass
HTTPServer(("127.0.0.1", 3998), H).serve_forever()
