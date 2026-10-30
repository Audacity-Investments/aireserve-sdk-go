package types

// ConverseStreamOutput is a sealed union — one variant per SSE stream event.
type ConverseStreamOutput interface {
	isConverseStreamOutput()
}

// ConverseStreamOutputMemberMessageStart carries the messageStart event.
type ConverseStreamOutputMemberMessageStart struct {
	Value MessageStartEvent
}

func (*ConverseStreamOutputMemberMessageStart) isConverseStreamOutput() {}

// ConverseStreamOutputMemberContentBlockStart carries the contentBlockStart event.
type ConverseStreamOutputMemberContentBlockStart struct {
	Value ContentBlockStartEvent
}

func (*ConverseStreamOutputMemberContentBlockStart) isConverseStreamOutput() {}

// ConverseStreamOutputMemberContentBlockDelta carries the contentBlockDelta event.
type ConverseStreamOutputMemberContentBlockDelta struct {
	Value ContentBlockDeltaEvent
}

func (*ConverseStreamOutputMemberContentBlockDelta) isConverseStreamOutput() {}

// ConverseStreamOutputMemberContentBlockStop carries the contentBlockStop event.
type ConverseStreamOutputMemberContentBlockStop struct {
	Value ContentBlockStopEvent
}

func (*ConverseStreamOutputMemberContentBlockStop) isConverseStreamOutput() {}

// ConverseStreamOutputMemberMessageStop carries the messageStop event.
type ConverseStreamOutputMemberMessageStop struct {
	Value MessageStopEvent
}

func (*ConverseStreamOutputMemberMessageStop) isConverseStreamOutput() {}

// ConverseStreamOutputMemberMetadata carries the metadata event (always last).
type ConverseStreamOutputMemberMetadata struct {
	Value ConverseStreamMetadataEvent
}

func (*ConverseStreamOutputMemberMetadata) isConverseStreamOutput() {}

// ────────────────────────────────────────────────
// Payload structs
// ────────────────────────────────────────────────

// MessageStartEvent is the payload for the messageStart stream event.
type MessageStartEvent struct {
	Role ConversationRole
}

// ContentBlockStartEvent is the payload for the contentBlockStart stream event.
type ContentBlockStartEvent struct {
	ContentBlockIndex int32
	Start             ContentBlockStart
}

// ContentBlockStart is a sealed union for block-start variants (currently toolUse only;
// text blocks emit no start event per the spec).
type ContentBlockStart interface {
	isContentBlockStart()
}

// ContentBlockStartMemberToolUse carries the toolUse identity at block start.
type ContentBlockStartMemberToolUse struct {
	Value ToolUseStart
}

func (*ContentBlockStartMemberToolUse) isContentBlockStart() {}

// ToolUseStart holds the identity of a newly opened tool-use block.
type ToolUseStart struct {
	ToolUseId string
	Name      string
}

// ContentBlockDeltaEvent is the payload for the contentBlockDelta stream event.
type ContentBlockDeltaEvent struct {
	ContentBlockIndex int32
	Delta             ContentBlockDelta
}

// ContentBlockDelta is a sealed union for delta variants.
type ContentBlockDelta interface {
	isContentBlockDelta()
}

// ContentBlockDeltaMemberText carries an incremental text fragment.
type ContentBlockDeltaMemberText struct {
	Value string
}

func (*ContentBlockDeltaMemberText) isContentBlockDelta() {}

// ContentBlockDeltaMemberToolUse carries an incremental tool-input JSON fragment.
type ContentBlockDeltaMemberToolUse struct {
	Value ToolUseInputDelta
}

func (*ContentBlockDeltaMemberToolUse) isContentBlockDelta() {}

// ToolUseInputDelta holds a streaming fragment of the tool's input JSON.
type ToolUseInputDelta struct {
	Input string
}

// ContentBlockStopEvent is the payload for the contentBlockStop stream event.
type ContentBlockStopEvent struct {
	ContentBlockIndex int32
}

// MessageStopEvent is the payload for the messageStop stream event.
type MessageStopEvent struct {
	StopReason StopReason
}

// ConverseStreamMetadataEvent is the payload for the metadata stream event.
type ConverseStreamMetadataEvent struct {
	Usage   *TokenUsage
	Metrics *ConverseMetrics
}
