package main

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	skillengine "github.com/xorType/skillsengine"
)

//go:embed static documents
var contentFS embed.FS

// ---------------------------------------------------------------------------
// Document registry
// ---------------------------------------------------------------------------

type docMeta struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Filename string `json:"filename"`
	Preview  string `json:"preview"`
	Content  string `json:"content,omitempty"`
}

var documents = []docMeta{
	{ID: "call-transcript", Name: "Sales Call Transcript", Filename: "documents/call-transcript.txt"},
	{ID: "marketing-research", Name: "Market Research Report", Filename: "documents/marketing-research.md"},
	{ID: "product-feedback", Name: "Product Feedback Summary", Filename: "documents/product-feedback.txt"},
	{ID: "competitive-analysis", Name: "Competitive Analysis", Filename: "documents/competitive-analysis.md"},
}

func loadDocuments() {
	for i := range documents {
		data, err := contentFS.ReadFile(documents[i].Filename)
		if err != nil {
			log.Printf("WARN: could not load document %s: %v", documents[i].Filename, err)
			continue
		}
		documents[i].Content = string(data)
		// build a ~120 char preview from first non-empty lines
		preview := ""
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(strings.TrimLeft(line, "#-*> "))
			if line == "" {
				continue
			}
			if len(preview)+len(line) > 120 {
				preview += line[:max(0, 120-len(preview))]
				break
			}
			if preview != "" {
				preview += " "
			}
			preview += line
		}
		documents[i].Preview = preview
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func docByID(id string) (docMeta, bool) {
	for _, d := range documents {
		if d.ID == id {
			return d, true
		}
	}
	return docMeta{}, false
}

// ---------------------------------------------------------------------------
// Ollama LLM
// ---------------------------------------------------------------------------

type ollamaLLM struct {
	baseURL string
	model   string
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Options  map[string]any  `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatResponse struct {
	Message ollamaMessage `json:"message"`
	Done    bool          `json:"done"`
	Error   string        `json:"error,omitempty"`
}

func (o *ollamaLLM) Chat(ctx context.Context, systemPrompt, userPrompt string, opts skillengine.ChatOpts) (<-chan skillengine.Chunk, error) {
	messages := []ollamaMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
	options := map[string]any{}
	if opts.Temperature > 0 {
		options["temperature"] = opts.Temperature
	}
	if opts.MaxTokens > 0 {
		options["num_predict"] = opts.MaxTokens
	}
	body, err := json.Marshal(ollamaChatRequest{
		Model:    o.model,
		Messages: messages,
		Stream:   true,
		Options:  options,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal ollama request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}

	out := make(chan skillengine.Chunk, 8)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var r ollamaChatResponse
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				out <- skillengine.Chunk{Err: fmt.Errorf("decode ollama stream: %w", err)}
				return
			}
			if r.Error != "" {
				out <- skillengine.Chunk{Err: fmt.Errorf("ollama error: %s", r.Error)}
				return
			}
			out <- skillengine.Chunk{Content: r.Message.Content, Done: r.Done}
			if r.Done {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			out <- skillengine.Chunk{Err: fmt.Errorf("ollama stream read: %w", err)}
		}
	}()
	return out, nil
}

// ---------------------------------------------------------------------------
// File-backed skill store (one .md file per skill)
// ---------------------------------------------------------------------------

type fileSkillStore struct {
	mu  sync.Mutex
	dir string
}

func newFileSkillStore(dir string) *fileSkillStore {
	return &fileSkillStore{dir: dir}
}

func (s *fileSkillStore) filePath(id string) string {
	return fmt.Sprintf("%s/%s.md", s.dir, id)
}

func (s *fileSkillStore) writeSkill(skill *skillengine.SkillDefinition) error {
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return fmt.Errorf("create skills dir: %w", err)
	}
	type meta struct {
		ID           string    `json:"id"`
		Name         string    `json:"name"`
		Description  string    `json:"description"`
		Intent       string    `json:"intent"`
		OutputFormat string    `json:"outputFormat"`
		CreatedBy    string    `json:"createdBy"`
		CreatedAt    time.Time `json:"createdAt"`
		UpdatedAt    time.Time `json:"updatedAt"`
	}
	m := meta{
		ID: skill.ID, Name: skill.Name, Description: skill.Description,
		Intent: skill.Intent, OutputFormat: skill.OutputFormat,
		CreatedBy: skill.CreatedBy, CreatedAt: skill.CreatedAt, UpdatedAt: skill.UpdatedAt,
	}
	frontmatter, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal skill meta: %w", err)
	}
	content := fmt.Sprintf("---\n%s\n---\n%s", frontmatter, skill.SkillMarkdown)
	return os.WriteFile(s.filePath(skill.ID), []byte(content), 0644)
}

func (s *fileSkillStore) readSkill(id string) (*skillengine.SkillDefinition, error) {
	data, err := os.ReadFile(s.filePath(id))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("skill not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("read skill file: %w", err)
	}
	return parseSkillFile(data)
}

func parseSkillFile(data []byte) (*skillengine.SkillDefinition, error) {
	const sep = "---"
	text := string(data)
	if !strings.HasPrefix(text, sep+"\n") {
		return nil, fmt.Errorf("skill file missing frontmatter")
	}
	rest := text[len(sep)+1:]
	end := strings.Index(rest, "\n"+sep+"\n")
	if end < 0 {
		return nil, fmt.Errorf("skill file missing closing frontmatter delimiter")
	}
	raw := rest[:end]
	body := rest[end+len("\n"+sep+"\n"):]
	var sk skillengine.SkillDefinition
	if err := json.Unmarshal([]byte(raw), &sk); err != nil {
		return nil, fmt.Errorf("parse skill frontmatter: %w", err)
	}
	sk.SkillMarkdown = body
	return &sk, nil
}

func (s *fileSkillStore) Save(_ context.Context, skill *skillengine.SkillDefinition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	skill.ID = fmt.Sprintf("skill_%d", time.Now().UnixNano())
	return s.writeSkill(skill)
}

func (s *fileSkillStore) Get(_ context.Context, id string) (*skillengine.SkillDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readSkill(id)
}

func (s *fileSkillStore) GetByUser(_ context.Context, userID string) ([]skillengine.SkillDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return []skillengine.SkillDefinition{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skills dir: %w", err)
	}
	out := make([]skillengine.SkillDefinition, 0)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(fmt.Sprintf("%s/%s", s.dir, e.Name()))
		if err != nil {
			continue
		}
		sk, err := parseSkillFile(data)
		if err != nil || sk.CreatedBy != userID {
			continue
		}
		out = append(out, *sk)
	}
	return out, nil
}

func (s *fileSkillStore) Update(_ context.Context, skill *skillengine.SkillDefinition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeSkill(skill)
}

func (s *fileSkillStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.filePath(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ---------------------------------------------------------------------------
// In-memory run store
// ---------------------------------------------------------------------------

type demoRunStore struct {
	mu        sync.Mutex
	nextRunID int
	nextArtID int
	runs      map[string]*skillengine.RunRecord
	artifacts map[string]*skillengine.ArtifactRecord
}

func newDemoRunStore() *demoRunStore {
	return &demoRunStore{
		runs:      make(map[string]*skillengine.RunRecord),
		artifacts: make(map[string]*skillengine.ArtifactRecord),
	}
}

func (s *demoRunStore) CreateRun(_ context.Context, run *skillengine.RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextRunID++
	run.ID = fmt.Sprintf("run_%d", s.nextRunID)
	s.runs[run.ID] = run
	return nil
}

func (s *demoRunStore) UpdateRunStatus(_ context.Context, runID string, status skillengine.RunStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run, ok := s.runs[runID]; ok {
		run.Status = status
	}
	return nil
}

func (s *demoRunStore) CompleteRun(_ context.Context, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run, ok := s.runs[runID]; ok {
		now := time.Now()
		run.Status = skillengine.RunCompleted
		run.CompletedAt = &now
	}
	return nil
}

func (s *demoRunStore) FailRun(_ context.Context, runID, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run, ok := s.runs[runID]; ok {
		run.Status = skillengine.RunFailed
		run.ErrorMessage = errMsg
	}
	return nil
}

func (s *demoRunStore) AddArtifact(_ context.Context, artifact *skillengine.ArtifactRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextArtID++
	artifact.ID = fmt.Sprintf("art_%d", s.nextArtID)
	s.artifacts[artifact.ID] = artifact
	return nil
}

func (s *demoRunStore) GetRun(_ context.Context, runID string) (*skillengine.RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok {
		return nil, fmt.Errorf("run not found: %s", runID)
	}
	return run, nil
}

func (s *demoRunStore) ListRuns(_ context.Context) ([]skillengine.RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]skillengine.RunRecord, 0, len(s.runs))
	for _, run := range s.runs {
		out = append(out, *run)
	}
	return out, nil
}

func (s *demoRunStore) GetRunArtifact(_ context.Context, runID string) (*skillengine.ArtifactRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, artifact := range s.artifacts {
		if artifact.RunID == runID {
			return artifact, nil
		}
	}
	return nil, fmt.Errorf("artifact not found")
}

func (s *demoRunStore) ListArtifacts(_ context.Context) ([]skillengine.ArtifactRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]skillengine.ArtifactRecord, 0, len(s.artifacts))
	for _, artifact := range s.artifacts {
		out = append(out, *artifact)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// HTTP server
// ---------------------------------------------------------------------------

type server struct {
	engine *skillengine.Engine
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// GET /
func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := contentFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// GET /api/documents
func (s *server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	type docSummary struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Preview string `json:"preview"`
	}
	out := make([]docSummary, len(documents))
	for i, d := range documents {
		out[i] = docSummary{ID: d.ID, Name: d.Name, Preview: d.Preview}
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/agents
func (s *server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	skills, err := s.engine.ListSkills(r.Context(), "demo-user")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type agentSummary struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	out := make([]agentSummary, len(skills))
	for i, sk := range skills {
		out[i] = agentSummary{ID: sk.ID, Name: sk.Name, Description: sk.Description}
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/agents
// Body: {"name":"...","description":"...","intent":"...","outputFormat":"..."}
// Response: SSE stream of progress, final event carries {id, name}
func (s *server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	var req struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		Intent       string `json:"intent"`
		OutputFormat string `json:"outputFormat"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Intent) == "" {
		http.Error(w, "name and intent are required", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sseEvent := func(eventType string, data any) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, b)
		flusher.Flush()
	}

	sseEvent("status", map[string]string{"message": "Generating agent instructions with LLM…"})

	skill, err := s.engine.GenerateSkill(
		r.Context(),
		req.Name,
		req.Description,
		req.Intent,
		req.OutputFormat,
		"demo-user",
	)
	if err != nil {
		sseEvent("error", map[string]string{"message": err.Error()})
		return
	}

	sseEvent("done", map[string]any{
		"id":          skill.ID,
		"name":        skill.Name,
		"description": skill.Description,
	})
}

