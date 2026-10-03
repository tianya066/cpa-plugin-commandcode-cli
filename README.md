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
| `User-Agent` | `cli-proxy-commandcode/<ver> (+repo)` | truthful self-identification of the calling application |

The `User-Agent` is deliberately **not** a copy of the official CLI's: the reference
implementation sends its own product identity (`<product>/<version> (+url)`) rather than
impersonating the vendor's client, and this fork follows that policy. Override it with
`cli_user_agent:` if your deployment needs a different identity.

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
