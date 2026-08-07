#!/usr/bin/env python3
"""Secret-safe smoke test for Resin stream-watch toggle on a deployed host.

Never prints credentials or tokens. Expects config at /root/grok2api/config.yaml
and admin API on 127.0.0.1:18081.
"""

from __future__ import annotations

import json
import urllib.error
import urllib.request
from pathlib import Path


def load_admin() -> tuple[str, str]:
    user = "admin"
    pwd = None

    # Prefer operator credentials file over bootstrap defaults.
    cred = Path("/root/grok2api/ADMIN_CREDENTIALS.txt")
    if cred.exists():
        for line in cred.read_text(encoding="utf-8", errors="ignore").splitlines():
            if ":" not in line:
                continue
            k, v = line.split(":", 1)
            kl = k.strip().lower()
            vv = v.strip().strip("\"'")
            if kl in ("username", "user") and vv:
                user = vv
            if kl in ("password", "pass") and vv:
                pwd = vv

    if not pwd:
        text = Path("/root/grok2api/config.yaml").read_text(encoding="utf-8", errors="ignore")
        for raw in text.splitlines():
            s = raw.strip()
            if not s or s.startswith("#") or ":" not in s:
                continue
            key, val = s.split(":", 1)
            key = key.strip().lower()
            val = val.strip().strip("\"'")
            if key in ("username", "adminusername", "admin_username") and val:
                user = val
            if key in ("password", "adminpassword", "admin_password") and val and not val.startswith("${"):
                pwd = val

    if not pwd:
        raise SystemExit("NO_PASSWORD_SOURCE")
    return user, pwd


def call(method: str, path: str, data=None, token: str | None = None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    body = None if data is None else json.dumps(data).encode()
    req = urllib.request.Request(
        f"http://127.0.0.1:18081{path}",
        data=body,
        headers=headers,
        method=method,
    )
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            raw = resp.read().decode()
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as e:
        # Never print response body (may contain details); only status/code.
        code = getattr(e, "code", "?")
        err = None
        try:
            payload = json.loads(e.read().decode())
            if isinstance(payload, dict):
                err = ((payload.get("error") or {}) if isinstance(payload.get("error"), dict) else {}).get("code")
                if not err:
                    err = payload.get("code")
        except Exception:  # noqa: BLE001
            err = None
        raise RuntimeError(f"HTTP {code} code={err or 'unknown'}") from None


def extract_token(obj):
    if not isinstance(obj, dict):
        return None
    # direct
    for k in ("token", "accessToken", "access_token"):
        if isinstance(obj.get(k), str) and obj.get(k):
            return obj[k]
    # tokens: { accessToken }
    tokens = obj.get("tokens")
    if isinstance(tokens, dict):
        for k in ("token", "accessToken", "access_token"):
            if isinstance(tokens.get(k), str) and tokens.get(k):
                return tokens[k]
    # data envelope
    data = obj.get("data")
    if isinstance(data, dict):
        got = extract_token(data)
        if got:
            return got
    return None


def main() -> None:
    import urllib.error

    user, pwd = load_admin()
    print("HAS_USER", bool(user), "HAS_PWD", bool(pwd), "USER_LEN", len(user), "PWD_LEN", len(pwd))
    login = None
    last = None
    for p in ("/api/admin/v1/auth/login",):
        try:
            login = call("POST", p, {"username": user, "password": pwd})
            print("LOGIN_PATH", p)
            break
        except Exception as e:  # noqa: BLE001
            last = str(e)
    if login is None:
        raise SystemExit(f"LOGIN_FAILED {last}")

    token = extract_token(login)
    if not token:
        top = sorted(login.keys())
        data_keys = sorted((login.get("data") or {}).keys()) if isinstance(login.get("data"), dict) else []
        raise SystemExit(f"TOKEN_MISSING keys={top} data_keys={data_keys}")

    st = call("GET", "/api/admin/v1/resin-quality-guard", token=token)
    payload = st.get("data", st)
    cfg = payload.get("config") or {}
    print(
        "STATUS available=",
        payload.get("available"),
        "enabled=",
        payload.get("enabled"),
        "watch=",
        cfg.get("streamWatchEnabled"),
        "max=",
        cfg.get("streamMaxAttempts"),
    )

    off = call(
        "PUT",
        "/api/admin/v1/resin-quality-guard/config",
        {"streamWatchEnabled": False},
        token=token,
    )
    offp = off.get("data", off)
    off_watch = (offp.get("config") or offp).get("streamWatchEnabled") if isinstance(offp, dict) else None
    print("OFF watch=", off_watch)

    after_off = (call("GET", "/api/admin/v1/resin-quality-guard", token=token).get("data", {}) or {}).get("config", {}).get(
        "streamWatchEnabled"
    )
    # handle both envelope shapes
    st2 = call("GET", "/api/admin/v1/resin-quality-guard", token=token)
    p2 = st2.get("data", st2)
    after_off = (p2.get("config") or {}).get("streamWatchEnabled")
    print("AFTER_OFF watch=", after_off)
    if after_off is not False:
        raise SystemExit(5)

    on = call(
        "PUT",
        "/api/admin/v1/resin-quality-guard/config",
        {"streamWatchEnabled": True},
        token=token,
    )
    onp = on.get("data", on)
    on_watch = (onp.get("config") or onp).get("streamWatchEnabled") if isinstance(onp, dict) else None
    print("ON watch=", on_watch)

    st3 = call("GET", "/api/admin/v1/resin-quality-guard", token=token)
    p3 = st3.get("data", st3)
    after_on = (p3.get("config") or {}).get("streamWatchEnabled")
    print("AFTER_ON watch=", after_on)
    if after_on is not True:
        raise SystemExit(6)

    state = Path("/root/grok2api/data/resin-quality-guard-state.json")
    if state.exists():
        sj = json.loads(state.read_text())
        print("STATE_OVERRIDE", sj.get("streamWatchOverride"))
    print("TOGGLE_VERIFIED")


if __name__ == "__main__":
    main()
