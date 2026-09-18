# aigate — 本地 LLM 模型路由聚合网关

把分散在**多个中转站 / 不同分组**里的模型，打包成一个「路由组」，对外暴露成一个
OpenAI / Anthropic 兼容的**本地中转站**。codex、claude code、cc-switch 把它当普通
中转站注册即可；在组内模型间切换**无需重启客户端**。

```
codex / claude code
    │  baseURL = http://127.0.0.1:8317   key = aigate-sk-xxx
    ▼
aigate 本地网关 (Go 单二进制，纯标准库零依赖)
    │  GET  /v1/models           → 返回当前路由组的模型列表
    │  POST /v1/chat/completions → 按请求里的 model 转发到对应上游
    │  POST /v1/messages         → 同上（Anthropic 格式，claude code 用）
    │  POST /v1/embeddings       → 同上
    ▼
中转站A(deepseek) / 中转站B(glm) / 中转站C(gpt)

         ┌─────────────────────────────────┐
         │  管理 UI (Python stdlib :8318)   │
         │  浏览器勾选模型 → 写 config.json  │
         │  aigate 热重载，无需重启          │
         └─────────────────────────────────┘
```

## 它解决什么问题

- 不同中转站有不同分组，每组 key 只能用部分模型
- 想把 deepseek + glm + gpt 放一起随时切，但 codex/claude 一次只认一个 base_url
- 每次切模型要改配置 + 重启 codex —— 烦
- aigate 把它们聚合成一个本地端点，客户端连 aigate，aigate 按请求里的 `model` 字段
  转发到对应中转站。切模型 = 改请求里的 model 名，客户端自带切换即可，不用重启

## 快速开始

### 1. 编译

```bash
go build -o aigate.exe .
```

得到一个 `aigate.exe`（Windows）或 `aigate`（macOS/Linux），单二进制，零依赖，
随手拷到哪都能跑。

### 2. 初始化配置

```bash
./aigate.exe init
```

输出示例：
```
created config: C:\Users\wlh19\Desktop\aigate\.aigate\config.json
api_key:        aigate-sk-07ca63fddf779bb475a3bc79b1e0de84
server:         http://127.0.0.1:8317/v1
```

记下 `api_key`，客户端连 aigate 时用这个 key（不是中转站的 key）。

### 3. 配置路由组

两种方式，任选其一。

**方式 A — CLI 逐个加**（适合脚本化）：

```bash
./aigate.exe routes add --route power-trio --model deepseek-chat \
  --base-url https://中转站A/v1 --api-key sk-中转站A的key

./aigate.exe routes add --route power-trio --model glm-4.6 \
  --base-url https://中转站B/v1 --api-key sk-中转站B的key

./aigate.exe routes add --route power-trio --model gpt-4o \
  --base-url https://中转站C/v1 --api-key sk-中转站C的key
```

**方式 B — 直接编辑配置文件**（适合一次性写好）：

打开 `.aigate/config.json`（在二进制同目录下），按模板填：

```json
{
  "server": { "host": "127.0.0.1", "port": 8317 },
  "api_key": "aigate-sk-xxx",
  "auth": true,
  "routes": {
    "power-trio": {
      "models": {
        "deepseek-chat": { "baseURL": "https://中转站A/v1", "apiKey": "sk-A", "upstreamModel": "deepseek-chat" },
        "glm-4.6":       { "baseURL": "https://中转站B/v1", "apiKey": "sk-B", "upstreamModel": "glm-4.6" },
        "gpt-4o":        { "baseURL": "https://中转站C/v1", "apiKey": "sk-C", "upstreamModel": "gpt-4o" }
      }
    }
  },
  "active_route": "power-trio"
}
```

字段含义：
| 字段 | 说明 |
|---|---|
| `baseURL` | 上游中转站地址，含或不含 `/v1` 都行（aigate 会自动去重） |
| `apiKey` | 上游中转站的 key |
| `upstreamModel` | 上游真实模型名；若和对外别名不同才需要填（比如对外叫 `gpt4o`，上游叫 `gpt-4o`） |

### 4. 启动网关

```bash
./aigate.exe serve
```

看到：
```
aigate listening on http://127.0.0.1:8317/v1
active route: power-trio  (3 models: [deepseek-chat glm-4.6 gpt-4o])
```

### 5. 接入客户端

#### codex（`~/.codex/config.toml`）

把 aigate 当一个 custom provider：

```toml
model_provider = "aigate"
model = "deepseek-chat"          # 组内任意一个模型名

[model_providers.aigate]
name = "aigate"
base_url = "http://127.0.0.1:8317/v1"
wire_api = "chat"                # 或 "responses"，按你的中转站支持情况
requires_openai_auth = false
env_key = "AIGATE_API_KEY"
```

并设环境变量 `AIGATE_API_KEY` = aigate 的 api_key。

之后在 codex 里切模型，只要把 `model = "xxx"` 改成组内另一个名（或用 codex 自带的
模型切换），**不用改 base_url、不用重启**——aigate 会按 model 名转发。

#### claude code（`~/.claude/settings.json`）

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8317",
    "ANTHROPIC_AUTH_TOKEN": "aigate-sk-xxx"
  }
}
```

claude code 发 `/v1/messages`，aigate 透传到上游中转站（要求中转站支持 Anthropic 格式）。

### 6. 和 cc-switch 联动

cc-switch 用 SQLite 存配置（`~/.cc-switch/cc-switch.db`），通过它的 UI 管理。
在 cc-switch 里**新增一个供应商 / 配置**：

| 字段 | 值 |
|---|---|
| 名称 | `aigate` |
| Base URL | `http://127.0.0.1:8317` |
| API Key | `aigate-sk-xxx`（aigate 的 key，不是中转站的） |

