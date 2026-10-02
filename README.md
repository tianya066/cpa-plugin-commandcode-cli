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
