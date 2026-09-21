"""Public HTTP fixture: a fixed health rate limit, unchanged in every case."""
from collections import deque
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import signal
import threading
import time

STARTUP_SECONDS = 4
DRAIN_SECONDS = 4


class RateLimit:
    """Allow 60 accepted requests per rolling minute for each client address."""

    def __init__(self):
        self.clients = {}
        self.lock = threading.Lock()

    def allow(self, client, now):
        with self.lock:
            recent = self.clients.setdefault(client, deque())
            while recent and recent[0] <= now - 60:
                recent.popleft()
            if len(recent) >= 60:
                return False
            recent.append(now)
            return True


class Handler(BaseHTTPRequestHandler):
    limits = RateLimit()

    def do_GET(self):
        if self.path != "/health":
            self.send_error(404)
            return
        allowed = self.limits.allow(self.client_address[0], time.monotonic())
        body = json.dumps({"status": "ok" if allowed else "rate_limited"}).encode()
        self.send_response(200 if allowed else 429)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        if not allowed:
            self.send_header("Retry-After", "60")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args):
        pass


def main():
    time.sleep(STARTUP_SECONDS)
    server = ThreadingHTTPServer(("0.0.0.0", 8000), Handler)
    server.daemon_threads = True

    def terminate(_signal, _frame):
        # Keep the listener available while Kubernetes removes the endpoint.
        timer = threading.Timer(DRAIN_SECONDS, server.shutdown)
        timer.daemon = True
        timer.start()

    signal.signal(signal.SIGTERM, terminate)
    signal.signal(signal.SIGINT, terminate)
    try:
        server.serve_forever(poll_interval=0.1)
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
