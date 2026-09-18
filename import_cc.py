"""从 cc-switch 数据库一键导入中转站到 aigate 配置。

读取 ~/.cc-switch/cc-switch.db 里所有 codex provider 的 base_url / api_key，
对有 modelCatalog 的直接用，没有的调上游 /v1/models 自动拉取模型列表。
密钥直接从数据库拷到配置文件，不打印到终端。
"""
import json
import os
import re
import sqlite3
import urllib.error
import urllib.request
from pathlib import Path

DB = Path.home() / ".cc-switch" / "cc-switch.db"
AIGATE_DIR = Path(__file__).resolve().parent / ".aigate"
AIGATE_CFG = AIGATE_DIR / "config.json"


def extract_providers():
    con = sqlite3.connect(str(DB))
    cur = con.cursor()
    cur.execute("SELECT name, settings_config, website_url FROM providers WHERE app_type='codex'")
    out = []
    for name, sc, website in cur.fetchall():
        if not sc:
            continue
        cfg = json.loads(sc)
        auth = cfg.get("auth", {})
        key = auth.get("OPENAI_API_KEY", "") or ""
        toml = cfg.get("config", "")
        m = re.search(r'base_url\s*=\s*"([^"]+)"', toml)
        base_url = m.group(1) if m else (website or "")
        m2 = re.search(r'env_key\s*=\s*"([^"]+)"', toml)
        env_key = m2.group(1) if m2 else ""
        if not key and env_key:
            key = os.environ.get(env_key, "") or ""
        catalog = [x["model"] for x in cfg.get("modelCatalog", {}).get("models", [])]
        m3 = re.search(r'^\s*model\s*=\s*"([^"]+)"', toml, re.M)
        fallback_model = m3.group(1) if m3 else ""
        if not base_url:
            continue
        out.append({
            "name": name,
            "base_url": base_url,
            "key": key,
            "catalog": catalog,
            "fallback_model": fallback_model,
            "env_key": env_key,
        })
    con.close()
    return out


def fetch_models(base_url, api_key):
    """调上游 /v1/models 拉取真实模型列表，尝试 Bearer 和 api-key 两种认证。"""
    url = base_url.rstrip("/")
    if not url.endswith("/v1"):
        url += "/v1"
    url += "/models"
    last_err = None
    for header_name, header_val in [("Authorization", f"Bearer {api_key}"), ("api-key", api_key)]:
        req = urllib.request.Request(url)
        req.add_header(header_name, header_val)
        try:
            with urllib.request.urlopen(req, timeout=20) as resp:
                data = json.loads(resp.read().decode("utf-8"))
                return [m["id"] for m in data.get("data", [])]
        except Exception as e:
            last_err = e
    raise last_err


def main():
    providers = extract_providers()
    if not providers:
        print("没找到可导入的 codex provider")
        return

    existing_key = ""
    if AIGATE_CFG.exists():
        try:
            existing_key = json.loads(AIGATE_CFG.read_text(encoding="utf-8")).get("api_key", "")
        except Exception:
            pass
    if not existing_key:
        import secrets
        existing_key = "aigate-sk-" + secrets.token_hex(16)

    cfg = {
        "server": {"host": "127.0.0.1", "port": 8317},
        "api_key": existing_key,
        "auth": True,
        "routes": {"all-models": {"models": {}}},
        "active_route": "all-models",
    }

    seen = {}
    total = 0
    missing = []
    print(f"发现 {len(providers)} 个 codex provider，开始导入...\n")
    for p in providers:
        models = list(p["catalog"])
        source = "modelCatalog"
        if not models and p["key"]:
            try:
                models = fetch_models(p["base_url"], p["key"])
                source = f"/v1/models 拉取({len(models)}个)"
            except Exception as e:
                print(f"  [拉取失败] {p['name']}: {e}")
                source = "拉取失败"
                if p.get("fallback_model"):
                    models = [p["fallback_model"]]
                    source = f"TOML兜底({p['fallback_model']})"
        elif not models and not p["key"]:
            source = "缺key跳过"
            missing.append(p["name"])

        added = []
        skipped = []
        for m in models:
            k = p["key"] or ""
            if not k:
                missing.append(f"{p['name']}/{m}")
                k = "sk-请手动填入"
            if m in seen:
                skipped.append(f"{m}(已有{seen[m]})")
                continue
            seen[m] = p["name"]
            cfg["routes"]["all-models"]["models"][m] = {
                "baseURL": p["base_url"],
                "apiKey": k,
                "upstreamModel": m,
            }
            added.append(m)
            total += 1

        status = "OK" if p["key"] else "缺key"
        print(f"  [{status}] {p['name']}  ({source})")
        print(f"        base_url: {p['base_url']}")
        if added:
            print(f"        +{len(added)}: {added}")
        if skipped:
            print(f"        跳过同名 {len(skipped)}: {skipped}")

    AIGATE_DIR.mkdir(parents=True, exist_ok=True)
    AIGATE_CFG.write_text(json.dumps(cfg, indent=2, ensure_ascii=False), encoding="utf-8")

    print(f"\n已生成: {AIGATE_CFG}")
    print(f"aigate api_key: {existing_key}")
    print(f"共导入 {total} 个模型 -> 路由组 'all-models'")
    if missing:
        print(f"\n⚠ {len(missing)} 项缺密钥，需手动编辑 config.json 填入:")
        for x in missing:
            print(f"    - {x}")
    print("\n下一步: ./aigate.exe serve")


if __name__ == "__main__":
    main()
