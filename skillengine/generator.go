package skillengine

import (
	"context"
	"fmt"
	"strings"
)

// metaPrompt is the system prompt used to auto-generate skill markdown
// from user-provided name, description, intent, and output format.
const metaPrompt = `You are a skill instruction generator. Your job is to create a clear, 
structured skill instruction document in Markdown that another LLM can follow precisely 
to produce the requested output from source documents.

The skill instruction must contain these sections:
1. **Intent** — A clear statement of what the agent should accomplish
2. **Instructions** — Step-by-step extraction and reasoning rules
3. **Output Format** — The exact structure/template the output must follow
4. **Rules** — Edge cases, defaults, and quality constraints

Write the skill as direct instructions to an LLM (use "you" voice).
Be specific and actionable — avoid vague language.
The skill must be self-contained: another LLM reading only this skill file must produce 
correct, consistent output without any additional context about what to do.

Return ONLY the markdown content. No preamble, no explanation outside the skill document.`

// generateSkillMarkdown attempts to call the LLM to produce a skill instruction
// document from the user's four inputs. If the LLM is unavailable it falls back
// to templateSkillMarkdown so skill creation always succeeds.
func generateSkillMarkdown(ctx context.Context, llm LLMProvider, name, desc, intent, outputFormat string) (string, error) {
	md, err := generateSkillMarkdownLLM(ctx, llm, name, desc, intent, outputFormat)
	if err != nil {
		// LLM unavailable or misconfigured — build a usable skill from the raw
		// user inputs so creation never hard-fails due to LLM provider issues.
		return templateSkillMarkdown(name, desc, intent, outputFormat), nil
	}
	return md, nil
}

// generateSkillMarkdownLLM calls the LLM to produce a skill instruction document.
func generateSkillMarkdownLLM(ctx context.Context, llm LLMProvider, name, desc, intent, outputFormat string) (string, error) {
	userPrompt := fmt.Sprintf(`Create a skill instruction document for the following agent:

**Name:** %s
**Description:** %s
**What it should do:** %s
**Desired output format:**
%s

Generate the complete skill instruction markdown.`, name, desc, intent, outputFormat)

	ch, err := llm.Chat(ctx, metaPrompt, userPrompt, ChatOpts{
		MaxTokens:   2000,
		Temperature: 0.3, // low temperature for consistent, structured output
	})
	if err != nil {
		return "", fmt.Errorf("skill generation LLM call: %w", err)
	}

	var b strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			return "", fmt.Errorf("skill generation streaming: %w", chunk.Err)
		}
		b.WriteString(chunk.Content)
	}

	result := strings.TrimSpace(b.String())
	if result == "" {
		return "", fmt.Errorf("skill generation returned empty result")
	}
	return result, nil
}

// templateSkillMarkdown builds a structured skill document directly from the
// user's inputs without calling an LLM. Used as a fallback when LLM is
// unavailable or misconfigured.
func templateSkillMarkdown(name, desc, intent, outputFormat string) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(name)
	b.WriteString("\n\n")

	if desc != "" {
		b.WriteString(desc)
		b.WriteString("\n\n")
	}

	b.WriteString("## Intent\n\n")
	b.WriteString(intent)
	b.WriteString("\n\n")

	b.WriteString("## Instructions\n\n")
	b.WriteString("Read all source documents carefully and extract the information requested.\n")
	b.WriteString("Be thorough, accurate, and faithful to the source material.\n")
	b.WriteString("Do not invent or assume information that is not present in the sources.\n\n")

	if outputFormat != "" {
		b.WriteString("## Output Format\n\n")
		b.WriteString("Structure your response exactly as shown below:\n\n")
		b.WriteString(outputFormat)
		b.WriteString("\n\n")
	}

	b.WriteString("## Rules\n\n")
	b.WriteString("- Use only information present in the provided source documents.\n")
	b.WriteString("- If information is missing or unclear, note it explicitly rather than guessing.\n")
	b.WriteString("- Maintain a professional and objective tone throughout.\n")
	b.WriteString("- Ensure all sections of the output format are populated.\n")

	return strings.TrimSpace(b.String())
}
