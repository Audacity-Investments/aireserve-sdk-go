# audacity-sdk-go

Go client for the [Audacity Investments](https://portal.audacityinvestments.com) API with an
**Amazon Bedrock Converse–shaped surface** — swap `bedrockruntime` for `audacityruntime`, change
the constructor, and keep the rest of your code.

- Module: `github.com/Audacity-Investments/audacity-sdk-go`
- Go 1.22+, **stdlib only** (`net/http`, `encoding/json`, `bufio`)
- Version: `0.5.1`

---

## Installation

```sh
go get github.com/Audacity-Investments/audacity-sdk-go
```

---

## Configuration

| Priority | Source |
|----------|--------|
| 1 | Explicit `Options` field |
| 2 | `AIRESERVE_API_KEY` / `AIRESERVE_BASE_URL` environment variables |
| 3 | Legacy `AUDACITY_API_KEY` / `AUDACITY_BASE_URL` environment variables |
| 4 | Defaults: `baseURL=https://api.audacityinvestments.com`, `timeout=120s`, `maxRetries=2` |

A missing API key causes `Converse`/`ConverseStream` to return `*types.MissingAPIKeyError`
immediately, before any network call.

| Variable | Purpose |
|----------|---------|
| `AIRESERVE_API_KEY` | API key (`aireserve_api_…`) |
| `AIRESERVE_BASE_URL` | Override the default endpoint |

The legacy `AUDACITY_*` names still work as fallbacks; when both are set, the
`AIRESERVE_*` names win.

---

## Quickstart — Converse (non-streaming)

This mirrors the aws-sdk-go-v2 Bedrock `Converse` example.

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/Audacity-Investments/audacity-sdk-go"
    "github.com/Audacity-Investments/audacity-sdk-go/audacityruntime"
    "github.com/Audacity-Investments/audacity-sdk-go/audacityruntime/types"
)

func main() {
    // Reads AIRESERVE_API_KEY (or legacy AUDACITY_API_KEY) from the environment.
    client := audacityruntime.New(audacityruntime.Options{})

    resp, err := client.Converse(context.Background(), &audacityruntime.ConverseInput{
        ModelId: audacity.String("gpt-5.4-mini"),
        Messages: []types.Message{{
            Role:    types.ConversationRoleUser,
            Content: []types.ContentBlock{
                &types.ContentBlockMemberText{Value: "Hello, world!"},
            },
        }},
        InferenceConfig: &types.InferenceConfiguration{
            MaxTokens:   audacity.Int32(500),
            Temperature: audacity.Float32(0.2),
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    out := resp.Output.(*types.ConverseOutputMemberMessage)
    text := out.Value.Content[0].(*types.ContentBlockMemberText).Value
    fmt.Println(text)

    fmt.Printf("stop=%s  input=%d  output=%d  latency=%dms\n",
        resp.StopReason,
        resp.Usage.InputTokens,
        resp.Usage.OutputTokens,
        resp.Metrics.LatencyMs,
    )
}
```

---

## Streaming example

```go
streamResp, err := client.ConverseStream(ctx, &audacityruntime.ConverseStreamInput{
    ModelId: audacity.String("gpt-5.4-mini"),
    Messages: []types.Message{{
        Role:    types.ConversationRoleUser,
        Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "Tell me a story"}},
    }},
})
if err != nil {
    log.Fatal(err)
}

stream := streamResp.GetStream()
defer stream.Close()

for event := range stream.Events() {
    switch e := event.(type) {
    case *types.ConverseStreamOutputMemberContentBlockDelta:
        if td, ok := e.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
            fmt.Print(td.Value)
        }
    case *types.ConverseStreamOutputMemberMessageStop:
        fmt.Printf("\n[stop: %s]\n", e.Value.StopReason)
    case *types.ConverseStreamOutputMemberMetadata:
        fmt.Printf("[tokens in=%d out=%d latency=%dms]\n",
            e.Value.Usage.InputTokens,
            e.Value.Usage.OutputTokens,
            e.Value.Metrics.LatencyMs,
        )
    }
}
if err := stream.Err(); err != nil {
    log.Fatal(err)
}
```

---

## OpenAI & Anthropic native formats (pass-through)

Besides the Bedrock-shaped `Converse` surface, the client exposes the gateway's
**OpenAI Chat Completions** and **Anthropic Messages** wire formats directly —
same auth, retries, and typed errors, but **no shape translation**: request
bodies are sent verbatim and responses returned untranslated. Both formats work
with every gateway model (the gateway bridges the format).

| Surface | Endpoint |
|---------|----------|
| `client.Chat.Completions.Create` / `CreateStream` | `POST /v1/chat/completions` |
| `client.Messages.Create` / `CreateStream` | `POST /v1/messages` |
| `client.Messages.CountTokens` | `POST /v1/messages/count_tokens` |

Params structs model the common fields; anything else goes in `Extra`, which is
shallow-merged into the request body last — any field the gateway supports
works, with no SDK release needed.

### OpenAI format

```go
resp, err := client.Chat.Completions.Create(ctx, &audacityruntime.ChatCompletionCreateParams{
    Model:       "gpt-5.4-mini",
    Messages:    []map[string]interface{}{{"role": "user", "content": "Hello!"}},
    MaxTokens:   audacity.Int32(500),
    Temperature: audacity.Float64(0.2),
    Extra:       map[string]interface{}{"seed": 7}, // any other OpenAI field
})
if err != nil {
    log.Fatal(err)
}
fmt.Println(resp.Choices[0].Message["content"]) // raw OpenAI shape
fmt.Println(resp.Raw["usage"])                  // full untranslated body
```

Streaming yields raw chat-completion chunks; the stream ends at the gateway's
`data: [DONE]` sentinel (EOF without it surfaces as `*types.ModelStreamErrorException`
via `stream.Err()`):

```go
stream, err := client.Chat.Completions.CreateStream(ctx, &audacityruntime.ChatCompletionCreateParams{
    Model:    "gpt-5.4-mini",
    Messages: []map[string]interface{}{{"role": "user", "content": "Tell me a story"}},
})
if err != nil {
    log.Fatal(err)
}
defer stream.Close()

