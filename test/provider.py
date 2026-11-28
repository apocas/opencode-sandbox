#!/usr/bin/env python3
"""A stand-in for a provider's API and for an MCP server, for
test/integration.sh.

Usage: provider.py LOG PORTFILE

Listens on a free port of 127.0.0.1 (written to PORTFILE), appends each
request to LOG as a line of JSON (method, path, and the headers a secret
travels in), and answers the way an OpenAI-compatible API does; at /mcp, the
way an MCP server does over HTTP, with one tool.
"""
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG, PORTFILE = sys.argv[1], sys.argv[2]
KEY_HEADERS = ("authorization", "x-api-key", "x-goog-api-key", "api-key", "x-forwarded-for", "x-mcp-key")
TEXT = "the gateway works"


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def record(self):
        headers = {k.lower(): v for k, v in self.headers.items() if k.lower() in KEY_HEADERS}
        with open(LOG, "a") as f:
            f.write(json.dumps({"method": self.command, "path": self.path, "headers": headers}) + "\n")

    def answer(self, obj):
        data = json.dumps(obj).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def empty(self, code, **headers):
        self.send_response(code)
        for name, value in headers.items():
            self.send_header(name, value)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def mcp(self):
        return self.path.split("?")[0] == "/mcp"

    def do_GET(self):
        self.record()
        if self.mcp():
            self.empty(405, Allow="POST, DELETE")  # no stream of its own to offer
            return
        self.answer({"object": "list", "data": [{"id": "m1", "object": "model"}]})

    def do_DELETE(self):
        self.record()
        self.empty(200 if self.mcp() else 405)

    def answer_mcp(self, req):
        if "id" not in req:  # a notification
            self.empty(202)
            return
        result = {}
        if req.get("method") == "initialize":
            result = {"protocolVersion": req.get("params", {}).get("protocolVersion", "2025-03-26"),
                      "capabilities": {"tools": {}}, "serverInfo": {"name": "stand-in", "version": "1"}}
        elif req.get("method") == "tools/list":
            result = {"tools": [{"name": "echo", "description": "Says it back.",
                                 "inputSchema": {"type": "object", "properties": {}}}]}
        self.answer({"jsonrpc": "2.0", "id": req["id"], "result": result})

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        self.record()
        try:
            req = json.loads(body)
        except ValueError:
            req = {}
        if self.mcp():
            self.answer_mcp(req if isinstance(req, dict) else {})
            return
        base = {"id": "c1", "created": int(time.time()), "model": req.get("model", "m1")}
        usage = {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
        if not req.get("stream"):
            message = {"role": "assistant", "content": TEXT}
            self.answer({**base, "object": "chat.completion", "usage": usage,
                         "choices": [{"index": 0, "message": message, "finish_reason": "stop"}]})
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()

        def event(obj):
            data = ("data: " + (obj if isinstance(obj, str) else json.dumps(obj)) + "\n\n").encode()
            self.wfile.write(b"%x\r\n%s\r\n" % (len(data), data))
            self.wfile.flush()

        def delta(d, finish=None, **more):
            event({**base, "object": "chat.completion.chunk", **more,
                   "choices": [{"index": 0, "delta": d, "finish_reason": finish}]})

        delta({"role": "assistant", "content": ""})
        for word in TEXT.split(" "):
            delta({"content": word + " "})
            time.sleep(0.05)
        delta({}, "stop", usage=usage)
        event("[DONE]")
        self.wfile.write(b"0\r\n\r\n")


server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
with open(PORTFILE, "w") as f:
    f.write(str(server.server_address[1]))
server.serve_forever()
