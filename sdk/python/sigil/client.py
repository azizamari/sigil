"""Thin client for the sigil HTTP API.

Session creation is server to server. The viewer's identity comes from your
backend, never from the browser, so this client belongs on your server and its
API key must never reach a page.
"""

from __future__ import annotations

import json
import urllib.error
import urllib.request
from dataclasses import dataclass
from typing import Any


class SigilError(Exception):
    """An API call failed."""

    def __init__(self, status: int, message: str) -> None:
        super().__init__(f"sigil: {status}: {message}")
        self.status = status
        self.message = message


@dataclass(frozen=True)
class Asset:
    asset_id: str
    status: str
    duration: float = 0.0
    segments: int = 0
    watermarked: bool = False


@dataclass(frozen=True)
class Session:
    session_id: str
    playlist_url: str
    expires_at: str


class Client:
    def __init__(self, base_url: str, api_key: str, timeout: float = 10.0) -> None:
        if not base_url:
            raise ValueError("base_url is required")
        if not api_key:
            raise ValueError("api_key is required")
        self._base = base_url.rstrip("/")
        self._key = api_key
        self._timeout = timeout

    def create_asset(
        self,
        asset_id: str,
        segment_count: int,
        segment_duration_seconds: float,
        total_duration_seconds: float | None = None,
    ) -> Asset:
        body: dict[str, Any] = {
            "asset_id": asset_id,
            "segment_count": segment_count,
            "segment_duration_seconds": segment_duration_seconds,
        }
        if total_duration_seconds is not None:
            body["total_duration_seconds"] = total_duration_seconds
        data = self._request("POST", "/v1/assets", body)
        return Asset(asset_id=data["asset_id"], status=data["status"])

    def get_asset(self, asset_id: str) -> Asset:
        data = self._request("GET", f"/v1/assets/{asset_id}")
        return Asset(
            asset_id=data["asset_id"],
            status=data["status"],
            duration=data.get("duration", 0.0),
            segments=data.get("segments", 0),
            watermarked=data.get("watermarked", False),
        )

    def create_session(
        self,
        asset_id: str,
        overlay_text: str = "",
        ttl: int = 3600,
    ) -> Session:
        """Mint a viewing session.

        overlay_text is opaque to sigil: it is rendered and never stored or
        interpreted. Putting an email on screen exposes it during a legitimate
        screen share, which is your decision to make.
        """
        data = self._request(
            "POST",
            "/v1/sessions",
            {"asset_id": asset_id, "overlay_text": overlay_text, "ttl": ttl},
        )
        return Session(
            session_id=data["session_id"],
            playlist_url=data["playlist_url"],
            expires_at=data["expires_at"],
        )

    def _request(self, method: str, path: str, body: dict[str, Any] | None = None) -> dict[str, Any]:
        payload = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(
            f"{self._base}{path}",
            data=payload,
            method=method,
            headers={
                "Authorization": f"Bearer {self._key}",
                "Content-Type": "application/json",
                "Accept": "application/json",
            },
        )
        try:
            with urllib.request.urlopen(req, timeout=self._timeout) as resp:
                raw = resp.read()
        except urllib.error.HTTPError as exc:
            raise SigilError(exc.code, _error_message(exc.read())) from exc
        except urllib.error.URLError as exc:
            raise SigilError(0, str(exc.reason)) from exc

        if not raw:
            return {}
        return json.loads(raw)


def _error_message(raw: bytes) -> str:
    try:
        return json.loads(raw).get("error", raw.decode(errors="replace"))
    except (ValueError, AttributeError):
        return raw.decode(errors="replace")
