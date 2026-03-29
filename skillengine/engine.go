package skillengine

import (
	"context"
	"fmt"
	"time"
)

// Engine is the main entry point for the skill engine package.
type Engine struct {
	llm         LLMProvider
	skills      SkillStore
	runs        RunStore
	adaptiveCfg AdaptiveConfig
}

// NewEngine creates a new Engine with the provided dependencies.
func NewEngine(llm LLMProvider, skills SkillStore, runs RunStore, cfg AdaptiveConfig) *Engine {
	if cfg.SinglePassMaxTokens <= 0 {
		cfg.SinglePassMaxTokens = DefaultAdaptiveConfig().SinglePassMaxTokens
	}
	return &Engine{
		llm:         llm,
		skills:      skills,
		runs:        runs,
		adaptiveCfg: cfg,
	}
}

// GenerateSkill creates a new skill by auto-generating the skill markdown
// from the user's four inputs, then persists it via SkillStore.
func (e *Engine) GenerateSkill(ctx context.Context, name, desc, intent, outputFormat, userID string) (*SkillDefinition, error) {
	md, err := generateSkillMarkdown(ctx, e.llm, name, desc, intent, outputFormat)
	if err != nil {
		return nil, fmt.Errorf("generate skill: %w", err)
	}

	now := time.Now()
	skill := &SkillDefinition{
		Name:          name,
		Description:   desc,
		Intent:        intent,
		OutputFormat:  outputFormat,
		SkillMarkdown: md,
		CreatedBy:     userID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := e.skills.Save(ctx, skill); err != nil {
		return nil, fmt.Errorf("save skill: %w", err)
	}
	return skill, nil
}

// UpdateSkill updates an existing skill. If intent or outputFormat changed,
// the skill markdown is regenerated.
func (e *Engine) UpdateSkill(ctx context.Context, id, name, desc, intent, outputFormat string) (*SkillDefinition, error) {
	skill, err := e.skills.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get skill: %w", err)
	}

	needsRegen := skill.Intent != intent || skill.OutputFormat != outputFormat

	skill.Name = name
	skill.Description = desc
	skill.Intent = intent
	skill.OutputFormat = outputFormat
	skill.UpdatedAt = time.Now()

	if needsRegen {
		md, err := generateSkillMarkdown(ctx, e.llm, name, desc, intent, outputFormat)
		if err != nil {
			return nil, fmt.Errorf("regenerate skill: %w", err)
		}
		skill.SkillMarkdown = md
	}

	if err := e.skills.Update(ctx, skill); err != nil {
		return nil, fmt.Errorf("update skill: %w", err)
	}
	return skill, nil
}

// GetSkill retrieves a skill definition by ID.
func (e *Engine) GetSkill(ctx context.Context, id string) (*SkillDefinition, error) {
	return e.skills.Get(ctx, id)
}

// ListSkills returns all skills created by a user.
func (e *Engine) ListSkills(ctx context.Context, userID string) ([]SkillDefinition, error) {
	return e.skills.GetByUser(ctx, userID)
}

// DeleteSkill removes a skill definition.
func (e *Engine) DeleteSkill(ctx context.Context, id string) error {
	return e.skills.Delete(ctx, id)
}

