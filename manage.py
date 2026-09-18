#!/usr/bin/env python3
"""aigate 管理 UI — 浏览器打开 http://127.0.0.1:8318/ 选模型、建路由组。

从 cc-switch.db 读中转站+模型，读写 .aigate/config.json。
key 自动从 db 读，不显示在网页上。纯标准库零依赖。
"""
import json
import os
import re
import sqlite3
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

AIGATE_CFG = Path(__file__).resolve().parent / ".aigate" / "config.json"
CC_DB = Path.home() / ".cc-switch" / "cc-switch.db"
UI_FILE = Path(__file__).resolve().parent / "ui.html"
PORT = 8318


def read_providers():
    con = sqlite3.connect(str(CC_DB))
    cur = con.cursor()
    cur.execute("SELECT name, settings_config, website_url FROM providers WHERE app_type='codex'")
    out = []
    for name, sc, website in cur.fetchall():
        cfg = json.loads(sc)
        key = cfg.get("auth", {}).get("OPENAI_API_KEY", "") or ""
        toml = cfg.get("config", "")
        m = re.search(r'base_url\s*=\s*"([^"]+)"', toml)
        base_url = m.group(1) if m else (website or "")
        catalog = [x["model"] for x in cfg.get("modelCatalog", {}).get("models", [])]
        m3 = re.search(r'^\s*model\s*=\s*"([^"]+)"', toml, re.M)
        fallback = m3.group(1) if m3 else ""
        if not key:
            m2 = re.search(r'env_key\s*=\s*"([^"]+)"', toml)
            if m2:
                key = os.environ.get(m2.group(1), "") or ""
        models = catalog if catalog else ([fallback] if fallback else [])
        if base_url and models:
            out.append({"name": name, "base_url": base_url, "has_key": bool(key), "models": models})
    con.close()
    return out


def get_provider(name):
    con = sqlite3.connect(str(CC_DB))
    cur = con.cursor()
    cur.execute("SELECT settings_config, website_url FROM providers WHERE name=? AND app_type='codex'", (name,))
    row = cur.fetchone()
    con.close()
    if not row:
        return None
    sc, website = row
    cfg = json.loads(sc)
    key = cfg.get("auth", {}).get("OPENAI_API_KEY", "") or ""
    toml = cfg.get("config", "")
    m = re.search(r'base_url\s*=\s*"([^"]+)"', toml)
    base_url = m.group(1) if m else (website or "")
    if not key:
        m2 = re.search(r'env_key\s*=\s*"([^"]+)"', toml)
        if m2:
            key = os.environ.get(m2.group(1), "") or ""
    return {"base_url": base_url, "key": key}


def load_aigate():
    if AIGATE_CFG.exists():
        return json.loads(AIGATE_CFG.read_text(encoding="utf-8"))
    return {"server": {"host": "127.0.0.1", "port": 8317}, "api_key": "", "auth": True, "routes": {}, "active_route": None}


