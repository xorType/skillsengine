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

	"github.com/xorType/skillsengine"
)

type demoLLM struct{}

func (d *demoLLM) Chat(_ context.Context, _, userPrompt string, _ skillengine.ChatOpts) (<-chan skillengine.Chunk, error) {
	out := make(chan skillengine.Chunk, 1)
	go func() {
		defer close(out)
		switch {
		case strings.Contains(userPrompt, "Create a skill instruction document"):
			out <- skillengine.Chunk{Content: "# Campaign Brief Generator\n\n## Intent\nCreate a concise campaign brief from source material.\n\n## Instructions\nExtract only facts from sources and organize into the required sections.\n\n## Output Format\n## Campaign Objective\n## Target Audience\n## Core Message\n## Channels\n## Timeline\n## Risks\n\n## Rules\n- Do not invent missing data.\n- Call out unknowns explicitly.", Done: true}
		case strings.Contains(userPrompt, "Extract all relevant information"):
			out <- skillengine.Chunk{Content: "## Extracted Data\n- Objective: Increase trial signups by 20% in Q2\n- Audience: Mid-market marketing ops managers\n- Message: Launch-ready workflows in under 1 hour\n- Channels: LinkedIn, lifecycle email, partner webinar\n- Timeline: 6-week campaign\n- Risk: Creative approvals may delay launch", Done: true}
		default:
			out <- skillengine.Chunk{Content: "## Campaign Objective\nIncrease trial signups by 20% in Q2.\n\n## Target Audience\nMid-market marketing operations managers.\n\n## Core Message\nTeams can launch production workflows in under one hour.\n\n## Channels\nLinkedIn paid, lifecycle email, and partner webinar.\n\n## Timeline\nSix-week campaign with launch in week 2.\n\n## Risks\nCreative approvals could delay launch."}
		}
	}()
	return out, nil
}

type demoSkillStore struct {
	mu     sync.Mutex
	nextID int
	skills map[string]*skillengine.SkillDefinition
}

func newDemoSkillStore() *demoSkillStore {
	return &demoSkillStore{skills: make(map[string]*skillengine.SkillDefinition)}
}

func (s *demoSkillStore) Save(_ context.Context, skill *skillengine.SkillDefinition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	skill.ID = fmt.Sprintf("skill_%d", s.nextID)
	s.skills[skill.ID] = skill
	return nil
}

func (s *demoSkillStore) Get(_ context.Context, id string) (*skillengine.SkillDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, ok := s.skills[id]
	if !ok {
		return nil, fmt.Errorf("skill not found: %s", id)
	}
	return sk, nil
}

func (s *demoSkillStore) GetByUser(_ context.Context, userID string) ([]skillengine.SkillDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]skillengine.SkillDefinition, 0)
	for _, sk := range s.skills {
		if sk.CreatedBy == userID {
			out = append(out, *sk)
		}
	}
	return out, nil
}

func (s *demoSkillStore) Update(_ context.Context, skill *skillengine.SkillDefinition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skills[skill.ID] = skill
	return nil
}

func (s *demoSkillStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.skills, id)
	return nil
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

	accessToken, err := mcp.runGCloud(ctx, "auth print-access-token")
	if err != nil {
		return fmt.Errorf("get access token through gcloud-mcp: %w", err)
	}
	accessToken = strings.TrimSpace(strings.Split(accessToken, "\n")[0])

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

func main() {
	recipient := flag.String("recipient", "", "recipient email address (required)")
	subject := flag.String("subject", "Example Campaign Brief", "email subject")
	skipEmail := flag.Bool("skip-email", false, "skip Google MCP + Gmail send step")
	flag.Parse()

	if strings.TrimSpace(*recipient) == "" {
		fmt.Fprintln(os.Stderr, "missing --recipient")
		flag.Usage()
		os.Exit(2)
	}

	ctx := context.Background()
	engine := skillengine.NewEngine(&demoLLM{}, newDemoSkillStore(), newDemoRunStore(), skillengine.DefaultAdaptiveConfig())

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
