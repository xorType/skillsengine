package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	skillengine "github.com/xorType/skillsengine"
)

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

// fileSkillStore persists each skill as an individual .md file inside a directory.
// File format:
//
//	---
//	{JSON metadata (all fields except SkillMarkdown)}
//	---
//	{SkillMarkdown body}
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
	// expect: ---\n{json}\n---\n{body}
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

type demoRunStore struct {
	mu        sync.Mutex
	nextRunID int
	nextArtID int
	runs      map[string]*skillengine.RunRecord
	artifacts map[string]*skillengine.ArtifactRecord
}

func newDemoRunStore() *demoRunStore {
	return &demoRunStore{runs: make(map[string]*skillengine.RunRecord), artifacts: make(map[string]*skillengine.ArtifactRecord)}
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

type mcpRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpClient struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	pending map[int64]chan mcpResponse
	mu      sync.Mutex
	nextID  int64
}

func startGCloudMCP(ctx context.Context) (*mcpClient, error) {
	cmd := exec.CommandContext(ctx, "npx", "-y", "@google-cloud/gcloud-mcp")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start gcloud-mcp: %w", err)
	}

	c := &mcpClient{cmd: cmd, stdin: stdin, pending: make(map[int64]chan mcpResponse), nextID: 1}
	go c.readLoop(stdout)
	if err := c.initialize(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (c *mcpClient) readLoop(r io.Reader) {
	s := bufio.NewScanner(r)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		var resp mcpResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			continue
		}
		if resp.ID == 0 {
			continue
		}
		c.mu.Lock()
		ch := c.pending[resp.ID]
		if ch != nil {
			delete(c.pending, resp.ID)
		}
		c.mu.Unlock()
		if ch != nil {
			ch <- resp
			close(ch)
		}
	}
}

func (c *mcpClient) initialize(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "skillengine-campaign-example",
			"version": "0.1.0",
		},
	})
	if err != nil {
		return fmt.Errorf("mcp initialize: %w", err)
	}
	return c.notify("notifications/initialized", map[string]any{})
}

func (c *mcpClient) notify(method string, params any) error {
	req := mcpRequest{JSONRPC: "2.0", Method: method, Params: params}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

func (c *mcpClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	ch := make(chan mcpResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req := mcpRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *mcpClient) Close() error {
	_ = c.stdin.Close()
	return c.cmd.Wait()
}

type toolCallResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent map[string]any `json:"structuredContent"`
	IsError           bool           `json:"isError"`
}

func (c *mcpClient) runGCloud(ctx context.Context, command string) (string, error) {
	variants := []map[string]any{
		{
			"name": "run_gcloud_command",
			"arguments": map[string]any{
				"command": command,
			},
		},
		{
			"name": "run_gcloud_command",
			"arguments": map[string]any{
				"args": strings.Fields(command),
			},
		},
	}

	var lastErr error
	for _, v := range variants {
		raw, err := c.call(ctx, "tools/call", v)
		if err != nil {
			lastErr = err
			continue
		}
		var out toolCallResult
		if err := json.Unmarshal(raw, &out); err != nil {
			lastErr = err
			continue
		}
		if out.IsError {
			lastErr = errors.New("tool reported error")
			continue
		}
		for _, c := range out.Content {
			if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
				return strings.TrimSpace(c.Text), nil
			}
		}
		if v, ok := out.StructuredContent["output"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), nil
		}
		lastErr = errors.New("no command output returned")
	}
	return "", fmt.Errorf("run_gcloud_command failed: %w", lastErr)
}

