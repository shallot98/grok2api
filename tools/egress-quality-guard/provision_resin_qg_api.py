"""HTTP and credential boundaries for Resin quality-slot provisioning."""

from __future__ import annotations

import json
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


class ApiError(RuntimeError):
    pass


def read_env(path: Path) -> dict[str, str]:
    result: dict[str, str] = {}
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if line and not line.startswith("#") and "=" in line:
            key, value = line.split("=", 1)
            result[key.strip()] = value.strip()
    return result


class JsonClient:
    def __init__(self, base_url: str, token: str = "") -> None:
        self.base_url = base_url.rstrip("/")
        self.token = token

    def call(self, method: str, path: str, body: dict[str, Any] | None = None) -> Any:
        headers = {"Content-Type": "application/json", "User-Agent": "grok2api-qg-provision/1"}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        data = None if body is None else json.dumps(body).encode("utf-8")
        request = urllib.request.Request(self.base_url + path, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(request, timeout=180) as response:
                raw = response.read().decode("utf-8")
                payload = json.loads(raw) if raw.strip() else {}
        except urllib.error.HTTPError as exc:
            if method == "DELETE" and exc.code == 404:
                return {}
            detail = exc.read().decode("utf-8", "replace")
            raise ApiError(f"HTTP {exc.code} {method} {path}: {detail}") from exc
        return payload.get("data", payload)


def login(grok: JsonClient) -> str:
    value = grok.call("POST", "/api/admin/v1/auth/trusted-login", {})
    token = str((value.get("tokens") or {}).get("accessToken") or "")
    if not token:
        raise ApiError("trusted admin login did not return an access token")
    return token
