package skillengine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockLLM struct {
	mu        sync.Mutex
	calls     []mockCall
	responses map[string]string // keyed by substring in userPrompt
}

type mockCall struct {
	SystemPrompt string
	UserPrompt   string
}

func newMockLLM() *mockLLM {
	return &mockLLM{responses: make(map[string]string)}
}

func (m *mockLLM) onPromptContaining(substr, response string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses[substr] = response
}

func (m *mockLLM) Chat(_ context.Context, systemPrompt, userPrompt string, _ ChatOpts) (<-chan Chunk, error) {
	m.mu.Lock()
	m.calls = append(m.calls, mockCall{SystemPrompt: systemPrompt, UserPrompt: userPrompt})
	resp := "Mock LLM response"
	for substr, r := range m.responses {
		if strings.Contains(userPrompt, substr) {
			resp = r
			break
		}
	}
	m.mu.Unlock()

	ch := make(chan Chunk, 1)
	go func() {
		ch <- Chunk{Content: resp, Done: true}
		close(ch)
	}()
	return ch, nil
}

func (m *mockLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

type failLLM struct{ err error }

func (f *failLLM) Chat(_ context.Context, _, _ string, _ ChatOpts) (<-chan Chunk, error) {
	return nil, f.err
}

type mockSkillStore struct {
	mu     sync.Mutex
	skills map[string]*SkillDefinition
	nextID int
}

func newMockSkillStore() *mockSkillStore {
	return &mockSkillStore{skills: make(map[string]*SkillDefinition)}
}

func (s *mockSkillStore) Save(_ context.Context, skill *SkillDefinition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	skill.ID = fmt.Sprintf("skill_%d", s.nextID)
	s.skills[skill.ID] = skill
	return nil
}

func (s *mockSkillStore) Get(_ context.Context, id string) (*SkillDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, ok := s.skills[id]
	if !ok {
		return nil, fmt.Errorf("skill not found: %s", id)
	}
	return sk, nil
}

func (s *mockSkillStore) GetByUser(_ context.Context, userID string) ([]SkillDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SkillDefinition, 0)
	for _, sk := range s.skills {
		if sk.CreatedBy == userID {
			out = append(out, *sk)
		}
	}
	return out, nil
}

func (s *mockSkillStore) Update(_ context.Context, skill *SkillDefinition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skills[skill.ID] = skill
	return nil
}

func (s *mockSkillStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.skills, id)
	return nil
}

type mockRunStore struct {
	mu        sync.Mutex
	runs      map[string]*RunRecord
	artifacts map[string]*ArtifactRecord
	nextRunID int
	nextArtID int
}

func newMockRunStore() *mockRunStore {
	return &mockRunStore{
		runs:      make(map[string]*RunRecord),
		artifacts: make(map[string]*ArtifactRecord),
	}
}

func (s *mockRunStore) CreateRun(_ context.Context, run *RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextRunID++
	run.ID = fmt.Sprintf("run_%d", s.nextRunID)
	s.runs[run.ID] = run
	return nil
}

func (s *mockRunStore) UpdateRunStatus(_ context.Context, runID string, status RunStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[runID]; ok {
		r.Status = status
	}
	return nil
}

func (s *mockRunStore) CompleteRun(_ context.Context, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[runID]; ok {
		r.Status = RunCompleted
		now := time.Now()
		r.CompletedAt = &now
	}
	return nil
}

func (s *mockRunStore) FailRun(_ context.Context, runID string, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[runID]; ok {
		r.Status = RunFailed
		r.ErrorMessage = errMsg
	}
	return nil
}

func (s *mockRunStore) AddArtifact(_ context.Context, artifact *ArtifactRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextArtID++
	artifact.ID = fmt.Sprintf("art_%d", s.nextArtID)
	s.artifacts[artifact.ID] = artifact
	return nil
}

