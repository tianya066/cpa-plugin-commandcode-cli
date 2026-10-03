# cpa-plugin-commandcode-cli

CLIProxyAPI (CPA) provider plugin for [CommandCode](https://commandcode.ai), with an
added **CLI transport** so a **Go / GOAT / Pro / Max** plan can be used.

Forked from [ahoo/cpa-plugin-commandcode](https://github.com/ahoo/cpa-plugin-commandcode) (MIT).
See `NOTICE` for the upstream attribution.

## Why this fork exists

CommandCode exposes two chat surfaces:

| Route | Availability |
|---|---|
| `POST /provider/v1/chat/completions` | Provider plan and above |
| `POST /alpha/generate` | the route CommandCode's own CLI client uses |

A Go-plan key is refused on the Provider API with:

```
403  Your Go plan doesn't include API access. Upgrade to Provider or higher.
```

while the CLI route serves the same catalog for that plan. Upstream v0.3.3 only
speaks the Provider API, so a Go-plan key cannot be used at all. This fork adds the
CLI transport as an explicit, opt-in `transport: cli` setting.

Everything else — model registration, routing, weighted multi-key pool, per-key
proxy, reasoning normalization, the host translators — is unchanged upstream code.

## Configuration

```yaml
plugins:
  enabled: true
  configs:
    commandcode:
      enabled: true
      priority: 100

      # provider (default) | cli | auto
      #   provider — /provider/v1/chat/completions (Provider plan and above)
      #   cli      — /alpha/generate, the plan's own CLI chat route
      #   auto     — provider first; a plan refusal is retried on the CLI route
      transport: cli

      # Sent as x-command-code-version. The CLI route gates on this header and
      # answers `403 upgrade_required` when it is absent.
      cli_version: "1.73.0"
      # cli_base_url: "https://api.commandcode.ai"   # default; derived from base_url otherwise
      # cli_working_dir: "/tmp"

      api_keys:
        - key: user_xxx
          weight: 10
          # proxy_url: http://127.0.0.1:18080   # optional, per key
        - key: user_yyy
          weight: 5
          # disabled: true                      # optional per-key kill switch

      models:
        - alias: deepseek-v4.1-flash
          name: deepseek/deepseek-v4.1-flash
        - alias: glm-5.3-flash
          name: z-ai/glm-5.3-flash
```

CPA v8.0.11 reloads plugin configuration through `plugin.reconfigure`. The management
page waits for the runtime revision to match the saved revision before reporting
success. Installing a new shared library remains a separate deployment operation.

## What the CLI transport does

* Builds the CLI request envelope from the OpenAI payload the host hands the executor:
  folded system section (one cacheable text block), `permissionMode`, `config`,
  `threadId`, and `stream: true` (the route streams only).
* Reads the CLI's concatenated-JSON event stream and converts every event
  (`text-delta`, `reasoning-delta`, `tool-call`, `finish`, `error`, …) into standard
  OpenAI `chat.completion.chunk` payloads.
* Backfills `reasoning_content` so `/v1/messages` renders thinking blocks — the host's
  OpenAI→Claude translator only reads that field.
* Serves non-streaming clients by aggregating the stream into one `chat.completion`,
  including usage (`cached_tokens`, `reasoning_tokens`).
* Keeps the weighted multi-key pool and failover semantics of the upstream executor.

## Build

Debian/glibc toolchain only — the runtime image is Debian, and a musl `.so` fails to
`dlopen`. Requires Go >= 1.26.

```bash
CGO_ENABLED=1 GOOS=linux GOARCH=arm64 go build -buildvcs=false -buildmode=c-shared \
  -ldflags "-s -w -X main.pluginVersion=0.4.0 -X github.com/ahoo/cpa-plugin-commandcode.pluginVersion=0.4.0" \
  -o commandcode-v0.4.0.so ./cmd/commandcode
```

Or with Docker:

```bash
docker run --rm -v "$PWD:/src" -v "$PWD/out:/out" -w /src \
  -e CGO_ENABLED=1 -e GOOS=linux -e GOARCH=arm64 golang:1.26 \
  sh -ec 'go build -buildvcs=false -buildmode=c-shared \
    -ldflags "-s -w -X main.pluginVersion=0.4.0 -X github.com/ahoo/cpa-plugin-commandcode.pluginVersion=0.4.0" \
    -o /out/commandcode-v0.4.0.so ./cmd/commandcode'
```

Install by copying the artifact to `<cliproxyapi_root>/plugins/<os>/<arch>/` and
restarting CPA.

## Test

```bash
go vet ./...
go test ./...
```

`cmd/liveprobe` is an end-to-end probe against the real upstream; it supplies its own
`HostHTTPClient`, so it runs the shipped code path outside CPA:

```bash
go build -o liveprobe ./cmd/liveprobe
./liveprobe user_your_key deepseek/deepseek-v4.1-flash
```

## Verified

Against a live Go-plan account (2026-10-03):

* streaming `/v1/chat/completions`: 26 chunks, `reasoning_content` streamed, `finish=stop`,
  usage with `reasoning_tokens`
* `/v1/messages`: thinking block + text block rendered, `cache_read_input_tokens` reported
* non-streaming `/v1/chat/completions`: aggregated `chat.completion`
* two-key weighted pool: 6/6 requests returned 200

## Notes

* Use of the CLI route is subject to CommandCode's terms. CommandCode answers
  `stream:false` on `/alpha/generate` with
  `400 Proxy use detected. This endpoint only serves CLI.` — make sure your use of this
  fork is permitted for your account before deploying it.
* Model names change; edit the `models:` list rather than the code.

## License

MIT — see `LICENSE` (upstream copyright retained) and `NOTICE`.
## Header set sent on the CLI route

Mirrors the reference implementation
([Mars-Sea/dsh-commandcode-provider](https://github.com/Mars-Sea/dsh-commandcode-provider),
`src/adapter.ts` — the `cli` protocol branch):

| Header | Value | Why |
|---|---|---|
| `Content-Type` | `application/json` | transport |
| `Authorization` | `Bearer <key>` | auth |
| `Accept` | `text/event-stream` | the route streams only |
| `accept-encoding` | `identity` | ask for plain bodies, so compressed SSE/pre-stream errors cannot arrive undecoded |
| `x-command-code-version` | `1.73.0` (configurable) | the route's compatibility gate; absent → `403 upgrade_required` |
| `x-cli-environment` | `production` | CLI environment discriminator |
| `x-project-slug` | slug of `cli_working_dir` | project identifier |
| `x-taste-learning` | `false` | CLI feature flag |
| `x-co-flag` | `false` | CLI feature flag |
| `User-Agent` | `cli-proxy-commandcode/<ver>` | identifies the calling client |

The `User-Agent` is deliberately **not** a copy of the official CLI's, and it carries
**no repository or provenance URL**. The UA reaches a third-party service on every
request, so embedding the operator's fork URL would leak which fork a deployment runs
for no functional gain — the route gates on `x-command-code-version`, not on the UA.
Override it with `cli_user_agent:` if your deployment needs a different identity.

`projectSlug` follows the reference implementation's `projectSlugFromPath`: lower-cased,
non-alphanumerics collapsed to single dashes, drive prefix dropped, edges trimmed.
## 统一管理页 / Management panel

在 CPA 管理中心打开 **CommandCode**。首次打开时输入 CPA 管理密钥并点击
“连接并读取”。管理密钥与 CommandCode 上游 Key 是两种不同的凭据。

页面支持：

- 新增、替换、删除和禁用 Key；设置备注、选择权重和每个 Key 的代理。
- 分别查看每个账号、订阅、余额、5 小时/每周额度及具体错误。
- 编辑客户端模型别名、上游模型名称和显示名称。
- 选择 CLI、Provider 或自动通道；Go/GOAT/Pro/Max 账号使用 CLI 通道。
- 对单个已保存 Key 发起少量实际调用，显示文本、状态和耗时。
- 保存时检查配置版本，保存后等待运行时确认生效，无需逐次重启 CPA。

已有 Key 只显示掩码。替换输入留空会保留原值，代理也必须显式修改或清除。
删除在保存前可撤销；配置冲突不会自动覆盖另一次修改。

| Route | Purpose |
|---|---|
| `GET /v0/resource/plugins/commandcode/index.html` | Self-contained management page |
| `GET /v0/management/commandcode/status` | Runtime revision and per-key accounts/quotas; `?refresh=1` bypasses cache |
| `GET /v0/management/commandcode/settings` | Masked saved settings and runtime revision |
| `POST /v0/management/commandcode/settings` | Validated configuration update with optimistic revision check |
| `POST /v0/management/commandcode/test` | Small inference probe for one saved key |

Every management route requires the CPA management credential. The plugin forwards
that credential only to the local CPA management API to preserve the existing config
and trigger reconfiguration. The default local management endpoint is
`http://127.0.0.1:8317`; `management_base_url` supports another loopback listener.

Account queries use the current management request's HTTP callback, never one retained
from a completed inference. Results are cached per plugin instance for 30 seconds;
reconfiguration discards the old cache. Individual accounts may fail independently.

### 从自定义供应商迁移

`api-keys.openai-compatibility` 的 CommandCode Key 与
`plugins.configs.commandcode.api_keys` 是两份不同配置，不会自动同步。
迁移时先备份并将现有 Key 去重导入插件，然后在插件里保留客户端正在使用的
别名、确认每个 Key 能调用，最后停用旧 CommandCode 供应商。

例如，保留 `cc-deepseek-v4.1-flash` 别名并将上游名称设置为
`deepseek/deepseek-v4.1-flash`。插件会自己完成路由与执行，不需要再建立同名
OpenAI-compatible 供应商。CPA 继续提供客户端鉴权、统一 API 和协议转换。

### Panel authentication

The page may reuse the CPA panel's remembered management credential or an explicitly
remembered `commandcode-management-key`. It also supports a session-only credential.
Management credentials are not accepted from URL parameters; CommandCode keys are
never saved in browser storage. On 401/403, requests stop and the page directs the
operator to the management credential input.

### ABI 状态码（0.7.1 修复）

宿主的 `management.handle` 返回值由 `pluginapi.ManagementResponse` 解码，该结构体
没有 JSON 标签，字段名是 `StatusCode` / `Headers` / `Body`。因此插件在信封结果里
必须使用这些 Go 字段名；早期版本发的是 `status_code`，`encoding/json` 既不报错也不
匹配，导致每个 4xx/5xx 都被静默改写成 HTTP 200，页面的 401 与 409 分支永远不生效。

现在编码统一走 `ManagementEnvelope`，并由
`TestManagementEnvelopeRoundTripsThroughSDKType` 用 SDK 类型解码回归。

### 管理响应转义与 usage 回调（0.7.2 修复）

宿主对 `schema_version < 6` 的插件会把管理接口 JSON 里的每个字符串做 HTML 实体转义。
本插件原先按 v7 SDK 的常量上报 4，导致 `/status` 里的上游错误文本变成 `&#34;` 之类，
并且 `display_name`、`base_url` 这类用户可编辑字段每次读取都被转义、保存时又把转义
结果写回，值会被逐次污染。现在上报 `abiSchemaVersion = 6`（宿主的
`SchemaVersionRawManagementResponse`），管理响应按原文返回。

同时补齐 `usage_plugin` 能力位：`plugin.go` 一直声明
`pluginapi.Capabilities.UsagePlugin`，但 ABI 注册结构里没有对应字段、分发表里也没有
`usage.handle`，所以宿主从不注册 usage 适配器，`HandleUsage` 永远不会被调用。现已
导出该能力并补上分发分支。

`cmd/commandcode/abi_contract_test.go` 覆盖这三项契约：状态码按 SDK 类型往返、
注册上报 schema_version ≥ 6、已实现能力必须在线上导出且有分发分支。

### User-Agent 不再包含仓库地址（0.7.3）

CLI 通道的默认 `User-Agent` 曾写成 `cli-proxy-commandcode/<ver> (+https://github.com/<owner>/<repo>)`。
这个请求头在每条请求上都会发给 CommandCode，等于把「本部署用的是哪个 fork、仓库在哪」
一并上报，而 CLI 路由实际只校验 `x-command-code-version`，UA 带 URL 没有任何功能收益。

现在默认值是不含 URL 的 `cli-proxy-commandcode/<version>`。`cli_local_test.go` 增加了断言：
UA 必须标识客户端，且**不得**出现 `http`、`github.com` 或括号包裹的来源说明。
需要不同标识时用 `cli_user_agent:` 覆盖。

### 响应 model 回显请求名（0.7.4）

OpenAI 客户端用响应里的 `model` 做归属判断，所以它必须是**客户端请求的那个名字**。
早期版本把上游名同时用于请求体和响应回填，于是请求 `cc-deepseek-v4.1-flash` 会收到
`model: deepseek/deepseek-v4.1-flash-fast`——客户端看到一个自己从未请求过的模型，
宿主也会因此对每次调用记录一条 model-substitution 警告。

现在两者分开：

- 上游请求体继续使用 `name`（上游模型名），CLI 路由不接受裸别名；
- 响应 `model` 回显客户端请求名，取值顺序为
  ① 宿主传入的 `requested_model` 元数据 → ② 配置的 alias 反查 → ③ 原样回显。

`executor_model_echo_test.go` 覆盖该契约，包括流式 chunk 与非流式聚合两条路径，
并断言请求体仍使用上游名。变异验证：把响应改回上游名后，5 个用例立即失败。

> 面板里的第三个字段 `display_name` 只是展示标签，不影响调用链路。
> 若它与 alias 不一致（例如给 `glm-5.3-flash` 填了 `cc-glm-5.3-flash`），
> 只会让映射看起来混乱，建议留空或与 alias 保持一致。

### 工具调用消息形状（0.7.5 修复）

CLI 路由收的是 AI SDK 的 `ModelMessage`，不是 OpenAI 消息形状。把 OpenAI 请求原样转发，
带工具历史的调用一定会被上游拒绝，逐字报：

```
Invalid option: expected one of "user"|"assistant" at "params.messages[N].role"
or Invalid input: expected array, received string at "params.messages[N].content"
```

两个字段都对不上：`role:"tool"` 不在允许的 role 里，且该分支要求 content 是**数组**；
assistant 用 `tool_calls` 字段声明调用也无效——上游的 zod 双报错恰好说明
`{role:"tool", content: [...]}` 才是它的第二分支。

转换规则（`cli_tool_messages.go`）：

- assistant 带 `tool_calls` → content 变为
  `[{"type":"tool-call","toolCallId":…,"toolName":…,"input":{…}}]`，
  `input` 由 `arguments` 字符串解析成对象；
- `role:"tool"` → `{"role":"tool","content":[{"type":"tool-result","toolCallId":…,"toolName":…,"output":{"type":"text","value":…}}]}`，
  上游用 id 把它匹配到前一条 assistant 的调用；
- 找不到归属调用的孤岛工具结果降级为普通 user 文本——上游对这类消息会报
  `Messages with role 'tool' must be a response to a preceding message with 'tool_calls'`，
  整条请求会因此 400；
- 没有工具调用的文本消息保持原形状，纯文本对话字节级不变。

`cli_tool_messages_test.go` 覆盖单调用、并行调用（一条 assistant 多个调用 + 多条结果）、
孤岛降级与文本形状回归。变异验证：把工具结果改回「role tool + 字符串 content」、
把 assistant 改回 `tool_calls` 字段，两轮各有用例立即失败。

### 额度耗尽的 Key 不再让整条调用失败（0.7.5）

上游对「账号额度用完」返回的是 **400**：

```json
{"success":false,"error":{"code":"BAD_REQUEST","status":400,
 "message":"You have insufficient credits to make this request. ..."}}
```

这是**关于 Key** 的失败，不是关于请求的失败——同一个池里另一个账号可能还有额度。
原先的策略把 400/403/404/422 一律当成「请求本身有问题，换 Key 没用」直接放弃，
于是池里只要有一个账号被用光，权重越高越容易先撞上它，整个 provider 就表现为不可用。

现在 `retryableBody` 给这条规则开了一个窄口子：只有 **400 且响应体带
`insufficient credits`** 才继续转移；校验类 400（例如消息形状错误）仍然立即失败，
不会拿多个账号反复重试一条必然被拒的请求。

`pool_credit_failover_test.go` 覆盖谓词真值表、一个「权重 10 的耗尽账号 + 权重 1 的正常
账号」跑 20 轮必须全部成功（若转移失效，靠随机顺序每轮都先选中正常账号的概率是
(1/11)^20），以及「校验 400 只打一次上游」的反向断言。变异验证：换掉额度标记后，
两轮用例立即失败，报错正是客户端看到的 `You have insufficient credits ...`。
