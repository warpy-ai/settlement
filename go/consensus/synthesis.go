package consensus

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// mergedReasoningTimeout bounds the AI merged-reasoning extraction.
const mergedReasoningTimeout = 15 * time.Second

// Synthesizer is the single LLM hook the consensus engine may use. A nil
// Synthesizer disables AI synthesis: merge tallying and reasoning extraction
// fall back to their offline algorithmic paths.
type Synthesizer interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// ExtractMergedReasoningAlgorithmic extracts unique reasoning contributions using text analysis.
func ExtractMergedReasoningAlgorithmic(workers []*Result) (*MergedReasoning, error) {
	if len(workers) == 0 {
		return nil, fmt.Errorf("no workers provided")
	}

	contributions := make([]ReasoningContribution, 0)
	allSentences := make([]string, 0) // Track all sentences to check for uniqueness
	order := 1

	// Process each worker's reasoning
	for _, worker := range workers {
		if worker.Response == nil || worker.Response.Reasoning == "" {
			continue
		}

		sentences := SplitIntoSentences(worker.Response.Reasoning)
		if len(sentences) == 0 {
			continue
		}

		// Find the most unique sentence from this worker
		var bestSentence string
		var bestUniqueness float64 = 0

		for _, sentence := range sentences {
			normalized := NormalizeText(sentence)
			if normalized == "" {
				continue
			}

			// Calculate uniqueness as 1 - max similarity to existing sentences
			uniqueness := 1.0
			for _, existing := range allSentences {
				similarity := CalculateSimilarity(normalized, existing)
				if 1-similarity < uniqueness {
					uniqueness = 1 - similarity
				}
			}

			// Prefer longer, more unique sentences
			lengthBonus := float64(len(sentence)) / 200.0 // Bonus for length up to 200 chars
			if lengthBonus > 0.3 {
				lengthBonus = 0.3
			}
			score := uniqueness + lengthBonus

			if score > bestUniqueness {
				bestUniqueness = score
				bestSentence = sentence
			}
		}

		// Add the best unique sentence if it's sufficiently unique (> 0.4 uniqueness)
		if bestSentence != "" && bestUniqueness > 0.4 {
			contributions = append(contributions, ReasoningContribution{
				WorkerID:   worker.WorkerID,
				Text:       bestSentence,
				Confidence: worker.Response.Confidence,
				Order:      order,
			})
			allSentences = append(allSentences, NormalizeText(bestSentence))
			order++
		}
	}

	// Build summary from contributions
	var summaryBuilder strings.Builder
	for i, contrib := range contributions {
		if i > 0 {
			summaryBuilder.WriteString(" ")
		}
		summaryBuilder.WriteString(contrib.Text)
		if !strings.HasSuffix(contrib.Text, ".") && !strings.HasSuffix(contrib.Text, "!") && !strings.HasSuffix(contrib.Text, "?") {
			summaryBuilder.WriteString(".")
		}
		if i >= 2 { // Limit summary to first 3 contributions
			break
		}
	}

	return &MergedReasoning{
		Summary:       summaryBuilder.String(),
		Contributions: contributions,
		WorkerCount:   len(workers),
		SynthesisType: "algorithmic",
	}, nil
}

