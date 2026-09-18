#!/usr/bin/env python3
"""看 Waw provider 的 env_key 和 auth 配置方式"""
import json, sqlite3
from pathlib import Path
db = Path.home() / ".cc-switch" / "cc-switch.db"
con = sqlite3.connect(str(db))
cur = con.cursor()
cur.execute("SELECT name, settings_config FROM providers WHERE name LIKE 'Waw%' AND app_type='codex' LIMIT 1")
row = cur.fetchone()
if row:
    cfg = json.loads(row[1])
    print(f"name: {row[0]}")
    print(f"auth has key: {'OPENAI_API_KEY' in cfg.get('auth', {})}")
    print(f"config TOML:\n{cfg.get('config', '')}")
con.close()