// POST /api/run
// Body: {"agentId":"...","documentId":"..."}
// Response: SSE stream of StepEvents, then a "result" event with markdown
func (s *server) handleRun(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	var req struct {
		AgentID    string `json:"agentId"`
		DocumentID string `json:"documentId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	doc, ok := docByID(req.DocumentID)
	if !ok {
		http.Error(w, "document not found", http.StatusNotFound)
		return
	}
	if strings.TrimSpace(doc.Content) == "" {
		http.Error(w, "document has no content", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sseEvent := func(eventType string, data any) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, b)
		flusher.Flush()
	}

	events, err := s.engine.Run(r.Context(), skillengine.RunRequest{
		SkillID: req.AgentID,
		UserID:  "demo-user",
		Sources: []skillengine.SourceInput{{
			ID:      doc.ID,
			Name:    doc.Name,
			Content: doc.Content,
		}},
	})
	if err != nil {
		sseEvent("error", map[string]string{"message": err.Error()})
		return
	}

	finalMarkdown := ""
	for ev := range events {
		sseEvent("step", map[string]any{
			"step":    ev.Step,
			"status":  ev.Status,
			"message": ev.Message,
		})
		if ev.Status == skillengine.EventStatusDone && ev.Step == skillengine.StepPersist {
			if payload, ok := ev.Payload.(map[string]any); ok {
				if md, ok := payload["markdown"].(string); ok {
					finalMarkdown = md
				}
			}
		}
	}

	if strings.TrimSpace(finalMarkdown) == "" {
		sseEvent("error", map[string]string{"message": "no output was generated"})
		return
	}

	sseEvent("result", map[string]string{"markdown": finalMarkdown})
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	loadDocuments()

	engine := skillengine.NewEngine(
		&ollamaLLM{baseURL: "http://localhost:11434", model: "qwen3.5:397b-cloud"},
		newFileSkillStore("skills"),
		newDemoRunStore(),
		skillengine.DefaultAdaptiveConfig(),
	)

	srv := &server{engine: engine}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", srv.handleIndex)
	mux.HandleFunc("GET /api/documents", srv.handleListDocuments)
	mux.HandleFunc("GET /api/agents", srv.handleListAgents)
	mux.HandleFunc("POST /api/agents", srv.handleCreateAgent)
	mux.HandleFunc("POST /api/run", srv.handleRun)

	addr := ":8080"
	log.Printf("Skills Engine Web Demo → http://localhost%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