func sendCampaignBriefViaGoogleMCP(ctx context.Context, recipient, subject, markdown string) error {
	mcp, err := startGCloudMCP(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = mcp.Close() }()

	accessToken, err := mcp.runGCloud(ctx, "auth print-access-token --scopes=https://www.googleapis.com/auth/gmail.send")
	if err != nil {
		return fmt.Errorf("get access token through gcloud-mcp: %w\n\nHint: re-authenticate with Gmail scopes:\n  gcloud auth application-default login --scopes=https://www.googleapis.com/auth/cloud-platform,https://www.googleapis.com/auth/gmail.send", err)
	}
	// Extract the first non-empty line and verify it looks like a bearer token.
	for _, line := range strings.Split(accessToken, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			accessToken = line
			break
		}
	}
	if !strings.HasPrefix(accessToken, "ya29.") {
		return fmt.Errorf("gcloud returned an invalid access token (got %q); the current credential may not include the Gmail send scope.\n\nRe-authenticate with:\n  gcloud auth application-default login --scopes=https://www.googleapis.com/auth/cloud-platform,https://www.googleapis.com/auth/gmail.send", accessToken)
	}

	rawMessage := buildRawGmailMessage(recipient, subject, markdown)
	payload, err := json.Marshal(map[string]string{"raw": rawMessage})
	if err != nil {
		return fmt.Errorf("marshal gmail payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://gmail.googleapis.com/gmail/v1/users/me/messages/send", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create gmail request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("gmail send request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gmail send failed: status=%s body=%s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func buildRawGmailMessage(recipient, subject, markdown string) string {
	mime := strings.Join([]string{
		fmt.Sprintf("To: %s", recipient),
		fmt.Sprintf("Subject: %s", subject),
		"Content-Type: text/plain; charset=UTF-8",
		"MIME-Version: 1.0",
		"",
		markdown,
	}, "\r\n")
	return base64.RawURLEncoding.EncodeToString([]byte(mime))
}

// mdToText strips common markdown syntax to produce a plain-text version.
func mdToText(md string) string {
	var sb strings.Builder
	for _, line := range strings.Split(md, "\n") {
		// strip heading markers
		trimmed := strings.TrimLeft(line, "#")
		if len(trimmed) < len(line) {
			line = strings.TrimSpace(trimmed)
		}
		// strip bold/italic markers
		line = strings.ReplaceAll(line, "**", "")
		line = strings.ReplaceAll(line, "__", "")
		line = strings.ReplaceAll(line, "*", "")
		line = strings.ReplaceAll(line, "_", "")
		// strip inline code
		line = strings.ReplaceAll(line, "`", "")
		// convert bullet - to plain dash spacing
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			line = "  " + strings.TrimSpace(line)
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func main() {
	recipient := flag.String("recipient", "", "recipient email address (required)")
	subject := flag.String("subject", "Example Campaign Brief", "email subject")
	skipEmail := flag.Bool("skip-email", false, "skip Google MCP + Gmail send step")
	output := flag.String("output", "", "file path to save the campaign brief (default: campaign-brief.<format>)")
	format := flag.String("format", "md", "output format for saved brief: md or txt")
	flag.Parse()

	if *format != "md" && *format != "txt" {
		fmt.Fprintln(os.Stderr, "--format must be md or txt")
		os.Exit(2)
	}
	if *output == "" {
		*output = "campaign-brief." + *format
	}

	if !*skipEmail && strings.TrimSpace(*recipient) == "" {
		fmt.Fprintln(os.Stderr, "missing --recipient")
		flag.Usage()
		os.Exit(2)
	}

	ctx := context.Background()
	engine := skillengine.NewEngine(&ollamaLLM{baseURL: "http://localhost:11434", model: "qwen3.5:397b-cloud"}, newFileSkillStore("skills"), newDemoRunStore(), skillengine.DefaultAdaptiveConfig())

	skill, err := engine.GenerateSkill(
		ctx,
		"Campaign Brief Generator",
		"Builds campaign briefs from source research and planning notes.",
		"Produce a practical campaign brief that a growth team can execute.",
		"## Campaign Objective\n## Target Audience\n## Core Message\n## Channels\n## Timeline\n## Risks",
		"demo-user",
	)
	if err != nil {
		panic(err)
	}

	events, err := engine.Run(ctx, skillengine.RunRequest{
		SkillID: skill.ID,
		UserID:  "demo-user",
		Sources: []skillengine.SourceInput{{
			ID:      "src-1",
			Name:    "campaign-notes.md",
			Content: "Goal is +20% trial signups in Q2. Primary audience is mid-market marketing ops managers. Core message: launch-ready workflows in under one hour. Channels: LinkedIn paid, lifecycle email, partner webinar. Timing: 6 weeks. Main risk: creative approvals.",
		}},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println("Streaming events:")
	finalMarkdown := ""
	for ev := range events {
		fmt.Printf("- step=%s status=%s msg=%s\n", ev.Step, ev.Status, ev.Message)
		if ev.Status == skillengine.EventStatusDone && ev.Step == skillengine.StepPersist {
			if payload, ok := ev.Payload.(map[string]any); ok {
				if md, ok := payload["markdown"].(string); ok {
					finalMarkdown = md
				}
			}
		}
	}

	if strings.TrimSpace(finalMarkdown) == "" {
		fmt.Fprintln(os.Stderr, "no markdown generated; aborting email send")
		os.Exit(1)
	}

	briefContent := finalMarkdown
	if *format == "txt" {
		briefContent = mdToText(finalMarkdown)
	}
	if err := os.WriteFile(*output, []byte(briefContent), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write brief to %s: %v\n", *output, err)
		os.Exit(1)
	}
	fmt.Printf("brief saved to %s\n", *output)

	if *skipEmail {
		fmt.Println("email send skipped (--skip-email)")
		return
	}

	sendCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := sendCampaignBriefViaGoogleMCP(sendCtx, *recipient, *subject, finalMarkdown); err != nil {
		fmt.Fprintf(os.Stderr, "failed to send email via Google MCP flow: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("campaign brief emailed to %s with subject %q\n", *recipient, *subject)
}
