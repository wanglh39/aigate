#!/usr/bin/env python3
"""看所有 codex provider 的 requires_openai_auth 和 env_key 配置"""
import json, sqlite3
from pathlib import Path
db = Path.home() / ".cc-switch" / "cc-switch.db"
con = sqlite3.connect(str(db))
cur = con.cursor()
cur.execute("SELECT name, settings_config FROM providers WHERE app_type='codex'")
for name, sc in cur.fetchall():
    cfg = json.loads(sc)
    toml = cfg.get("config", "")
    has_auth = "OPENAI_API_KEY" in cfg.get("auth", {})
    import re
    m1 = re.search(r'requires_openai_auth\s*=\s*(\w+)', toml)
    m2 = re.search(r'env_key\s*=\s*"([^"]+)"', toml)
    m3 = re.search(r'wire_api\s*=\s*"([^"]+)"', toml)
    print(f"{name:20s}  auth_key={has_auth}  requires_openai_auth={m1.group(1) if m1 else 'N/A':5s}  env_key={m2.group(1) if m2 else 'N/A':20s}  wire_api={m3.group(1) if m3 else 'N/A'}")
con.close()