// Package types contains all public types for the AI Reserve runtime client,
// matching the Amazon Bedrock Converse API shapes.
package types

// ConversationRole identifies the author of a message.
type ConversationRole string

const (
	ConversationRoleUser      ConversationRole = "user"
	ConversationRoleAssistant ConversationRole = "assistant"
)

// StopReason explains why the model stopped generating.
type StopReason string

const (
	StopReasonEndTurn         StopReason = "end_turn"
	StopReasonMaxTokens       StopReason = "max_tokens"
	StopReasonToolUse         StopReason = "tool_use"
	StopReasonStopSequence    StopReason = "stop_sequence"
	StopReasonContentFiltered StopReason = "content_filtered"
)

// Message is a conversation turn.
type Message struct {
	Role    ConversationRole
	Content []ContentBlock
}

// ContentBlock is a sealed union for message content variants.
type ContentBlock interface {
	isContentBlock()
}

// ContentBlockMemberText is a plain-text content block.
type ContentBlockMemberText struct {
	Value string
}

func (*ContentBlockMemberText) isContentBlock() {}

// ImageFormat identifies the media type of an image block.
type ImageFormat string

const (
	ImageFormatPng  ImageFormat = "png"
	ImageFormatJpeg ImageFormat = "jpeg"
	ImageFormatGif  ImageFormat = "gif"
	ImageFormatWebp ImageFormat = "webp"
)

// ImageSource is a sealed union for where an image's data comes from.
type ImageSource interface {
	isImageSource()
}

// ImageSourceMemberBytes carries raw image bytes (Bedrock parity).
type ImageSourceMemberBytes struct {
	Value []byte
}

func (*ImageSourceMemberBytes) isImageSource() {}

// ImageSourceMemberUrl carries an https or data URL (AI Reserve extension).
type ImageSourceMemberUrl struct {
	Value string
}

func (*ImageSourceMemberUrl) isImageSource() {}

// ImageBlock is the payload of an image content block.
type ImageBlock struct {
	Format ImageFormat
	Source ImageSource
}

// ContentBlockMemberImage is an image content block. Only valid in user
// messages; ignored in assistant messages (Bedrock parity).
type ContentBlockMemberImage struct {
	Value ImageBlock
}

func (*ContentBlockMemberImage) isContentBlock() {}

// VideoFormat identifies the container format of a video block.
type VideoFormat string

const (
	VideoFormatMp4     VideoFormat = "mp4"
	VideoFormatMov     VideoFormat = "mov"
	VideoFormatMkv     VideoFormat = "mkv"
	VideoFormatWebm    VideoFormat = "webm"
	VideoFormatFlv     VideoFormat = "flv"
	VideoFormatMpeg    VideoFormat = "mpeg"
	VideoFormatMpg     VideoFormat = "mpg"
	VideoFormatWmv     VideoFormat = "wmv"
	VideoFormatThreeGp VideoFormat = "three_gp"
)

// VideoSource is a sealed union for where a video's data comes from.
type VideoSource interface {
	isVideoSource()
}

// VideoSourceMemberBytes carries raw video bytes (Bedrock parity).
type VideoSourceMemberBytes struct {
	Value []byte
}

func (*VideoSourceMemberBytes) isVideoSource() {}

// VideoSourceMemberURI references a previously uploaded file by its
// audacity://files/… URI (returned by Client.UploadFile) — the analogue of
// Bedrock's s3Location source for videos too large to send inline.
type VideoSourceMemberURI struct {
	Value string
}

func (*VideoSourceMemberURI) isVideoSource() {}

// VideoBlock is the payload of a video content block.
type VideoBlock struct {
	Format VideoFormat
	Source VideoSource
}

// ContentBlockMemberVideo is a video content block. Only valid in user
// messages; ignored in assistant messages (Bedrock parity).
type ContentBlockMemberVideo struct {
	Value VideoBlock
}

func (*ContentBlockMemberVideo) isContentBlock() {}

// ToolUseBlock carries a model-initiated tool invocation.
type ToolUseBlock struct {
	ToolUseId string
	Name      string
	Input     interface{} // any JSON-serialisable value
}

// ContentBlockMemberToolUse is a tool-use content block.
type ContentBlockMemberToolUse struct {
	Value ToolUseBlock
}

func (*ContentBlockMemberToolUse) isContentBlock() {}

// ToolResultContentBlock is a sealed union for tool-result content variants.
type ToolResultContentBlock interface {
	isToolResultContent()
}

// ToolResultContentMemberText is a plain-text tool result entry.
type ToolResultContentMemberText struct {
	Value string
}

func (*ToolResultContentMemberText) isToolResultContent() {}

// ToolResultContentMemberJson is a JSON tool result entry.
type ToolResultContentMemberJson struct {
	Value interface{}
}

func (*ToolResultContentMemberJson) isToolResultContent() {}

// ToolResultBlock is the payload of a tool result block.
type ToolResultBlock struct {
	ToolUseId string
	Content   []ToolResultContentBlock
	Status    *string // "success" | "error" — optional, ignored on the wire
}

// ContentBlockMemberToolResult is a tool-result content block.
type ContentBlockMemberToolResult struct {
	Value ToolResultBlock
}

func (*ContentBlockMemberToolResult) isContentBlock() {}

// CachePointType identifies the kind of prompt-cache breakpoint.
type CachePointType string

const (
	CachePointTypeDefault CachePointType = "default"
)

// CachePointBlock marks the end of a cacheable prompt prefix (Bedrock parity).
// The zero value is a default cache point.
type CachePointBlock struct {
	Type CachePointType
}