for chunk := range stream.Events() { // chunk is map[string]interface{}, untranslated
    if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
        delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})
        if content, ok := delta["content"].(string); ok {
            fmt.Print(content)
        }
    }
}
if err := stream.Err(); err != nil {
    log.Fatal(err)
}
```

### Anthropic format

The wire format used by the anthropic SDKs and Claude Code — and not limited
to Claude models. Requests carry the `anthropic-version: 2023-06-01` header
automatically:

```go
resp, err := client.Messages.Create(ctx, &audacityruntime.MessageCreateParams{
    Model:     "claude-sonnet-4-6",
    MaxTokens: 500,
    Messages:  []map[string]interface{}{{"role": "user", "content": "Hello!"}},
    System:    "Be brief.",
})
if err != nil {
    log.Fatal(err)
}
fmt.Println(resp.Content[0]["text"]) // raw Anthropic shape
fmt.Println(resp.StopReason)         // "end_turn"
```

Streaming yields raw Anthropic events (`message_start` … `message_stop`);
there is no `[DONE]` — a healthy stream ends with `message_stop` followed by
EOF, and EOF before `message_stop` (or an `error` event) surfaces via
`stream.Err()`:

```go
stream, err := client.Messages.CreateStream(ctx, &audacityruntime.MessageCreateParams{
    Model:     "claude-sonnet-4-6",
    MaxTokens: 500,
    Messages:  []map[string]interface{}{{"role": "user", "content": "Tell me a story"}},
})
if err != nil {
    log.Fatal(err)
}
defer stream.Close()

