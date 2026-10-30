// Package aireserveruntime provides the AI Reserve API client with an Amazon Bedrock
// Converse–shaped surface, translating Bedrock-style inputs/outputs to the
// AI Reserve OpenAI-compatible endpoint.
package aireserveruntime

import (
	"net/http"
	"os"
	"time"
)

const (
	defaultBaseURL    = "https://api.aireserve.com"
	defaultTimeout    = 120 * time.Second
	defaultMaxRetries = 2
	sdkVersion        = "0.7.0"
	userAgent         = "aireserve-sdk-go/" + sdkVersion
)

// NoTimeout disables the request timeout entirely.  Any negative Timeout
// value is treated the same way; Timeout == 0 means "use the default".
const NoTimeout time.Duration = -1

// NoRetries disables retries (a single attempt).  Any negative MaxRetries
// value is treated the same way; MaxRetries == 0 means "use the default".
const NoRetries int = -1

// Options configures a Client.
//
// Resolution order for each field:
//  1. Explicit value in Options.
//  2. Environment variable (AIRESERVE_API_KEY / AIRESERVE_BASE_URL; the
//     legacy AUDACITY_API_KEY / AUDACITY_BASE_URL names still work).
//  3. Built-in default.
type Options struct {
	// APIKey is the AI Reserve API key (e.g. "aireserve_api_…").
	// Falls back to AIRESERVE_API_KEY, then the legacy AUDACITY_API_KEY.
	APIKey string

	// BaseURL overrides the default API endpoint.
	// Falls back to AIRESERVE_BASE_URL (legacy AUDACITY_BASE_URL), then
	// https://api.aireserve.com.
	BaseURL string

	// HTTPClient replaces the default http.Client.  The SDK never sets
	// http.Client.Timeout itself (it would cut streams off mid-body); if you
	// provide a client with a Timeout, that timeout will bound entire
	// streaming responses.
	HTTPClient *http.Client

	// MaxRetries is the number of additional attempts after the first.
	// 0 means "use the default" (2, i.e. up to 3 total attempts).
	// Use NoRetries (or any negative value) to disable retries.
	MaxRetries int

	// Timeout bounds each attempt: connection + request write + response
	// headers, and — for Converse only — the full response body read.  The
	// SSE body of a ConverseStream is never bounded by it (streams may run
	// far longer); cancel the context to abort a stream.
	// 0 means "use the default" (120s).  Use NoTimeout (or any negative
	// value) to disable it.
	Timeout time.Duration
}

// Client is the AI Reserve runtime API client.
type Client struct {
	options    Options
	httpClient *http.Client

	// Chat is the OpenAI-format pass-through surface (spec §9):
	// Chat.Completions.Create / Chat.Completions.CreateStream send the
	// request body verbatim to POST /v1/chat/completions.
	Chat *ChatService

	// Messages is the Anthropic-format pass-through surface (spec §9):
	// Messages.Create / Messages.CreateStream / Messages.CountTokens send
	// the request body verbatim to POST /v1/messages and
	// POST /v1/messages/count_tokens.
	Messages *MessagesService
}

// New creates a Client from the provided Options, applying environment-variable
// and default fallbacks.
//
// The client does not validate the API key at construction time; a missing key
// will produce a types.MissingAPIKeyError on the first operation.
func New(opts Options) *Client {
	if opts.APIKey == "" {
		opts.APIKey = os.Getenv("AIRESERVE_API_KEY")
	}
	if opts.APIKey == "" {
		opts.APIKey = os.Getenv("AUDACITY_API_KEY")
	}
	if opts.BaseURL == "" {
		opts.BaseURL = os.Getenv("AIRESERVE_BASE_URL")
	}
	if opts.BaseURL == "" {
		opts.BaseURL = os.Getenv("AUDACITY_BASE_URL")
	}
	if opts.BaseURL == "" {
		opts.BaseURL = defaultBaseURL
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = defaultMaxRetries
	} else if opts.MaxRetries < 0 {
		opts.MaxRetries = 0 // NoRetries: single attempt
	}
	if opts.Timeout == 0 {
		opts.Timeout = defaultTimeout
	} else if opts.Timeout < 0 {
		opts.Timeout = 0 // NoTimeout: internally, 0 = unbounded
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		// Deliberately no http.Client.Timeout: it would bound the entire
		// response body read and kill long-running SSE streams.  Per-attempt
		// timeouts are applied via context in the request path instead.
		httpClient = &http.Client{}
	}

	c := &Client{
		options:    opts,
		httpClient: httpClient,
	}
	c.Chat = &ChatService{Completions: &ChatCompletionsService{client: c}}
	c.Messages = &MessagesService{client: c}
	return c
}
