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

Restart CPA after changing the config (`docker restart cli-proxy-api`); the host does
not hot-reload plugin configuration.

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
## Management panel

The plugin contributes its own page to the CPA Management Center (menu entry
**CommandCode**) plus seven editable config fields, so the panel shows the channel
state instead of an empty plugin row.

| Route | Purpose |
|---|---|
| `GET /v0/resource/plugins/commandcode/index.html` | the panel page (self-contained HTML, no build step) |
| `GET /v0/management/commandcode/status` | JSON behind the page: transport, model map, key pool, account |

The page renders:

* **transport & endpoints** — `provider` / `cli` / `auto`, the provider base URL, and the
  CLI base URL + version header when the CLI route is in use
* **account** — email and subscription status from `/alpha/whoami` and
  `/alpha/billing/subscriptions`
* **plan quota** — monthly credits and the rolling 5-hour / weekly windows with used/cap,
  progress bars and reset countdown, from `/alpha/billing/credits`
* **key pool** — every configured credential with weight, proxy flag and disabled state
  (values are masked; the panel never receives a full key)
* **model map** — client alias → upstream name → `commandcode/...` namespace id

Account reads reuse the host HTTP client captured from the last executor request, falling
back to a plain client before any request has been seen; results are cached for 30 s so a
page reload does not hammer the account endpoints.

`transport`, `cli_version`, `cli_base_url`, `cli_working_dir`, `cli_user_agent`,
`base_url` and `priority` are declared as `ConfigField`s, so the panel can render them.
The CLI route still needs a restart after a config change (the host does not hot-reload
plugin configuration).

### Panel authentication

The page document is served from the plugin resource route (unauthenticated),
while its data comes from `/v0/management/commandcode/status`, which the host
protects with the management key. The page resolves the key in this order:

1. **its own entry** `localStorage["commandcode-management-key"]` — paste the CPA
   management key into the box at the top of the page once and press 保存; this is
   the reliable path and the same approach the clinepass plugin page uses;
2. `localStorage["managementKey"]`, which the panel writes only when you tick
   **记住密码** at login (XOR-obfuscated with
   `"cli-proxy-api-webui::secure-storage|<host>|<userAgent>"`, `enc::v1::` prefix —
   the page de-obfuscates it exactly like the panel store does);
3. `localStorage["cli-proxy-auth"]` (the panel's zustand blob), if it ever carries
   `state.managementKey`;
4. `?key=<management key>` on the page URL.

If the page reports `HTTP 401`, paste the key into the box and press 保存 once.