def save_aigate(cfg):
    AIGATE_CFG.parent.mkdir(parents=True, exist_ok=True)
    AIGATE_CFG.write_text(json.dumps(cfg, indent=2, ensure_ascii=False), encoding="utf-8")


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

    def _json(self, status, obj):
        body = json.dumps(obj, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = self.path.split("?")[0]
        if path in ("/", "/index.html"):
            if UI_FILE.exists():
                data = UI_FILE.read_bytes()
                self.send_response(200)
                self.send_header("Content-Type", "text/html; charset=utf-8")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
            else:
                self._json(404, {"error": "ui.html not found"})
            return
        if path == "/api/providers":
            self._json(200, read_providers())
            return
        if path == "/api/routes":
            cfg = load_aigate()
            routes = {}
            for name, route in cfg.get("routes", {}).items():
                routes[name] = list(route.get("models", {}).keys())
            self._json(200, {"routes": routes, "active": cfg.get("active_route"), "api_key": cfg.get("api_key", "")})
            return
        self._json(404, {"error": "not found"})

    def do_POST(self):
        path = self.path.split("?")[0]
        length = int(self.headers.get("Content-Length", 0) or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            body = json.loads(raw)
        except Exception:
            self._json(400, {"error": "invalid json"})
            return

        if path == "/api/save":
            group = body.get("group", "my-pick")
            picks = body.get("picks", [])
            cfg = load_aigate()
            cfg.setdefault("routes", {})[group] = {"models": {}}
            added = []
            for pick in picks:
                model = pick.get("model", "")
                provider = pick.get("provider", "")
                p = get_provider(provider)
                if not p or not model:
                    continue
                cfg["routes"][group]["models"][model] = {
                    "baseURL": p["base_url"],
                    "apiKey": p["key"] or "sk-请手动填入",
                    "upstreamModel": model,
                }
                added.append(model)
            cfg["active_route"] = group
            save_aigate(cfg)
            self._json(200, {"ok": True, "group": group, "added": added, "count": len(added)})
            return

        if path == "/api/switch":
            group = body.get("group", "")
            cfg = load_aigate()
            if group not in cfg.get("routes", {}):
                self._json(404, {"error": "group not found"})
                return
            cfg["active_route"] = group
            save_aigate(cfg)
            self._json(200, {"ok": True, "active": group})
            return

        if path == "/api/delete":
            group = body.get("group", "")
            cfg = load_aigate()
            if group in cfg.get("routes", {}):
                del cfg["routes"][group]
                if cfg.get("active_route") == group:
                    cfg["active_route"] = ""
                save_aigate(cfg)
                self._json(200, {"ok": True})
            else:
                self._json(404, {"error": "group not found"})
            return

        if path == "/api/register-ccswitch":
            cfg = load_aigate()
            api_key = cfg.get("api_key", "")
            active = cfg.get("active_route", "")
            if not active or active not in cfg.get("routes", {}):
                self._json(400, {"error": "没有激活的路由组"})
                return
            models = list(cfg["routes"][active].get("models", {}).keys())
            if not models:
                self._json(400, {"error": "激活路由组没有模型"})
                return
            first_model = models[0]
            server = cfg.get("server", {})
            host = server.get("host", "127.0.0.1")
            port = server.get("port", 8317)
            base_url = f"http://{host}:{port}"
            toml = (
                f'model_provider = "custom"\n'
                f'model = "{first_model}"\n'
                f'[model_providers.custom]\n'
                f'name = "aigate"\n'
                f'base_url = "{base_url}"\n'
                f'wire_api = "chat"\n'
                f'requires_openai_auth = false\n'
                f'env_key = "AIGATE_API_KEY"\n'
            )
            settings_config = json.dumps({
                "auth": {"OPENAI_API_KEY": api_key},
                "config": toml,
                "modelCatalog": {"models": [{"model": m, "displayName": m} for m in models]},
            }, ensure_ascii=False)
            con = sqlite3.connect(str(CC_DB))
            cur = con.cursor()
            cur.execute("SELECT id FROM providers WHERE name='aigate' AND app_type='codex'")
            row = cur.fetchone()
            pid = row[0] if row else str(uuid.uuid4())
            now = int(time.time() * 1000)
            cur.execute("""
                INSERT OR REPLACE INTO providers
                (id, app_type, name, settings_config, website_url, category, created_at, meta, is_current, in_failover_queue, cost_multiplier)
                VALUES (?, 'codex', 'aigate', ?, ?, 'local', ?, '{}', 0, 0, '1.0')
            """, (pid, settings_config, base_url, now))
            con.commit()
            con.close()
            self._json(200, {"ok": True, "provider": "aigate", "models": models, "base_url": base_url})
            return

        self._json(404, {"error": "not found"})


def main():
    print(f"aigate 管理 UI: http://127.0.0.1:{PORT}/")
    print("浏览器打开上面的地址，勾选模型、建路由组")
    print("Ctrl+C 退出")
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\n退出")
    finally:
        server.server_close()


if __name__ == "__main__":
    main()