// ContentBlockMemberCachePoint is a prompt-cache breakpoint. The content part
// preceding it gets an ephemeral cache_control marker on the wire; the block
// itself is never emitted. A cache point with no preceding content part in the
// same message is silently ignored.
type ContentBlockMemberCachePoint struct {
	Value CachePointBlock
}

func (*ContentBlockMemberCachePoint) isContentBlock() {}

// SystemContentBlock is a system-prompt content entry. Exactly one of Text or
// CachePoint should be set: a Text entry contributes system-prompt text, while
// a CachePoint entry marks the end of the cacheable system prefix.
type SystemContentBlock struct {
	Text string

	// CachePoint, when non-nil, makes this entry a prompt-cache breakpoint
	// (Text is ignored).
	CachePoint *CachePointBlock
}

// InferenceConfiguration holds optional inference parameters.
type InferenceConfiguration struct {
	MaxTokens     *int32
	Temperature   *float32
	TopP          *float32
	StopSequences []string
}

// MediaResolution controls how the provider tokenizes video/image input.
// It is sent as the top-level media_resolution request field; the gateway
// validates the enum and rewrites it for the provider (Gemini models —
// MediaResolutionLow cuts video token cost roughly 4x; other models ignore
// it). The zero value omits the field.
type MediaResolution string

const (
	MediaResolutionLow       MediaResolution = "low"
	MediaResolutionMedium    MediaResolution = "medium"
	MediaResolutionHigh      MediaResolution = "high"
	MediaResolutionUltraHigh MediaResolution = "ultra_high"
)

// GuardrailTrace is Bedrock's guardrail trace mode.
type GuardrailTrace string

const (
	GuardrailTraceEnabled     GuardrailTrace = "enabled"
	GuardrailTraceDisabled    GuardrailTrace = "disabled"
	GuardrailTraceEnabledFull GuardrailTrace = "enabled_full"
)

// GuardrailStreamProcessingMode is Bedrock's guardrail stream-processing
// mode (ConverseStream only).
type GuardrailStreamProcessingMode string

const (
	GuardrailStreamProcessingModeSync  GuardrailStreamProcessingMode = "sync"
	GuardrailStreamProcessingModeAsync GuardrailStreamProcessingMode = "async"
)

// GuardrailConfiguration mirrors Bedrock Converse's guardrail configuration
// (migration-parity shape). It is forwarded to the gateway verbatim as the
// top-level guardrailConfig request field: the gateway does not run Bedrock
// guardrails and rejects any request carrying it with an honest 400 that
// explains the managed-moderation alternative. The SDK never drops or
// rewrites the field (spec §3).
type GuardrailConfiguration struct {
	// GuardrailIdentifier is the guardrail ID or ARN (required by Bedrock).
	GuardrailIdentifier *string

	// GuardrailVersion is the guardrail version, e.g. "1" or "DRAFT"
	// (required by Bedrock).
	GuardrailVersion *string

	// Trace enables guardrail tracing. Omitted when empty.
	Trace GuardrailTrace

	// StreamProcessingMode selects sync/async guardrail evaluation on
	// ConverseStream (Bedrock parity). Omitted when empty.
	StreamProcessingMode GuardrailStreamProcessingMode
}

// ToolInputSchema wraps a JSON Schema object for a tool's input.
type ToolInputSchema struct {
	Json interface{} // JSON Schema object
}

// ToolSpecification defines a single tool.
type ToolSpecification struct {
	Name        string
	Description *string
	InputSchema *ToolInputSchema
}

// Tool wraps a ToolSpecification.
type Tool struct {
	ToolSpec *ToolSpecification
}

// ToolChoice is a sealed union for how the model selects tools.
type ToolChoice interface {
	isToolChoice()
}

// ToolChoiceMemberAuto lets the model decide whether to call a tool.
type ToolChoiceMemberAuto struct{}

func (*ToolChoiceMemberAuto) isToolChoice() {}

// ToolChoiceMemberAny requires the model to call at least one tool.
type ToolChoiceMemberAny struct{}

func (*ToolChoiceMemberAny) isToolChoice() {}

// SpecificToolChoice names the tool the model must call.
type SpecificToolChoice struct {
	Name string
}

// ToolChoiceMemberTool forces the model to call a specific tool.
type ToolChoiceMemberTool struct {
	Value SpecificToolChoice
}

func (*ToolChoiceMemberTool) isToolChoice() {}

// ToolConfiguration bundles tool definitions and the tool-choice policy.
type ToolConfiguration struct {
	Tools      []Tool
	ToolChoice ToolChoice
}

// ConverseOutput is a sealed union for the Converse operation's output.
type ConverseOutput interface {
	isConverseOutput()
}

// ConverseOutputMemberMessage wraps the assistant's response message.
type ConverseOutputMemberMessage struct {
	Value Message
}

func (*ConverseOutputMemberMessage) isConverseOutput() {}

// TokenUsage reports token consumption for a request.
type TokenUsage struct {
	InputTokens  int32
	OutputTokens int32
	TotalTokens  int32

	// CacheReadInputTokens is the number of prompt tokens served from the
	// provider's prompt cache (Bedrock name).
	CacheReadInputTokens int32

	// CacheWriteInputTokens is the number of prompt tokens written to the
	// provider's prompt cache (Bedrock name).
	CacheWriteInputTokens int32
}

// ConverseMetrics reports latency for a request.
type ConverseMetrics struct {
	LatencyMs int64
}
