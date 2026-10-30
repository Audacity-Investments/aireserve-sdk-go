package aireserveruntime

// compat_stream.go — raw SSE event stream for the §9 pass-through surfaces.
//
// Unlike ConverseStream, no state machine runs here: events are decoded from
// SSE `data:` lines and delivered exactly as the gateway sent them.  Only the
// termination rules differ per wire format:
//
//   - OpenAI    — the `data: [DONE]` sentinel ends the stream (not yielded);
//     EOF without [DONE] is an error; a chunk carrying `error` aborts with
//     the §4-mapped exception.
//   - Anthropic — no [DONE]: a healthy stream is message_start … message_stop
//     followed by EOF; EOF before message_stop is an error; an `error` event
//     aborts with the §4-mapped exception.  `event:` lines are ignored (the
//     payload's `type` field carries the same information).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
)

// rawStreamMode selects the §9 termination rules for a RawEventStream.
type rawStreamMode int

const (
	rawStreamOpenAI    rawStreamMode = iota // terminates on data: [DONE]
	rawStreamAnthropic                      // terminates on message_stop + EOF
)

// RawEventStream delivers raw provider SSE events — OpenAI chat-completion
// chunks or Anthropic stream events — as decoded JSON objects, untranslated:
//
//	stream, err := client.Chat.Completions.CreateStream(ctx, params)
//	defer stream.Close()
//	for event := range stream.Events() { … }
//	if err := stream.Err(); err != nil { … }
type RawEventStream struct {
	events chan map[string]interface{}
	mode   rawStreamMode

	mu   sync.Mutex
	err  error
	once sync.Once
	done chan struct{}
	resp *http.Response

	// ctx is the stream's request context (derived from the caller's);
	// cancel releases the underlying HTTP request.  Either Close() or
	// cancelling the caller's context unblocks the pump and frees the
	// connection — abandoned streams do not leak goroutines.
	ctx    context.Context
	cancel context.CancelFunc
}

// newRawEventStream wraps a live SSE response and starts the reader goroutine.
func newRawEventStream(resp *http.Response, sctx context.Context, cancel context.CancelFunc, mode rawStreamMode) *RawEventStream {
	s := &RawEventStream{
		events: make(chan map[string]interface{}, 64),
		mode:   mode,
		done:   make(chan struct{}),
		resp:   resp,
		ctx:    sctx,
		cancel: cancel,
	}
	go s.pump()
	return s
}

// Events returns a read-only channel that receives raw events in order.
// The channel is closed when the stream ends (normally or with an error).
func (s *RawEventStream) Events() <-chan map[string]interface{} {
	return s.events
}

// Err returns any error that occurred during streaming.  Call this after
// the Events() channel has been drained (i.e. after a range loop exits).
func (s *RawEventStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close stops the stream, cancels the underlying HTTP request, and releases
// the response body.  Safe to call more than once.
func (s *RawEventStream) Close() error {
	s.once.Do(func() {
		close(s.done)
		s.cancel()
	})
	return s.resp.Body.Close()
}

// pump runs in a goroutine: reads SSE lines from resp.Body under the §1
// transport rules and sends decoded events on the channel, verbatim.
func (s *RawEventStream) pump() {
	defer close(s.events)
	// Release the request context once the body has been fully consumed (or
	// the pump aborts).  Idempotent with Close().
	defer s.cancel()

	scanner := bufio.NewScanner(s.resp.Body)
	// Small initial buffer; allow growth up to the §1 32 MiB line cap.
	scanner.Buffer(make([]byte, 64<<10), maxSSELineBytes)

	sawMessageStop := false

	for scanner.Scan() {
		// Honour Close() — discard remaining events without setting an error.
		select {
		case <-s.done:
			return
		default:
		}

		line := scanner.Bytes()

		// SSE parsing rules: skip blank lines and comment lines.
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		if !bytes.HasPrefix(line, sseDataPrefix) {
			continue // ignore other SSE field types (event:, id:, retry:)
		}

		payload := line[len(sseDataPrefix):]
		if len(payload) > 0 && payload[0] == ' ' {
			payload = payload[1:] // strip optional single space
		}

		if s.mode == rawStreamOpenAI && bytes.Equal(payload, sseDone) {
			return // healthy OpenAI termination; the sentinel is not yielded
		}

		// Probe the two fields that drive termination/error handling; the
		// event itself is delivered as an untranslated map.
		var probe struct {
			Type  string          `json:"type"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(payload, &probe); err != nil {
			continue // malformed or non-object data line — skip (spec §9)
		}
		hasError := len(probe.Error) > 0 && string(probe.Error) != "null"

		switch s.mode {
		case rawStreamOpenAI:
			if hasError {
				s.setErr(parseStreamError(probe.Error))
				return
			}
		case rawStreamAnthropic:
			if probe.Type == "error" {
				if hasError {
					s.setErr(parseStreamError(probe.Error))
				} else {
					s.setErr(streamError("stream error event with no error payload", nil))
				}
				return
			}
			if probe.Type == "message_stop" {
				sawMessageStop = true
			}
		}

		var event map[string]interface{}
		if err := json.Unmarshal(payload, &event); err != nil {
			continue
		}
		s.send(event)
	}

	// scanner.Scan() returned false — either EOF or a read error.
	select {
	case <-s.done:
		return // intentional Close(); no error.
	default:
	}

	if scanErr := scanner.Err(); scanErr != nil {
		// Transport failure (or an over-32MiB line, surfacing as
		// bufio.ErrTooLong) after the first SSE byte.
		s.setErr(streamError("stream read error", scanErr))
		return
	}

	switch s.mode {
	case rawStreamOpenAI:
		// EOF without [DONE] = unexpected connection drop.
		s.setErr(streamError("stream ended without [DONE]", nil))
	case rawStreamAnthropic:
		if !sawMessageStop {
			s.setErr(streamError("stream ended before message_stop", nil))
		}
	}
}

// send delivers an event, or returns immediately once the request context is
// cancelled — which Close() always does — so an abandoned consumer cannot
// wedge the pump on a full channel.
func (s *RawEventStream) send(event map[string]interface{}) {
	select {
	case s.events <- event:
	case <-s.ctx.Done():
	}
}

func (s *RawEventStream) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}