for event := range stream.Events() { // event is map[string]interface{}, untranslated
    if event["type"] == "content_block_delta" {
        delta := event["delta"].(map[string]interface{})
        if text, ok := delta["text"].(string); ok {
            fmt.Print(text)
        }
    }
}
if err := stream.Err(); err != nil {
    log.Fatal(err)
}
```

Token counting is free — no inference happens:

```go
count, err := client.Messages.CountTokens(ctx, &audacityruntime.CountTokensParams{
    Model:    "claude-sonnet-4-6",
    Messages: []map[string]interface{}{{"role": "user", "content": "How many tokens is this?"}},
})
if err != nil {
    log.Fatal(err)
}
fmt.Println(count.InputTokens)
```

Errors on all three routes map to the same typed exceptions as `Converse`
(429 → `*types.ThrottlingException` and retried, 401 →
`*types.AccessDeniedException`, spend cap → `*types.ServiceQuotaExceededException`),
including the gateway's Anthropic-shaped error envelopes.

---

## Migrating from `bedrockruntime`

```diff
 import (
-    "github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
-    "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
+    "github.com/Audacity-Investments/audacity-sdk-go/audacityruntime"
+    "github.com/Audacity-Investments/audacity-sdk-go/audacityruntime/types"
 )

-client := bedrockruntime.NewFromConfig(cfg)
+client := audacityruntime.New(audacityruntime.Options{})  // reads AIRESERVE_API_KEY

 resp, err := client.Converse(ctx, &audacityruntime.ConverseInput{
-    ModelId: aws.String("anthropic.claude-3-5-sonnet-20241022-v2:0"),
+    ModelId: audacity.String("gpt-5.4-mini"),
     Messages: []types.Message{{ … }},
 })
```

The input/output shapes, union-type members, and `errors.As` patterns are identical to
`aws-sdk-go-v2`.

---

## Error handling

All server-derived errors embed `types.APIError` and are reachable via `errors.As`:

```go
import (
    "errors"
    "github.com/Audacity-Investments/audacity-sdk-go/audacityruntime/types"
)

_, err := client.Converse(ctx, input)
switch {
case err == nil:
    // success
case errors.Is(err, &types.MissingAPIKeyError{}):
    log.Fatal("set AIRESERVE_API_KEY")
default:
    var throttle *types.ThrottlingException
    var quota *types.ServiceQuotaExceededException
    var accessDenied *types.AccessDeniedException

    switch {
    case errors.As(err, &throttle):
        fmt.Printf("rate limited (retry-after=%v)\n", throttle.RetryAfterSeconds)
    case errors.As(err, &quota):
        fmt.Println("budget exhausted — will not retry")
    case errors.As(err, &accessDenied):
        fmt.Println("check your API key")
    default:
        log.Fatal(err)
    }
}
```

### Exception taxonomy

| Type | Retryable | Typical cause |
|------|-----------|---------------|
| `ValidationException` | no | Bad request |
| `AccessDeniedException` | no | Auth / authz failure |
| `ResourceNotFoundException` | no | Model not found |
| `ServiceQuotaExceededException` | no | Budget / quota exhausted |
| `ThrottlingException` | **yes** | Rate limited |
| `ModelTimeoutException` | **yes** | Model timeout |
| `ModelErrorException` | no | Model-level error |
| `ModelStreamErrorException` | no | Stream interrupted |
| `ServiceUnavailableException` | **yes** | 502/503/504 |
| `InternalServerException` | **yes** | 500 |
| `MissingAPIKeyError` | — | No key configured |
| `SdkError` | (network: yes) | Network / decode failure |

The SDK retries transient failures automatically (up to `Options.MaxRetries` additional
attempts, default 2) using jittered exponential backoff.  `BUDGET_EXCEEDED` (429) and
all 4xx non-408/429 errors are never retried.

---

## Tool use

```go
resp, err := client.Converse(ctx, &audacityruntime.ConverseInput{
    ModelId: audacity.String("gpt-5.4-mini"),
    Messages: []types.Message{{
        Role:    types.ConversationRoleUser,
        Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "Weather in London?"}},
    }},
    ToolConfig: &types.ToolConfiguration{
        Tools: []types.Tool{{
            ToolSpec: &types.ToolSpecification{
                Name:        "get_weather",
                Description: audacity.String("Returns current weather"),
                InputSchema: &types.ToolInputSchema{
                    Json: map[string]interface{}{
                        "type": "object",
                        "properties": map[string]interface{}{
                            "city": map[string]interface{}{"type": "string"},
                        },
                        "required": []string{"city"},
                    },
                },
            },
        }},
        ToolChoice: &types.ToolChoiceMemberAuto{},
    },
})
// resp.StopReason == "tool_use"
// resp.Output.(*types.ConverseOutputMemberMessage).Value.Content[0].(*types.ContentBlockMemberToolUse)
```

---

## Images (vision models)

Bedrock-style image content blocks are supported in user messages. Pass raw
bytes (base64-encoded for you) or a URL (Audacity extension):

```go
imageBytes, err := os.ReadFile("chart.png")
if err != nil {
    log.Fatal(err)
}

