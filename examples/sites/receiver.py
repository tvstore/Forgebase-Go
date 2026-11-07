"""A tiny webhook receiver for examples/sites/ci.sh (M9.5). Standard library only.

    WEBHOOK_SECRET=... python3 -I receiver.py PORT RECORD_FILE

It listens on 127.0.0.1:PORT and verifies every POST with the recipe of docs/guide/webhooks.md:
HMAC-SHA256 over "<timestamp>.<raw body>", compared with hmac.compare_digest, and a timestamp
tolerance. A verified delivery is answered 200 and recorded as one JSON line (event, doctype, name,
delivery id) in RECORD_FILE; anything else is answered 400 and not recorded. A GET answers 200, so a
script can tell that the receiver is up. The secret comes from the environment and is never printed.

A test tool, not a production receiver: it keeps no state between runs and does no de-duplication
(every verified delivery is recorded, so a repeated delivery id shows up twice).
"""
import hashlib
import hmac
import json
import os
import sys
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

SECRET = os.environ["WEBHOOK_SECRET"].encode()
TOLERANCE = 5 * 60  # seconds
MAX_BODY = 1 << 20  # a test receiver: refuse anything bigger than 1 MiB


def verify(headers, body):
    """Return the parsed delivery, or raise ValueError. `body` is the raw bytes of the request."""
    timestamp = headers.get("X-Forgebase-Timestamp", "")
    signature = headers.get("X-Forgebase-Signature", "")
    if not timestamp.isdigit():
        raise ValueError("no timestamp")
    if abs(time.time() - int(timestamp)) > TOLERANCE:
        raise ValueError("timestamp out of tolerance")
    expected = "sha256=" + hmac.new(
        SECRET, timestamp.encode() + b"." + body, hashlib.sha256
    ).hexdigest()
    if not hmac.compare_digest(expected, signature):
        raise ValueError("bad signature")
    return json.loads(body)  # only after the signature is good


class Receiver(BaseHTTPRequestHandler):
    record_file = None

    def do_GET(self):
        self.reply(200)

    def do_POST(self):
        length = self.headers.get("Content-Length", "")
        if not length.isdigit() or int(length) > MAX_BODY:
            return self.reply(400)
        body = self.rfile.read(int(length))
        try:
            delivery = verify(self.headers, body)
            line = json.dumps(
                {
                    "event": delivery["event"],
                    "doctype": delivery["doctype"],
                    "name": delivery["name"],
                    "delivery": delivery["delivery"],
                }
            )
        except (ValueError, KeyError, TypeError):
            return self.reply(400)
        with open(self.record_file, "a", encoding="utf-8") as record:
            record.write(line + "\n")
        self.reply(200)

    def reply(self, status):
        self.send_response(status)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def log_message(self, *args):
        pass  # no access log: nothing of a request is printed


if __name__ == "__main__":
    Receiver.record_file = sys.argv[2]
    HTTPServer(("127.0.0.1", int(sys.argv[1])), Receiver).serve_forever()