func (s *mockRunStore) GetRun(_ context.Context, runID string) (*RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[runID]
	if !ok {
		return nil, fmt.Errorf("run not found")
	}
	return r, nil
}

func (s *mockRunStore) ListRuns(_ context.Context) ([]RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RunRecord, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, *r)
	}
	return out, nil
}

func (s *mockRunStore) GetRunArtifact(_ context.Context, runID string) (*ArtifactRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.artifacts {
		if a.RunID == runID {
			return a, nil
		}
	}
	return nil, fmt.Errorf("artifact not found")
}

func (s *mockRunStore) ListArtifacts(_ context.Context) ([]ArtifactRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ArtifactRecord, 0, len(s.artifacts))
	for _, a := range s.artifacts {
		out = append(out, *a)
	}
	return out, nil
}

func TestGenerateSkill(t *testing.T) {
	llm := newMockLLM()
	llm.onPromptContaining("Status Report", "# Skill: Status Report\n\n## Intent\nSummarize project status...")
	skills := newMockSkillStore()
	runs := newMockRunStore()

	engine := NewEngine(llm, skills, runs, DefaultAdaptiveConfig())

	skill, err := engine.GenerateSkill(context.Background(), "Status Report", "Weekly status report", "Summarize project status", "## Summary\n## Risks\n## Next Steps", "user1")
	if err != nil {
		t.Fatalf("GenerateSkill: %v", err)
	}

	if skill.ID == "" {
		t.Error("expected skill ID to be set")
	}
	if skill.Name != "Status Report" {
		t.Errorf("expected name 'Status Report', got %q", skill.Name)
	}
	if skill.SkillMarkdown == "" {
		t.Error("expected skill markdown to be generated")
	}
	if skill.CreatedBy != "user1" {
		t.Errorf("expected createdBy 'user1', got %q", skill.CreatedBy)
	}
}

func TestUpdateSkill_RegeneratesMarkdown(t *testing.T) {
	llm := newMockLLM()
	llm.onPromptContaining("Status Report", "# Original Skill MD")
	llm.onPromptContaining("Risk Assessment", "# Regenerated Skill MD")
	skills := newMockSkillStore()
	runs := newMockRunStore()

	engine := NewEngine(llm, skills, runs, DefaultAdaptiveConfig())

	skill, _ := engine.GenerateSkill(context.Background(), "Status Report", "desc", "Summarize status", "## format", "user1")
	updated, err := engine.UpdateSkill(context.Background(), skill.ID, "Risk Assessment", "new desc", "Assess risks", "## new format")
	if err != nil {
		t.Fatalf("UpdateSkill: %v", err)
	}
	if !strings.Contains(updated.SkillMarkdown, "Regenerated") {
		t.Errorf("expected regenerated markdown, got %q", updated.SkillMarkdown)
	}
	if llm.callCount() != 2 {
		t.Errorf("expected 2 LLM calls, got %d", llm.callCount())
	}
}

func TestRunSinglePass(t *testing.T) {
	llm := newMockLLM()
	llm.onPromptContaining("Skill Instructions", "# Status Report\n\nEverything is on track.")
	llm.onPromptContaining("Status Report", "# Mock Skill MD")
	skills := newMockSkillStore()
	runs := newMockRunStore()
	engine := NewEngine(llm, skills, runs, DefaultAdaptiveConfig())

	skill, _ := engine.GenerateSkill(context.Background(), "Status Report", "desc", "Summarize", "fmt", "user1")
	eventCh, err := engine.Run(context.Background(), RunRequest{
		SkillID: skill.ID,
		UserID:  "user1",
		Sources: []SourceInput{{ID: "src1", Name: "notes.txt", Content: "Short meeting notes.", TokenCount: 10}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var events []StepEvent
	for ev := range eventCh {
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}

	runsList, _ := engine.ListRuns(context.Background())
	if len(runsList) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runsList))
	}
	if runsList[0].Mode != ModeSingle {
		t.Errorf("expected mode %q, got %q", ModeSingle, runsList[0].Mode)
	}
}