resp, err := client.Converse(ctx, &audacityruntime.ConverseInput{
    ModelId: audacity.String("gpt-5.5"),
    Messages: []types.Message{{
        Role: types.ConversationRoleUser,
        Content: []types.ContentBlock{
            &types.ContentBlockMemberText{Value: "What does this chart show?"},
            &types.ContentBlockMemberImage{Value: types.ImageBlock{
                Format: types.ImageFormatPng,
                Source: &types.ImageSourceMemberBytes{Value: imageBytes},
            }},
        },
    }},
})

// Or reference a hosted image directly (not available in Bedrock):
// Source: &types.ImageSourceMemberUrl{Value: "https://example.com/photo.jpg"}
```

`Format` is one of `ImageFormatPng`, `ImageFormatJpeg`, `ImageFormatGif`,
`ImageFormatWebp`. Use a vision-capable model.

---

## Video input

Bedrock-style video content blocks are supported in user messages. Pass raw
bytes; they are base64-encoded for you:

```go
videoBytes, err := os.ReadFile("demo.mp4")
if err != nil {
    log.Fatal(err)
}

resp, err := client.Converse(ctx, &audacityruntime.ConverseInput{
    ModelId: audacity.String("gemini-2.5-flash"),
    Messages: []types.Message{{
        Role: types.ConversationRoleUser,
        Content: []types.ContentBlock{
            &types.ContentBlockMemberText{Value: "What happens in this video?"},
            &types.ContentBlockMemberVideo{Value: types.VideoBlock{
                Format: types.VideoFormatMp4,
                Source: &types.VideoSourceMemberBytes{Value: videoBytes},
            }},
        },
    }},
})
```

`Format` is one of `VideoFormatMp4`, `VideoFormatMov`, `VideoFormatMkv`,
`VideoFormatWebm`, `VideoFormatFlv`, `VideoFormatMpeg`, `VideoFormatMpg`,
`VideoFormatWmv`, `VideoFormatThreeGp`.

Video is **Gemini-only** at the gateway: `gemini-2.5-flash`, `gemini-2.5-pro`,
and `gemini-3-flash-preview` accept video input — every other model rejects it
with an HTTP 400. Inline video rides the request body, so keep raw video
≤ ~20 MB (base64 inflates it by ~33% against the gateway's body cap).

### Media resolution (cheaper video tokens)

Set `MediaResolution` on the request to control how video/image input is
tokenized — `types.MediaResolutionLow` cuts video token cost roughly **4x**
(Gemini models; other models ignore the field). Accepted values:
`MediaResolutionLow`, `MediaResolutionMedium`, `MediaResolutionHigh`,
`MediaResolutionUltraHigh`. When unset, the field is omitted and the model's
default applies.

```go
resp, err := client.Converse(ctx, &audacityruntime.ConverseInput{
    ModelId:         audacity.String("gemini-2.5-flash"),
    MediaResolution: types.MediaResolutionLow, // ~4x cheaper video tokens
    Messages: []types.Message{{
        Role: types.ConversationRoleUser,
        Content: []types.ContentBlock{
            &types.ContentBlockMemberText{Value: "What happens in this video?"},
            &types.ContentBlockMemberVideo{Value: types.VideoBlock{
                Format: types.VideoFormatMp4,
                Source: &types.VideoSourceMemberBytes{Value: videoBytes},
            }},
        },
    }},
})
```

The same field exists on `ConverseStreamInput`.

### Large videos: upload first, then reference by URI

For videos over ~20 MB (up to **1 GB**), upload once with `UploadFile` and
reference the returned `audacity://files/…` URI — the analogue of Bedrock
Converse's `s3Location` video source:

