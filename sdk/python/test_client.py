"""Tests run against a local stub, so no network and no credentials."""

import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from sigil import Client, SigilError


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def _reply(self, status, body):
        raw = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        if self.headers.get("Authorization") != "Bearer test-key":
            self._reply(401, {"error": "invalid api key"})
            return
        if self.path == "/v1/assets/lecture-01":
            self._reply(200, {
                "asset_id": "lecture-01", "status": "ready",
                "duration": 600.0, "segments": 800, "watermarked": True,
            })
            return
        self._reply(404, {"error": "asset not found"})

    def do_POST(self):
        if self.headers.get("Authorization") != "Bearer test-key":
            self._reply(401, {"error": "invalid api key"})
            return
        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length) or b"{}")
        if self.path == "/v1/assets":
            self._reply(201, {"asset_id": body["asset_id"], "status": "ready"})
        elif self.path == "/v1/sessions":
            self._reply(201, {
                "session_id": "ses_abc",
                "playlist_url": "https://sigil.example.com/v1/playlist/ses_abc?t=tok",
                "expires_at": "2026-01-01T00:00:00Z",
            })
        else:
            self._reply(404, {"error": "not found"})


@pytest.fixture
def server():
    srv = HTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=srv.serve_forever, daemon=True)
    thread.start()
    yield f"http://127.0.0.1:{srv.server_port}"
    srv.shutdown()


def test_requires_base_url_and_key():
    with pytest.raises(ValueError):
        Client("", "key")
    with pytest.raises(ValueError):
        Client("http://x", "")


def test_create_and_get_asset(server):
    c = Client(server, "test-key")
    created = c.create_asset("lecture-01", segment_count=800, segment_duration_seconds=0.75)
    assert created.asset_id == "lecture-01"

    got = c.get_asset("lecture-01")
    assert got.segments == 800
    assert got.watermarked is True


def test_create_session(server):
    c = Client(server, "test-key")
    s = c.create_session("lecture-01", overlay_text="viewer@example.com", ttl=3600)
    assert s.session_id == "ses_abc"
    assert s.playlist_url.startswith("https://")


def test_bad_key_raises(server):
    c = Client(server, "wrong")
    with pytest.raises(SigilError) as exc:
        c.get_asset("lecture-01")
    assert exc.value.status == 401


def test_missing_asset_raises(server):
    c = Client(server, "test-key")
    with pytest.raises(SigilError) as exc:
        c.get_asset("nope")
    assert exc.value.status == 404


def test_trailing_slash_in_base_url(server):
    c = Client(server + "/", "test-key")
    assert c.get_asset("lecture-01").asset_id == "lecture-01"
