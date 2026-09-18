"""手动添加模型到 aigate 配置。从 cc-switch.db 自动读密钥，不用手填。

用法:
  uv run add_models.py --provider Waw-gptmax --models gpt-4o,gpt-4o-mini,gpt-5
  uv run add_models.py                    # 交互式：列出中转站让你选
"""
import json
import re
import sqlite3
from pathlib import Path

DB = Path.home() / ".cc-switch" / "cc-switch.db"
AIGATE_CFG = Path(__file__).resolve().parent / ".aigate" / "config.json"


def list_providers():
    con = sqlite3.connect(str(DB))
    cur = con.cursor()
    cur.execute("SELECT name, settings_config FROM providers WHERE app_type='codex'")
    out = []
    for name, sc in cur.fetchall():
        cfg = json.loads(sc)
        key = cfg.get("auth", {}).get("OPENAI_API_KEY", "") or ""
        toml = cfg.get("config", "")
        m = re.search(r'base_url\s*=\s*"([^"]+)"', toml)
        base_url = m.group(1) if m else ""
        if base_url:
            out.append((name, base_url, key))
    con.close()
    return out


def add(provider_name, models, route_name="all-models"):
    providers = list_providers()
    match = [p for p in providers if p[0] == provider_name]
    if not match:
        print(f"找不到 provider: {provider_name}")
        print("可用:", [p[0] for p in providers])
        return
    name, base_url, key = match[0]
    if not key:
        print(f"{name} 在 cc-switch 里没存 key，无法自动添加")
        return

    cfg = json.loads(AIGATE_CFG.read_text(encoding="utf-8"))
    route = cfg.setdefault("routes", {}).setdefault(route_name, {"models": {}})
    route.setdefault("models", {})
    added, skipped = [], []
    for m in models:
        if m in route["models"]:
            skipped.append(m)
            continue
        route["models"][m] = {"baseURL": base_url, "apiKey": key, "upstreamModel": m}
        added.append(m)
    AIGATE_CFG.write_text(json.dumps(cfg, indent=2, ensure_ascii=False), encoding="utf-8")
    print(f"中转站: {name}  ({base_url})")
    if added:
        print(f"已添加 {len(added)} 个: {added}")
    if skipped:
        print(f"跳过已存在 {len(skipped)}: {skipped}")
    print(f"路由组 '{route_name}' 现有 {len(route['models'])} 个模型")


def main():
    import sys
    args = sys.argv[1:]
    if "--provider" in args and "--models" in args:
        p = args[args.index("--provider") + 1]
        ms = [x.strip() for x in args[args.index("--models") + 1].split(",") if x.strip()]
        add(p, ms)
        return

    providers = list_providers()
    print("可用中转站:")
    for i, (n, u, k) in enumerate(providers):
        print(f"  {i + 1}. {n}  ({u})  {'有key' if k else '无key'}")
    choice = input("\n选哪个（序号）: ").strip()
    idx = int(choice) - 1
    name, base_url, key = providers[idx]
    ms = input("模型名（逗号分隔）: ").strip()
    models = [x.strip() for x in ms.split(",") if x.strip()]
    add(name, models)


if __name__ == "__main__":
    main()