```go
videoBytes, err := os.ReadFile("large-demo.mp4")
if err != nil {
    log.Fatal(err)
}

upload, err := client.UploadFile(ctx, &audacityruntime.UploadFileInput{
    Data:        videoBytes,
    ContentType: "video/mp4",
})
if err != nil {
    log.Fatal(err)
}

resp, err := client.Converse(ctx, &audacityruntime.ConverseInput{
    ModelId: audacity.String("gemini-2.5-flash"),
    Messages: []types.Message{{
        Role: types.ConversationRoleUser,
        Content: []types.ContentBlock{
            &types.ContentBlockMemberText{Value: "Summarise this video."},
            &types.ContentBlockMemberVideo{Value: types.VideoBlock{
                Format: types.VideoFormatMp4,
                Source: &types.VideoSourceMemberURI{Value: upload.Uri},
            }},
        },
    }},
})
```

`UploadFile` performs the two-step §6 flow for you: a `POST /v1/files` for a
presigned upload ticket (same auth, error mapping, and retry policy as
`Converse`), then a **resumable upload** of the bytes (GCS resumable-session
protocol): the file is sent in 8 MiB chunks, and on a network failure or
transient server error (5xx/429) the SDK queries the session for the last
confirmed byte and **automatically resumes from there** — up to 5 recovery
attempts, with the budget resetting every time a chunk is confirmed. Other
4xx errors fail immediately. Notes:

- `ContentType` must be one of the video MIME types (`video/mp4`,
  `video/mov`, `video/webm`, …); `Data` is capped at **1 GB**.
- Uploaded files are transient inference inputs: they auto-delete after
  **~24 hours**, so upload shortly before use and re-upload for later
  sessions.
- The presigned upload URL itself expires after **~15 minutes**
  (`UploadFileOutput.ExpiresAt`); `UploadFile` uses it immediately, so this
  only matters if you inspect the ticket yourself.
- Files are namespaced per API key's client — a URI from one client is a
  400 `file not found` for another.

---

## Image generation

Generate images from a text prompt with `GenerateImage`. With
`ResponseFormat` `"b64_json"` the image bytes come back inline:

```go
out, err := client.GenerateImage(ctx, &audacityruntime.GenerateImageInput{
    Model:          audacity.String("gpt-image-1"),
    Prompt:         audacity.String("A watercolor painting of a fox in a snowy forest"),
    Size:           audacity.String("1024x1024"),
    ResponseFormat: audacity.String("b64_json"),
})
if err != nil {
    log.Fatal(err)
}

img, err := base64.StdEncoding.DecodeString(out.Data[0].B64Json)
if err != nil {
    log.Fatal(err)
}
os.WriteFile("fox.png", img, 0o644)
```

With `ResponseFormat` `"url"` (the default) the gateway stores the image and
returns a signed download URL that expires after ~24 hours:

```go
out, err := client.GenerateImage(ctx, &audacityruntime.GenerateImageInput{
    Model:  audacity.String("gemini-2.5-flash-image"),
    Prompt: audacity.String("A watercolor painting of a fox in a snowy forest"),
})
if err != nil {
    log.Fatal(err)
}
fmt.Println(out.Data[0].Url) // signed download URL, valid ~24 h
```

Optional fields: `N` (1–10 images), `Size` (`"WxH"`, model-dependent),
`Quality` (e.g. `"standard"`, `"hd"`), and `User`. The output carries
`Created`, `Data` (each entry has `Url` or `B64Json`, plus `RevisedPrompt`
when the provider rewrites your prompt) and optional `Usage` token counts.
Errors map to the same typed errors as `Converse` (401 →
`*types.AccessDeniedException`, 429 → `*types.ThrottlingException`, spend cap
→ `*types.ServiceQuotaExceededException`), usable with `errors.As`.

### Image models

| Model | Pricing |
|------|---------|
| `gemini-2.5-flash-image` | token-based (≈ $0.039 / image) |
| `gemini-3.1-flash-image` | token-based + $0.067 / image at 1K resolution |
| `gpt-image-1` | token-based ($5.00 / 1M text input, $40.00 / 1M image output tokens) |

Per-image models bill a flat rate per generated image; token-based models
report token counts in `GenerateImageOutput.Usage`. Each request's cost is
recorded against your key like any other API call.

