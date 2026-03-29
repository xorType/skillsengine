package skillengine

import "context"

// ChatOpts configures a single LLM call.
type ChatOpts struct {
	MaxTokens   int     `json:"maxTokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
}

// Chunk is a streaming token from the LLM.
type Chunk struct {
	Content string `json:"content"`
	Done    bool   `json:"done"`
	Err     error  `json:"-"`
}

// LLMProvider is the interface the host application must implement to supply
// LLM capabilities to the skill engine.
type LLMProvider interface {
	// Chat streams a response for the given system+user prompts.
	// The returned channel is closed when streaming completes.
	Chat(ctx context.Context, systemPrompt, userPrompt string, opts ChatOpts) (<-chan Chunk, error)
}

// SkillStore persists SkillDefinition documents.
// The host app implements this backed by its own database.
type SkillStore interface {
	Save(ctx context.Context, skill *SkillDefinition) error
	Get(ctx context.Context, id string) (*SkillDefinition, error)
	GetByUser(ctx context.Context, userID string) ([]SkillDefinition, error)
	Update(ctx context.Context, skill *SkillDefinition) error
	Delete(ctx context.Context, id string) error
}

// RunStore persists run records and artifacts.
// The host app implements this backed by its own database.
type RunStore interface {
	CreateRun(ctx context.Context, run *RunRecord) error
	UpdateRunStatus(ctx context.Context, runID string, status RunStatus) error
	CompleteRun(ctx context.Context, runID string) error
	FailRun(ctx context.Context, runID string, errMsg string) error
	AddArtifact(ctx context.Context, artifact *ArtifactRecord) error
	GetRun(ctx context.Context, runID string) (*RunRecord, error)
	ListRuns(ctx context.Context) ([]RunRecord, error)
	GetRunArtifact(ctx context.Context, runID string) (*ArtifactRecord, error)
	ListArtifacts(ctx context.Context) ([]ArtifactRecord, error)
}