// Run executes a skill against the provided sources using the adaptive pipeline.
// It returns a channel of StepEvents for streaming and starts execution in a goroutine.
func (e *Engine) Run(ctx context.Context, req RunRequest) (<-chan StepEvent, error) {
	skill, err := e.skills.Get(ctx, req.SkillID)
	if err != nil {
		return nil, fmt.Errorf("load skill %s: %w", req.SkillID, err)
	}

	if len(req.Sources) == 0 {
		return nil, fmt.Errorf("no sources provided")
	}

	cfg := e.adaptiveCfg
	if skill.AdaptiveConfig != nil {
		if skill.AdaptiveConfig.SinglePassMaxTokens > 0 {
			cfg.SinglePassMaxTokens = skill.AdaptiveConfig.SinglePassMaxTokens
		}
		if skill.AdaptiveConfig.ForceMode != "" {
			cfg.ForceMode = skill.AdaptiveConfig.ForceMode
		}
	}
	mode := chooseMode(cfg, req.Sources)

	sourceIDs := make([]string, len(req.Sources))
	for i, s := range req.Sources {
		sourceIDs[i] = s.ID
	}

	run := &RunRecord{
		SkillID:        req.SkillID,
		SkillName:      skill.Name,
		UserID:         req.UserID,
		Status:         RunPending,
		Mode:           mode,
		SourceInputIDs: sourceIDs,
		StartedAt:      time.Now(),
	}
	if err := e.runs.CreateRun(ctx, run); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}

	eventCh := make(chan StepEvent, 20)
	go e.executePipeline(ctx, run, skill, req.Sources, mode, eventCh)
	return eventCh, nil
}

func (e *Engine) executePipeline(ctx context.Context, run *RunRecord, skill *SkillDefinition, sources []SourceInput, mode string, eventCh chan StepEvent) {
	defer close(eventCh)

	emit := func(ev StepEvent) {
		select {
		case eventCh <- ev:
		case <-ctx.Done():
		}
	}

	markFailed := func(msg string) {
		if err := e.runs.FailRun(ctx, run.ID, msg); err != nil {
			msg = fmt.Sprintf("%s (failed to persist failure state: %v)", msg, err)
		}
		emit(StepEvent{Step: StepPersist, Status: EventStatusFailed, Message: msg})
	}

	if err := e.runs.UpdateRunStatus(ctx, run.ID, RunRunning); err != nil {
		markFailed(fmt.Sprintf("update run status: %v", err))
		return
	}
	emit(StepEvent{Step: StepAssess, Status: EventStatusDone, Message: fmt.Sprintf("Mode: %s (%d sources, ~%d tokens)", mode, len(sources), totalSourceTokens(sources))})

	var markdown string
	var err error

	switch mode {
	case ModeSingle:
		markdown, err = executeSinglePass(ctx, e.llm, skill, sources, emit)
	case ModeTwoPass:
		markdown, err = executeTwoPass(ctx, e.llm, skill, sources, emit)
	default:
		markdown, err = executeSinglePass(ctx, e.llm, skill, sources, emit)
	}

	if err != nil {
		markFailed(err.Error())
		return
	}

	emit(StepEvent{Step: StepPersist, Status: EventStatusStarted, Message: "Saving output..."})

	artifact := &ArtifactRecord{
		RunID:     run.ID,
		Type:      ArtifactTypeSkillOutput,
		Content:   markdown,
		CreatedAt: time.Now(),
	}
	if err := e.runs.AddArtifact(ctx, artifact); err != nil {
		markFailed(err.Error())
		return
	}

	if err := e.runs.CompleteRun(ctx, run.ID); err != nil {
		markFailed(fmt.Sprintf("mark run complete: %v", err))
		return
	}
	emit(StepEvent{Step: StepPersist, Status: EventStatusDone, Message: "Complete", Payload: map[string]any{
		"runId":    run.ID,
		"markdown": markdown,
		"mode":     mode,
	}})
}

// GetRun retrieves a run record.
func (e *Engine) GetRun(ctx context.Context, runID string) (*RunRecord, error) {
	return e.runs.GetRun(ctx, runID)
}

// ListRuns returns all skill runs.
func (e *Engine) ListRuns(ctx context.Context) ([]RunRecord, error) {
	return e.runs.ListRuns(ctx)
}

// GetRunArtifact returns the artifact for a specific run.
func (e *Engine) GetRunArtifact(ctx context.Context, runID string) (*ArtifactRecord, error) {
	return e.runs.GetRunArtifact(ctx, runID)
}

// ListArtifacts returns all skill artifacts.
func (e *Engine) ListArtifacts(ctx context.Context) ([]ArtifactRecord, error) {
	return e.runs.ListArtifacts(ctx)
}
