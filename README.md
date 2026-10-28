# llmproxy

**Know what your LLM calls cost.** `llmproxy` is a single-binary reverse proxy that sits between your app and the OpenAI or Anthropic API. It records the model, tokens, cost and latency of every request in a local SQLite file, without changing your code.

```
your app  ──►  llmproxy (localhost:8000)  ──►  api.openai.com / api.anthropic.com
                  │
                  ├─ records model, tokens, cost, latency → llmproxy.db
                  ├─ tags requests by feature or user
                  ├─ caches identical requests (optional)
                  └─ enforces a daily budget (optional)
```

```
$ llmproxy stats
Since 2026-10-27 14:50 (last 24h0m0s)

MODEL                   REQUESTS  CACHED  INPUT  OUTPUT  COST
gpt-4o-2024-08-06       2         0       1840   620     $0.010800
gpt-4o-mini-2024-07-18  40        12      4100   5300    $0.003795
TOTAL                   42        12      5940   5920    $0.014595

TAG       REQUESTS  CACHED  INPUT  OUTPUT  COST
search    2         0       1840   620     $0.010800
chat      40        12      4100   5300    $0.003795
TOTAL     42        12      5940   5920    $0.014595
```

## Features

- **Drop-in**: point your SDK's base URL at the proxy. API keys pass straight through and are never stored.
- **Streaming-safe**: server-sent events are forwarded as they arrive, and usage is read from the stream.
- **OpenAI and Anthropic**: Chat Completions, Responses and Messages APIs, JSON and streaming.
- **Cost tracking**: built-in price table, overridable with a JSON file.
- **Tags**: add an `X-Llmproxy-Tag` header to see spend per feature, user or environment.
- **Daily budget**: requests get `429` once the day's spend reaches your limit.
- **Response cache**: identical requests (same API key, URL and body) can be answered from memory.
- **One static binary**: pure Go, no cgo, no Docker, no external database.

## Install

```sh
go install github.com/amangeldievkuu/llmproxy@latest
```

Or build from source:

```sh
git clone https://github.com/amangeldievkuu/llmproxy
cd llmproxy
go build
```

## Quick start

Start the proxy (it forwards to OpenAI by default):

```sh
llmproxy serve
```

Send a request through it:

```sh
curl http://localhost:8000/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hello!"}]}'
```

The proxy logs one line per request:

```
POST /v1/chat/completions 200 gpt-4o-mini-2024-07-18 10 in / 10 out $0.000008 1.23s
```

Then see the totals:

```sh
llmproxy stats            # last 24 hours
llmproxy stats -since 168h  # last 7 days
```

### Use it from your code

Only the base URL changes.

**OpenAI (Python)**

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8000/v1")
```

**OpenAI (Node.js)**

```js
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "http://localhost:8000/v1" });
```

**Anthropic**: run a second proxy pointed at Anthropic, then set the SDK's base URL:

```sh
llmproxy serve -port 8001 -upstream https://api.anthropic.com
```

```python
from anthropic import Anthropic

client = Anthropic(base_url="http://localhost:8001")
```

Any OpenAI-compatible API (OpenRouter, Groq, Together, a local server) works the same way with `-upstream`. Add its models to a prices file to get costs.

## Tags

Add a header to label requests. The proxy removes it before forwarding.

```python
client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[...],
    extra_headers={"X-Llmproxy-Tag": "checkout-assistant"},
)
```

`llmproxy stats` then breaks spend down by tag.

## Daily budget

```sh
llmproxy serve -daily-budget 5
```

Once today's recorded spend reaches $5, requests get `429` with an error body in the usual `{"error": {...}}` shape until midnight (local time).

## Response cache

```sh
llmproxy serve -cache-ttl 10m
```

Identical `POST` requests (same API key, URL and body) are answered from memory for 10 minutes and marked `X-Llmproxy-Cache: hit`. Cached responses are recorded at zero cost. The cache is useful in development and tests; leave it off when you want fresh model output for every call.

## Prices

Built-in prices cover current OpenAI and Anthropic models and are matched by the longest model-name prefix (`gpt-4o-mini-2024-07-18` → `gpt-4o-mini`). Providers change prices, so you can override or add models with a JSON file of USD per million tokens:

```json
{
  "gpt-4o-mini": { "input": 0.15, "output": 0.60 },
  "llama-3.3-70b": { "input": 0.59, "output": 0.79 }
}
```

```sh
llmproxy serve -prices prices.json
```

Requests for a model with no price are still recorded, with cost `0`, and logged as `(price unknown)`.

## Configuration

`llmproxy serve` flags:

| Flag | Env | Default | Description |
| --- | --- | --- | --- |
| `-port` | `PORT` | `8000` | Port to listen on |
| `-upstream` | `LLMPROXY_UPSTREAM` | `https://api.openai.com` | API to forward requests to |
| `-db` | `LLMPROXY_DB` | `llmproxy.db` | SQLite file for usage records |
| `-daily-budget` | | `0` (off) | USD per day before requests get 429 |
| `-cache-ttl` | | `0` (off) | How long to cache identical requests, e.g. `10m` |
| `-prices` | | | JSON file with extra or overriding prices |

`llmproxy stats` flags: `-db` (same as above) and `-since` (default `24h`).

The database is plain SQLite, so you can also query it directly:

```sh
sqlite3 llmproxy.db "SELECT model, SUM(cost_usd) FROM requests GROUP BY model"
```

## Known limitations

- **Prompt-cache discounts are not applied.** Costs use the standard input and output prices. OpenAI cached input tokens and Anthropic cache reads and writes are billed differently, so recorded costs can be slightly off for apps that rely on prompt caching.
- **OpenAI streaming needs `stream_options`.** OpenAI only reports usage in a Chat Completions stream when the request sets `"stream_options": {"include_usage": true}`. Without it, streamed requests are recorded with zero tokens. Anthropic streams and the OpenAI Responses API always include usage.
- **One upstream per process.** Run one proxy per provider.
- **The response cache is in memory** and is cleared on restart.
- **No authentication.** The proxy forwards whatever key the client sends. Run it on localhost or a private network.

## Development

```sh
go test -race ./...
go vet ./...
```

Contributions are welcome. Please open an issue first for larger changes.

## License

[MIT](LICENSE)