func TestRunTwoPass(t *testing.T) {
	llm := newMockLLM()
	llm.onPromptContaining("Extract all relevant", "## Extracted Data\n- Key point 1\n- Key point 2")
	llm.onPromptContaining("Extracted Data", "# Final Report from extracted data")
	llm.onPromptContaining("Status Report", "# Mock Skill MD")
	skills := newMockSkillStore()
	runs := newMockRunStore()
	engine := NewEngine(llm, skills, runs, DefaultAdaptiveConfig())

	skill, _ := engine.GenerateSkill(context.Background(), "Status Report", "desc", "Summarize", "fmt", "user1")
	bigContent := strings.Repeat("word ", 5000)
	eventCh, err := engine.Run(context.Background(), RunRequest{
		SkillID: skill.ID,
		UserID:  "user1",
		Sources: []SourceInput{{ID: "src1", Name: "big-doc.txt", Content: bigContent, TokenCount: 5000}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for range eventCh {
	}

	runsList, _ := engine.ListRuns(context.Background())
	if len(runsList) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runsList))
	}
	if runsList[0].Mode != ModeTwoPass {
		t.Errorf("expected mode %q, got %q", ModeTwoPass, runsList[0].Mode)
	}
}

func TestChooseMode(t *testing.T) {
	cfg := DefaultAdaptiveConfig()
	tests := []struct {
		name   string
		tokens int
		force  string
		want   string
	}{
		{"small source", 100, "", ModeSingle},
		{"large source", 5000, "", ModeTwoPass},
		{"at threshold", 4000, "", ModeSingle},
		{"above threshold", 4001, "", ModeTwoPass},
		{"force single", 5000, "single", ModeSingle},
		{"force two", 100, "two", ModeTwoPass},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cfg
			c.ForceMode = tt.force
			sources := []SourceInput{{ID: "1", Content: "", TokenCount: tt.tokens}}
			got := chooseMode(c, sources)
			if got != tt.want {
				t.Errorf("chooseMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGenerateSkill_LLMFallback(t *testing.T) {
	badLLM := &failLLM{err: fmt.Errorf("ollama chat: HTTP 404")}
	skills := newMockSkillStore()
	runs := newMockRunStore()
	engine := NewEngine(badLLM, skills, runs, DefaultAdaptiveConfig())

	skill, err := engine.GenerateSkill(context.Background(), "Risk Report", "Lists project risks", "Extract all risks from the documents", "## Risks\n- Risk 1\n- Risk 2", "user1")
	if err != nil {
		t.Fatalf("GenerateSkill should not fail when LLM is unavailable: %v", err)
	}
	if skill.SkillMarkdown == "" {
		t.Fatal("expected non-empty skill markdown from template fallback")
	}
}

func TestRunFailsWhenExecutionLLMUnavailable(t *testing.T) {
	badLLM := &failLLM{err: fmt.Errorf("provider unavailable")}
	skills := newMockSkillStore()
	runs := newMockRunStore()
	engine := NewEngine(badLLM, skills, runs, DefaultAdaptiveConfig())

	skill, err := engine.GenerateSkill(context.Background(), "Risk Report", "Lists project risks", "Extract all risks", "## Risks", "user1")
	if err != nil {
		t.Fatalf("GenerateSkill: %v", err)
	}

	events, err := engine.Run(context.Background(), RunRequest{
		SkillID: skill.ID,
		UserID:  "user1",
		Sources: []SourceInput{{ID: "s1", Name: "doc.txt", Content: "some content", TokenCount: 20}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	failed := false
	for ev := range events {
		if ev.Status == EventStatusFailed {
			failed = true
		}
	}
	if !failed {
		t.Fatal("expected a failed event")
	}

	runList, _ := engine.ListRuns(context.Background())
	if len(runList) != 1 || runList[0].Status != RunFailed {
		t.Fatalf("expected failed run, got %#v", runList)
	}
}

func TestRunQueryWrappers(t *testing.T) {
	llm := newMockLLM()
	llm.onPromptContaining("Status Report", "# Mock Skill MD")
	llm.onPromptContaining("Skill Instructions", "## Summary\nOK")
	skills := newMockSkillStore()
	runs := newMockRunStore()
	engine := NewEngine(llm, skills, runs, DefaultAdaptiveConfig())

	skill, _ := engine.GenerateSkill(context.Background(), "Status Report", "desc", "Summarize", "fmt", "user1")
	events, err := engine.Run(context.Background(), RunRequest{
		SkillID: skill.ID,
		UserID:  "user1",
		Sources: []SourceInput{{ID: "s1", Name: "doc.txt", Content: "text", TokenCount: 10}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for range events {
	}

	runsList, _ := engine.ListRuns(context.Background())
	if len(runsList) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runsList))
	}

	run, err := engine.GetRun(context.Background(), runsList[0].ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.ID == "" {
		t.Fatal("expected run id")
	}

	artifacts, err := engine.ListArtifacts(context.Background())
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}

	artifact, err := engine.GetRunArtifact(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRunArtifact: %v", err)
	}
	if artifact.Type != ArtifactTypeSkillOutput {
		t.Fatalf("expected artifact type %q, got %q", ArtifactTypeSkillOutput, artifact.Type)
	}
}

func TestSkillAdaptiveConfigOverride(t *testing.T) {
	llm := newMockLLM()
	llm.onPromptContaining("Status Report", "# Mock Skill MD")
	llm.onPromptContaining("Extract all relevant", "## Extracted Data\n- one")
	llm.onPromptContaining("Extracted Data", "## Summary\nDone")
	skills := newMockSkillStore()
	runs := newMockRunStore()

	cfg := DefaultAdaptiveConfig()
	cfg.ForceMode = ModeSingle
	engine := NewEngine(llm, skills, runs, cfg)

	skill, _ := engine.GenerateSkill(context.Background(), "Status Report", "desc", "Summarize", "fmt", "user1")
	skill.AdaptiveConfig = &AdaptiveConfig{ForceMode: ModeTwoPass}

	events, err := engine.Run(context.Background(), RunRequest{
		SkillID: skill.ID,
		UserID:  "user1",
		Sources: []SourceInput{{ID: "s1", Name: "doc.txt", Content: "short", TokenCount: 50}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for range events {
	}

	runsList, _ := engine.ListRuns(context.Background())
	if len(runsList) != 1 || runsList[0].Mode != ModeTwoPass {
		t.Fatalf("expected overridden mode %q, got %#v", ModeTwoPass, runsList)
	}
}

func TestNewEngineDefaultsSinglePassThresholdOnly(t *testing.T) {
	cfg := AdaptiveConfig{SinglePassMaxTokens: 0, ForceMode: ModeTwoPass}
	engine := NewEngine(newMockLLM(), newMockSkillStore(), newMockRunStore(), cfg)

	if engine.adaptiveCfg.SinglePassMaxTokens != DefaultAdaptiveConfig().SinglePassMaxTokens {
		t.Fatalf("expected default threshold, got %d", engine.adaptiveCfg.SinglePassMaxTokens)
	}
	if engine.adaptiveCfg.ForceMode != ModeTwoPass {
		t.Fatalf("expected ForceMode preserved, got %q", engine.adaptiveCfg.ForceMode)
	}
}

func TestTemplateSkillMarkdown(t *testing.T) {
	md := templateSkillMarkdown("Status Report", "Weekly summary", "Summarize project status", "## Summary\n## Risks")
	for _, want := range []string{"Status Report", "Weekly summary", "Summarize project status", "## Summary\n## Risks", "## Intent", "## Instructions", "## Output Format", "## Rules"} {
		if !strings.Contains(md, want) {
			t.Errorf("templateSkillMarkdown missing %q\nFull output:\n%s", want, md)
		}
	}
}
