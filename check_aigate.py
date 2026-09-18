#!/usr/bin/env python3
"""检查 cc-switch.db 里 aigate provider 的实际配置"""
import json, sqlite3
from pathlib import Path
db = Path.home() / ".cc-switch" / "cc-switch.db"
con = sqlite3.connect(str(db))
cur = con.cursor()
cur.execute("SELECT settings_config FROM providers WHERE name='aigate' AND app_type='codex'")
row = cur.fetchone()
if row:
    cfg = json.loads(row[0])
    print("auth has key:", "OPENAI_API_KEY" in cfg.get("auth", {}))
    print("config TOML:")
    print(cfg.get("config", ""))
else:
    print("aigate provider not found in cc-switch.db")
con.close()