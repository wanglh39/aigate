"""交互式挑选模型，建精简路由组。从 cc-switch.db 自动读密钥。

运行: uv run pick.py
它会列出所有中转站的模型，你输入序号选要的，自动组成新路由组。
"""
import json
import re
import sqlite3
from pathlib import Path

DB = Path.home() / ".cc-switch" / "cc-switch.db"
AIGATE_CFG = Path(__file__).resolve().parent / ".aigate" / "config.json"


def list_providers_with_models():
    con = sqlite3.connect(str(DB))
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
        models = catalog if catalog else ([fallback] if fallback else [])
        if base_url and models:
            out.append({"name": name, "base_url": base_url, "key": key, "models": models})
    con.close()
    return out


def main():
    providers = list_providers_with_models()

    current = {}
    if AIGATE_CFG.exists():
        cfg = json.loads(AIGATE_CFG.read_text(encoding="utf-8"))
        for rname, route in cfg.get("routes", {}).items():
            for m, up in route.get("models", {}).items():
                current[m] = up

    print("=" * 60)
    print("可选模型（按中转站分组），输入序号选择，逗号分隔")
    print("=" * 60)
    all_choices = []
    idx = 1
    for p in providers:
        print(f"\n【{p['name']}】  {p['base_url']}")
        for m in p["models"]:
            mark = " (已在配置)" if m in current else ""
            print(f"  {idx:>2}. {m}{mark}")
            all_choices.append((idx, m, p["name"], p["base_url"], p["key"]))
            idx += 1

    for m, up in current.items():
        if not any(c[1] == m for c in all_choices):
            print(f"\n【手动添加】  {up['baseURL']}")
            print(f"  {idx:>2}. {m} (已在配置)")
            all_choices.append((idx, m, "手动", up["baseURL"], up["apiKey"]))
            idx += 1

    print("\n" + "=" * 60)
    choice = input("选哪些序号（逗号分隔，如 1,4,8）: ").strip()
    picks = []
    for c in choice.split(","):
        c = c.strip()
        if c.isdigit():
            n = int(c)
            for item in all_choices:
                if item[0] == n:
                    picks.append(item)
                    break

    if not picks:
        print("没选任何模型，退出")
        return

    group_name = input("路由组名称（回车默认 my-pick）: ").strip() or "my-pick"

    cfg = json.loads(AIGATE_CFG.read_text(encoding="utf-8"))
    cfg.setdefault("routes", {})[group_name] = {"models": {}}
    for _, m, pname, base_url, key in picks:
        cfg["routes"][group_name]["models"][m] = {
            "baseURL": base_url,
            "apiKey": key or "sk-请手动填入",
            "upstreamModel": m,
        }
    cfg["active_route"] = group_name
    AIGATE_CFG.write_text(json.dumps(cfg, indent=2, ensure_ascii=False), encoding="utf-8")

    print(f"\n已创建路由组 '{group_name}'，含 {len(picks)} 个模型:")
    for _, m, pname, base_url, _ in picks:
        print(f"  - {m}  ({pname}, {base_url})")
    print(f"\n已切为激活组。启动: ./aigate.exe serve")
    print("以后想加减模型: uv run add_models.py")


if __name__ == "__main__":
    main()