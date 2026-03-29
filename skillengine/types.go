// Package skillengine provides a standalone, pluggable skill-based agent system.
// Users define agents through simple templates (name, description, intent, output format).
// The engine auto-generates a skill markdown instruction set and executes it against
// user-provided sources using an adaptive single-pass or two-pass LLM pipeline.
//
// This package has no dependencies on the host application's internals. It communicates
// through interfaces (LLMProvider, SkillStore, RunStore) that the host app implements.
package skillengine

import "time"

// SkillDefinition is a user-created agent template stored in the database.
// When a user fills in the four fields (Name, Description, Intent, OutputFormat)
// the engine auto-generates SkillMarkdown, the instruction set the LLM follows.
type SkillDefinition struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Intent         string          `json:"intent"`                   // what the agent should do
	OutputFormat   string          `json:"outputFormat"`             // how the output should look
	SkillMarkdown  string          `json:"skillMarkdown"`            // auto-generated instruction set
	AdaptiveConfig *AdaptiveConfig `json:"adaptiveConfig,omitempty"` // per-agent override
	CreatedBy      string          `json:"createdBy"`                // user ID
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

// AdaptiveConfig controls when the engine switches from a single-pass to a
// two-pass pipeline. The engine uses global defaults unless the skill provides
// per-agent overrides.
type AdaptiveConfig struct {
	// SinglePassMaxTokens is the source token count threshold.
	// Below this: single pass. Above this: two pass.
	// Default: 4000
	SinglePassMaxTokens int `json:"singlePassMaxTokens,omitempty"`

	// ForceMode overrides adaptive logic: "single", "two", or "" (auto).
	// "two" is accepted for backwards compatibility and normalized to "two-pass".
	ForceMode string `json:"forceMode,omitempty"`
}

// DefaultAdaptiveConfig returns the sensible default adaptive configuration.
func DefaultAdaptiveConfig() AdaptiveConfig {
	return AdaptiveConfig{
		SinglePassMaxTokens: 4000,
		ForceMode:           "", // auto
	}
}

// RunRequest is the input to Engine.Run.
type RunRequest struct {
	SkillID string        `json:"skillId"`
	UserID  string        `json:"userId"`
	Sources []SourceInput `json:"sources"`
}

// SourceInput is one document/file to be processed by the skill.
type SourceInput struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	MimeType   string `json:"mimeType,omitempty"`
	TokenCount int    `json:"tokenCount,omitempty"` // 0 = engine will estimate
}

const (
	ModeSingle  = "single"
	ModeTwoPass = "two-pass"

	StepAssess  = "assess"
	StepExecute = "execute"
	StepExtract = "extract"
	StepFormat  = "format"
	StepPersist = "persist"

	EventStatusStarted = "started"
	EventStatusDone    = "done"
	EventStatusFailed  = "failed"

	ArtifactTypeSkillOutput = "skill_output"
)

// StepEvent is emitted during pipeline execution, mapping 1:1 to an SSE frame.
type StepEvent struct {
	Step    string `json:"step"`
	Status  string `json:"status"` // EventStatus*
	Message string `json:"message,omitempty"`
	Payload any    `json:"payload,omitempty"`
}

// RunStatus is the lifecycle state of a skill execution.
type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
)

// RunRecord is the persistent representation of a skill execution.
type RunRecord struct {
	ID             string     `json:"id"`
	SkillID        string     `json:"skillId"`
	SkillName      string     `json:"skillName"`
	UserID         string     `json:"userId"`
	Status         RunStatus  `json:"status"`
	Mode           string     `json:"mode"` // ModeSingle | ModeTwoPass
	SourceInputIDs []string   `json:"sourceInputIds"`
	ErrorMessage   string     `json:"errorMessage,omitempty"`
	StartedAt      time.Time  `json:"startedAt"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
}

// ArtifactRecord is the persistent representation of a skill execution output.
type ArtifactRecord struct {
	ID        string    `json:"id"`
	RunID     string    `json:"runId"`
	Type      string    `json:"type"`    // ArtifactTypeSkillOutput
	Content   string    `json:"content"` // markdown string
	CreatedAt time.Time `json:"createdAt"`
}