// extractMergedReasoningAI uses the Synthesizer to produce a conversational response from worker reasoning.
func extractMergedReasoningAI(ctx context.Context, synth Synthesizer, workers []*Result, question, decision string) (*MergedReasoning, error) {
	if synth == nil {
		return nil, fmt.Errorf("no synthesizer available for AI extraction")
	}

	// Build prompt with worker reasonings for conversational synthesis
	var promptBuilder strings.Builder
	promptBuilder.WriteString(`You are "The Council" - a wise gathering of AI advisors helping users with their questions.
Multiple council members have deliberated and reached consensus. Your task is to synthesize their reasoning into a single, natural conversational response.

`)
	promptBuilder.WriteString(fmt.Sprintf("User's Question: %s\n\n", question))
	promptBuilder.WriteString(fmt.Sprintf("Council's Decision: %s\n\n", decision))
	promptBuilder.WriteString("Council Members' Reasoning:\n")

	for i, worker := range workers {
		if worker.Response == nil || worker.Response.Reasoning == "" {
			continue
		}
		promptBuilder.WriteString(fmt.Sprintf("Councillor %d (confidence: %.0f%%):\n%s\n\n",
			i+1, worker.Response.Confidence*100, worker.Response.Reasoning))
	}

	promptBuilder.WriteString(`Create a unified response that:
1. Sounds like a single wise advisor speaking naturally
2. Incorporates the best insights from each council member
3. Is conversational and helpful (like ChatGPT)
4. Addresses the user directly
5. Is concise but thorough

Return ONLY valid JSON:
{
  "conversational_response": "Your natural, conversational response to the user that synthesizes all council reasoning into one cohesive answer",
  "summary": "Brief 1-2 sentence summary of the key points",
  "contributions": [
    {"worker_id": "councillor-1", "text": "key insight from this member", "order": 1}
  ]
}`)

	// Call the synthesizer
	response, err := synth.Complete(ctx, promptBuilder.String())
	if err != nil {
		return nil, fmt.Errorf("AI extraction failed: %w", err)
	}

	// Parse the JSON response
	response = cleanJSONResponse(response)

	var aiResult struct {
		ConversationalResponse string `json:"conversational_response"`
		Summary                string `json:"summary"`
		Contributions          []struct {
			WorkerID string `json:"worker_id"`
			Text     string `json:"text"`
			Order    int    `json:"order"`
		} `json:"contributions"`
	}

	if err := json.Unmarshal([]byte(response), &aiResult); err != nil {
		return nil, fmt.Errorf("failed to parse AI response: %w", err)
	}

	// Build worker confidence map for lookup
	confidenceMap := make(map[string]float64)
	workerIDMap := make(map[int]string) // Map councillor number to actual worker ID
	for i, worker := range workers {
		if worker.Response != nil {
			confidenceMap[worker.WorkerID] = worker.Response.Confidence
			workerIDMap[i+1] = worker.WorkerID
		}
	}

	// Convert to MergedReasoning with proper worker IDs
	contributions := make([]ReasoningContribution, 0, len(aiResult.Contributions))
	for _, c := range aiResult.Contributions {
		// Try to extract councillor number from worker_id like "councillor-1"
		actualWorkerID := c.WorkerID
		if strings.HasPrefix(c.WorkerID, "councillor-") {
			numStr := strings.TrimPrefix(c.WorkerID, "councillor-")
			if num, err := strconv.Atoi(numStr); err == nil {
				if realID, ok := workerIDMap[num]; ok {
					actualWorkerID = realID
				}
			}
		}

		contributions = append(contributions, ReasoningContribution{
			WorkerID:   actualWorkerID,
			Text:       c.Text,
			Confidence: confidenceMap[actualWorkerID],
			Order:      c.Order,
		})
	}

	return &MergedReasoning{
		Summary:                aiResult.Summary,
		Contributions:          contributions,
		WorkerCount:            len(workers),
		SynthesisType:          "ai",
		ConversationalResponse: aiResult.ConversationalResponse,
	}, nil
}

// IsLowQualityMergedReasoning checks if the algorithmic result needs AI fallback.
func IsLowQualityMergedReasoning(result *MergedReasoning) bool {
	if result == nil {
		return true
	}
	// Low quality if less than 2 contributions or short summary
	if len(result.Contributions) < 2 {
		return true
	}
	if len(result.Summary) < 50 {
		return true
	}
	// Check for empty contributions
	for _, c := range result.Contributions {
		if c.Text == "" {
			return true
		}
	}
	return false
}

// ExtractMergedReasoning extracts merged reasoning using an LLM-first approach
// for conversational responses, falling back to algorithmic extraction.
func ExtractMergedReasoning(ctx context.Context, synth Synthesizer, workers []*Result, question, decision string) *MergedReasoning {
	// Always try AI extraction first for conversational responses
	log.Printf("[consensus] Using LLM for conversational reasoning synthesis")
	aiResult, err := extractMergedReasoningAI(ctx, synth, workers, question, decision)
	if err != nil {
		log.Printf("[consensus] AI reasoning extraction failed: %v, falling back to algorithmic", err)
		// Fall back to algorithmic extraction if AI fails
		result, algErr := ExtractMergedReasoningAlgorithmic(workers)
		if algErr != nil {
			log.Printf("[consensus] Algorithmic extraction also failed: %v", algErr)
			return nil
		}
		return result
	}

	return aiResult
}