切到这个配置，cc-switch 会把 codex/claude 的配置改成指向 aigate。之后：

- **cc-switch** 管「切到 aigate 聚合层」还是「直连某中转站」
- **aigate** 管「聚合层内切具体模型」（`aigate switch 另一个组`，或客户端自己切 model）

两层分工，互不干扰。

## 日常使用

### 切换路由组

配多个组，按场景切：

```bash
./aigate.exe switch cheap      # 切到只含便宜模型的组
./aigate.exe switch power-trio # 切回三巨头组
```

切换是热生效——aigate 监听配置文件 mtime，下次请求就用新组，**不用重启 aigate**、
更不用重启 codex。

### 查看状态

```bash
./aigate.exe status
./aigate.exe routes list
```

### 轮换 aigate 自己的密钥

```bash
./aigate.exe key --rotate
```

## 管理界面（可视化勾选模型 + 建路由组）

不想手写 JSON，用浏览器界面操作：

```bash
uv run manage.py
```

浏览器打开 `http://127.0.0.1:8318/`：

1. **左侧**：自动从 cc-switch.db 读出所有中转站和模型，勾选你想要的
2. **底部**：输入路由组名称，点「保存路由组」
3. **右侧**：查看已有路由组，一键切换 / 删除

管理 UI 只读写配置文件，**不碰密钥**——key 自动从 cc-switch.db 读，不显示在网页上。
保存后 aigate 热重载，立刻生效。

> 技术栈：Python 标准库 `http.server` + `sqlite3`，零依赖，无需安装任何包。

## 辅助脚本

### 从 cc-switch 批量导入模型

```bash
uv run import_cc.py
```

读取 `~/.cc-switch/cc-switch.db` 里所有 codex provider，拉取 `/v1/models`（失败时用
TOML 里的 model 字段兜底），生成 aigate 配置。

### 手动添加模型

```bash
uv run add_models.py --provider Waw-gptmax --models gpt-5.5,gpt-5.6-sol
# 或不带参数进入交互模式
uv run add_models.py
```

### 交互式挑选模型

```bash
uv run pick.py
```

列出所有中转站和模型，用方向键挑选，生成精简路由组。

## 命令参考

```
aigate init [--force]              初始化配置（生成随机 api_key）
aigate serve [--host H] [--port P] 启动网关
aigate status                      查看当前状态
aigate key [--rotate]              查看 / 轮换 api_key
aigate switch <路由组>             切换激活路由组（热生效）
aigate routes list                 列出所有路由组（* 标记当前激活）
aigate routes add --route R --model M --base-url U --api-key K [--upstream-model M']
                                   添加模型到路由组（组不存在则创建）
aigate routes remove --route R [--model M]  移除整个组或组内某模型
aigate routes show --route R       查看某组详情
```

## 配置文件

默认位置：**二进制同目录下的 `.aigate/config.json`**。

想放别处（比如全局），设环境变量：

```bash
set AIGATE_HOME=C:\Users\wlh19\.aigate   # Windows
export AIGATE_HOME=~/.aigate             # macOS / Linux
```

配置文件改了**自动热重载**，不用重启 aigate。

## 设计说明

- **Go 纯标准库**：`net/http` + `io.Copy` + `encoding/json`，零第三方依赖，单文件
- **单二进制**：`go build` 出一个可执行文件，不需要运行时环境，启动瞬间
- **透传不解析**：请求体原样转给上游（只替换 `model` 字段为上游真实名），响应原样
  回传，流式 SSE 逐块 flush —— 兼容 function call、reasoning、图片等所有扩展字段
- **通用转发**：任何带 `model` 字段的 POST 都按 model 查路由表转发，所以
  `/v1/chat/completions`（OpenAI）、`/v1/messages`（Anthropic）、`/v1/embeddings`
  都能走
- **URL 智能拼接**：`baseURL` 含 `/v1` 时自动去重，不会拼出 `/v1/v1/`

## 测试

```bash
go test -v .
```

进程内起 server + mock 上游，验证 `/v1/models`、鉴权、404/502、热重载、流式透传、
Anthropic 格式转发、URL 拼接等 10 项。
## 项目结构

```
aigate/
├── main.go              # Go 转发网关核心（纯标准库，零依赖）
├── main_test.go         # 端到端测试（10/10）
├── go.mod               # Go module
├── manage.py            # 管理 UI 后端（Python stdlib http.server + sqlite3）
├── ui.html              # 管理 UI 前端（单 HTML，原生 JS + CSS，深色主题）
├── import_cc.py         # 从 cc-switch.db 批量导入模型
├── add_models.py        # 手动添加模型到 cc-switch provider
├── pick.py              # 交互式挑选模型建路由组
├── README.md            # 本文件
└── .aigate/
    └── config.json      # aigate 配置（路由组、api_key、active_route）
```

## 技术选型

| 组件 | 技术 | 理由 |
|---|---|---|
| 转发网关 | Go + `net/http` + `io.Copy` | 单二进制、零依赖、流式透传性能最好 |
| 管理 UI 后端 | Python 标准库 `http.server` + `sqlite3` | 只做配置 CRUD，零依赖，`uv run` 直接跑 |
| 管理 UI 前端 | 单 HTML 文件（原生 JS + CSS） | 控制界面够用，不引入构建工具链 |
| 配置存储 | JSON 文件 | 人类可读可改，热重载靠 mtime 监听 |

**不引入框架**：FastAPI/Flask 对配置 CRUD 是过度设计；Vue/React 要加构建工具链。
自用场景，节俭为主——后端 Go 高速转发，前端够用就行。
