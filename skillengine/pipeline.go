package skillengine

import (
	"context"
	"fmt"
	"strings"
)

// estimateTokens returns a rough token count for a string.
// Uses the ~4 chars per token heuristic.
func estimateTokens(s string) int {
	return len(s) / 4
}

// totalSourceTokens sums token counts across all sources,
// estimating where the caller has not provided a count.
func totalSourceTokens(sources []SourceInput) int {
	total := 0
	for _, s := range sources {
		if s.TokenCount > 0 {
			total += s.TokenCount
		} else {
			total += estimateTokens(s.Content)
		}
	}
	return total
}

// chooseMode decides between single-pass and two-pass based on adaptive config and source size.
func chooseMode(cfg AdaptiveConfig, sources []SourceInput) string {
	if forced := normalizeForcedMode(cfg.ForceMode); forced != "" {
		return forced
	}
	threshold := cfg.SinglePassMaxTokens
	if threshold <= 0 {
		threshold = DefaultAdaptiveConfig().SinglePassMaxTokens
	}
	if totalSourceTokens(sources) > threshold {
		return ModeTwoPass
	}
	return ModeSingle
}

func normalizeForcedMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ModeSingle:
		return ModeSingle
	case "two", ModeTwoPass:
		return ModeTwoPass
	default:
		return ""
	}
}

// concatenateSources joins all source contents with clear delimiters.
func concatenateSources(sources []SourceInput) string {
	if len(sources) == 1 {
		return sources[0].Content
	}
	var b strings.Builder
	for i, s := range sources {
		if i > 0 {
			b.WriteString("\n\n---\n\n")
		}
		fmt.Fprintf(&b, "## Source: %s\n\n%s", s.Name, s.Content)
	}
	return b.String()
}

const singlePassSystem = `You are a skilled document analyst and writer. You will be given
 a skill instruction document and source material. Follow the skill instructions precisely
 to analyze the sources and produce the requested output.

Output ONLY the final document in markdown format. No preamble, no meta-commentary.`

func llmConfigHint(err error) string {
	return fmt.Sprintf("LLM error: %s. Verify the configured model/provider and connectivity.", err.Error())
}

// executeSinglePass runs the skill against sources in one LLM call.
func executeSinglePass(ctx context.Context, llm LLMProvider, skill *SkillDefinition, sources []SourceInput, emit func(StepEvent)) (string, error) {
	emit(StepEvent{Step: StepExecute, Status: EventStatusStarted, Message: "Processing sources (single pass)..."})

	sourceText := concatenateSources(sources)
	userPrompt := fmt.Sprintf(`## Skill Instructions

%s

## Source Material

%s

Now follow the skill instructions above and produce the output.`, skill.SkillMarkdown, sourceText)

	ch, err := llm.Chat(ctx, singlePassSystem, userPrompt, ChatOpts{
		MaxTokens:   4000,
		Temperature: 0.4,
	})
	if err != nil {
		emit(StepEvent{Step: StepExecute, Status: EventStatusFailed, Message: llmConfigHint(err)})
		return "", fmt.Errorf("single pass LLM call: %w", err)
	}

	var b strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			emit(StepEvent{Step: StepExecute, Status: EventStatusFailed, Message: chunk.Err.Error()})
			return "", fmt.Errorf("single pass streaming: %w", chunk.Err)
		}
		b.WriteString(chunk.Content)
	}

	result := strings.TrimSpace(b.String())
	emit(StepEvent{Step: StepExecute, Status: EventStatusDone, Message: "Single pass complete"})
	return result, nil
}

const extractionSystem = `You are a precise information extractor. Given source material,
extract all relevant facts, data points, quotes, and information that would be needed
to produce a document. Organize the extraction by topic/category.

Output structured extracted data in markdown. Be thorough and include everything potentially relevant.
Do not summarize or draw conclusions yet. Extract raw information only.`

const formattingSystem = `You are a skilled document writer. You will be given:
1. A skill instruction document describing what to produce and how
2. Extracted data from source documents

Follow the skill instructions precisely to transform the extracted data into the
requested output format. Use ONLY information from the extracted data.

Output ONLY the final document in markdown format. No preamble, no meta-commentary.`

// executeTwoPass runs extraction then formatting as separate LLM calls.
func executeTwoPass(ctx context.Context, llm LLMProvider, skill *SkillDefinition, sources []SourceInput, emit func(StepEvent)) (string, error) {
	emit(StepEvent{Step: StepExtract, Status: EventStatusStarted, Message: "Extracting information from sources..."})

	sourceText := concatenateSources(sources)
	extractPrompt := fmt.Sprintf(`Extract all relevant information from the following source material.

Context: This extraction will be used to produce a "%s" document.
Focus especially on: %s

## Source Material

%s`, skill.Name, skill.Intent, sourceText)

	ch, err := llm.Chat(ctx, extractionSystem, extractPrompt, ChatOpts{
		MaxTokens:   4000,
		Temperature: 0.2,
	})
	if err != nil {
		emit(StepEvent{Step: StepExtract, Status: EventStatusFailed, Message: llmConfigHint(err)})
		return "", fmt.Errorf("extraction LLM call: %w", err)
	}

	var extracted strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			emit(StepEvent{Step: StepExtract, Status: EventStatusFailed, Message: chunk.Err.Error()})
			return "", fmt.Errorf("extraction streaming: %w", chunk.Err)
		}
		extracted.WriteString(chunk.Content)
	}
	emit(StepEvent{Step: StepExtract, Status: EventStatusDone, Message: "Extraction complete"})

	emit(StepEvent{Step: StepFormat, Status: EventStatusStarted, Message: "Formatting output..."})

	formatPrompt := fmt.Sprintf(`## Skill Instructions

%s

## Extracted Data

%s

Now follow the skill instructions above and produce the final output from the extracted data.`,
		skill.SkillMarkdown, extracted.String())

	ch2, err := llm.Chat(ctx, formattingSystem, formatPrompt, ChatOpts{
		MaxTokens:   4000,
		Temperature: 0.4,
	})
	if err != nil {
		emit(StepEvent{Step: StepFormat, Status: EventStatusFailed, Message: llmConfigHint(err)})
		return "", fmt.Errorf("formatting LLM call: %w", err)
	}

	var result strings.Builder
	for chunk := range ch2 {
		if chunk.Err != nil {
			emit(StepEvent{Step: StepFormat, Status: EventStatusFailed, Message: chunk.Err.Error()})
			return "", fmt.Errorf("formatting streaming: %w", chunk.Err)
		}
		result.WriteString(chunk.Content)
	}

	output := strings.TrimSpace(result.String())
	emit(StepEvent{Step: StepFormat, Status: EventStatusDone, Message: "Formatting complete"})
	return output, nil
}