**Reliability note.** Upstream image backends occasionally stall with a 503
for a few minutes. There is deliberately **no automatic fallback** to a
different image model (silently swapping models would change output style and
quality) — the SDK already retries 503s with backoff up to
`Options.MaxRetries`, and callers should retry beyond that rather than switch
models.

---

## Prompt caching

Place a Bedrock-style cache-point block after the stable prefix you want the
provider to cache (system prompt, large documents). Everything up to the cache
point is cached provider-side on Claude models; OpenAI/Gemini models cache
automatically and ignore the marker. At most 4 cache points per request.

```go
resp, err := client.Converse(ctx, &audacityruntime.ConverseInput{
    ModelId: audacity.String("claude-sonnet-4-5"),
    System: []types.SystemContentBlock{
        {Text: longSystemPrompt},
        {CachePoint: &types.CachePointBlock{Type: types.CachePointTypeDefault}},
    },
    Messages: []types.Message{{
        Role: types.ConversationRoleUser,
        Content: []types.ContentBlock{
            &types.ContentBlockMemberText{Value: bigReferenceDocument},
            &types.ContentBlockMemberCachePoint{
                Value: types.CachePointBlock{Type: types.CachePointTypeDefault},
            },
            &types.ContentBlockMemberText{Value: "Summarise the key risks."},
        },
    }},
})

// Cache activity is reported in usage (Bedrock names):
fmt.Println(resp.Usage.CacheReadInputTokens)  // tokens served from cache
fmt.Println(resp.Usage.CacheWriteInputTokens) // tokens written to cache
```

A cache point with nothing before it in the same message is silently ignored.

---

## Admin API (`audacityadmin`)

Run your organization from code — members, teams, budgets, API keys, settings, routing,
GPU machines, containers and training — with the same operations the portal and the
AI Reserve MCP server offer. It authenticates with a **management token**
(`aireserve_mcp_…`, from `aireserve login`, an MCP client's Authenticate step, or the portal's
Management Keys), never an inference key, and acts as that token's owner.

```go
import "github.com/Audacity-Investments/audacity-sdk-go/audacityadmin"

admin, err := audacityadmin.New(audacityadmin.Options{ManagementToken: os.Getenv("AIRESERVE_MCP_TOKEN")})
if err != nil { log.Fatal(err) }

users, err := admin.Members.ListOrgUsers(ctx, nil)
key, err := admin.ApiKeys.CreateClientApiKey(ctx, map[string]any{"name": "CI pipeline"})
fmt.Println(key.Secret) // shown once; key.Sensitive == true
ops, err := admin.Ops(ctx) // what this token may call, filtered by role
```

Every operation returns an `*Outcome` (`OK`, `Summary`, `Message`, `Data`, `Secret`,
`Sensitive`); refusals, `401` and `429` return an `*audacityadmin.Error` with the server's
`Code`. Input fields are listed in each method's doc comment; the package is generated from the
gateway's operation registry. See https://docs.aireserve.com/capabilities/admin-api.

## Options reference

```go
client := audacityruntime.New(audacityruntime.Options{
    APIKey:     "aireserve_api_…",         // falls back to AIRESERVE_API_KEY, then legacy AUDACITY_API_KEY
    BaseURL:    "https://…",              // falls back to AIRESERVE_BASE_URL (legacy AUDACITY_BASE_URL), then default
    HTTPClient: &http.Client{…},          // custom transport / TLS config
    MaxRetries: 3,                        // additional attempts (0 = default 2 → 3 total;
                                          // audacityruntime.NoRetries disables retries)
    Timeout:    60 * time.Second,         // per-attempt timeout (0 = default 120s;
                                          // audacityruntime.NoTimeout disables it)
})
```

The timeout bounds each attempt's connection + headers, and — for `Converse` only —
the full response body. **Streams are never cut off by it**: for `ConverseStream` the
timeout applies only until response headers arrive, so long generations can run
indefinitely. To abort a stream, cancel the request `context` or call `stream.Close()`
— either unblocks the reader goroutine and releases the connection.

If you provide a custom `HTTPClient`, leave its `Timeout` unset — an `http.Client`
timeout bounds the entire response body and will kill long streams mid-generation.

---

## License

Copyright Audacity Investments. All rights reserved.
