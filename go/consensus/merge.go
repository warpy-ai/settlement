package consensus

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// Merge synthesizes all voter responses into a single unified consensus answer
// (the merge_match strategy for subjective questions). On success it returns
// true and the merged Response encoded as JSON.
func Merge(ctx context.Context, question string, results []Result, cfg Config, synth Synthesizer) (bool, string) {
	if len(results) == 0 {
		return false, ""
	}

	// Collect all valid responses
	validResponses := make([]*Result, 0)
	totalVotingPower := 0.0
	totalConfidence := 0.0
	allAlternatives := make(map[string]bool) // Use map to deduplicate

	var responsesText strings.Builder
	responsesText.WriteString("The following are responses from multiple AI workers to the question: ")
	responsesText.WriteString(question)
	responsesText.WriteString("\n\n")

	for i, result := range results {
		if result.Response == nil {
			continue
		}

		// Skip rate-limited responses
		if result.Response.Metadata != nil {
			if errorVal, ok := result.Response.Metadata["error"].(string); ok && strings.Contains(errorVal, "Rate limit reached") {
				continue
			}
		}

		validResponses = append(validResponses, &result)
		totalVotingPower += result.VotingPower
		totalConfidence += result.Response.Confidence * result.VotingPower

		// Build response text for synthesis
		responsesText.WriteString(fmt.Sprintf("Worker %d:\n", i+1))
		responsesText.WriteString(fmt.Sprintf("  Answer: %s\n", result.Response.Decision))
		responsesText.WriteString(fmt.Sprintf("  Reasoning: %s\n", result.Response.Reasoning))
		if len(result.Response.Alternatives) > 0 {
			responsesText.WriteString(fmt.Sprintf("  Alternatives: %s\n", strings.Join(result.Response.Alternatives, ", ")))
		}
		responsesText.WriteString("\n")

		// Collect alternatives
		for _, alt := range result.Response.Alternatives {
			allAlternatives[alt] = true
		}
	}

	if len(validResponses) == 0 {
		return false, ""
	}

	// Calculate average confidence
	avgConfidence := totalConfidence / totalVotingPower

	// First, try voting-based approach for deterministic consensus
	decisionVotes := make(map[string]float64)         // decision -> weighted votes
	decisionConfidences := make(map[string][]float64) // decision -> list of confidences

	for _, result := range validResponses {
		decision := result.Response.Decision
		weightedVote := result.VotingPower * result.Response.Confidence
		decisionVotes[decision] += weightedVote
		decisionConfidences[decision] = append(decisionConfidences[decision], result.Response.Confidence)
	}

	// Find the decision with the most votes
	bestDecision := ""
	maxVotes := 0.0
	totalVotes := 0.0
	for decision, votes := range decisionVotes {
		totalVotes += votes
		if votes > maxVotes {
			maxVotes = votes
			bestDecision = decision
		}
	}

	// If we have a clear winner (at least 40% of votes), use it directly
	useVoting := totalVotes > 0 && (maxVotes/totalVotes >= 0.4 || len(decisionVotes) == 1)

	if !useVoting {
		// Votes are split - use AI to synthesize a deterministic answer
		log.Printf("[consensus] Votes are split (%.1f%% for top answer), using AI synthesis", (maxVotes/totalVotes)*100)

		synthesisPrompt := fmt.Sprintf(`You are a consensus synthesizer. Multiple AI workers have provided different answers. You MUST pick ONE definitive answer.

CRITICAL RULES:
- Pick ONE answer definitively - do NOT say "it depends" or explain subjectivity
- Count which answer appears most frequently
- If similar answers exist, pick the most common one
- Be direct and concise (1-2 sentences maximum)
- Answer the question directly without hedging

Question: %s

Worker answers and their vote weights:
%s

Provide ONLY the definitive answer. No explanations about subjectivity. Just the answer.`, question, func() string {
			var votesText strings.Builder
			for decision, votes := range decisionVotes {
				avgConf := 0.0
				if confs, ok := decisionConfidences[decision]; ok && len(confs) > 0 {
					for _, c := range confs {
						avgConf += c
					}
					avgConf /= float64(len(confs))
				}
				votesText.WriteString(fmt.Sprintf("- %.1f%% votes: %s (avg confidence: %.2f)\n", (votes/totalVotes)*100, decision, avgConf))
			}
			return votesText.String()
		}())

		// Synthesize via the injected LLM hook
		if synth == nil {
			log.Printf("[consensus] Cannot synthesize consensus: no synthesizer available")
			return fallbackMerge(ctx, validResponses, allAlternatives, avgConfidence, question, cfg, synth)
		}

		synthesizedDecision, err := synth.Complete(ctx, synthesisPrompt)
		if err != nil {
			log.Printf("[consensus] Failed to synthesize consensus: %v", err)
			return fallbackMerge(ctx, validResponses, allAlternatives, avgConfidence, question, cfg, synth)
		}

		// Clean the synthesized response
		bestDecision = cleanJSONResponse(synthesizedDecision)

		// Validate synthesized answer
		if len(bestDecision) < 5 || len(bestDecision) > 500 {
			log.Printf("[consensus] Synthesized answer invalid, using fallback")
			return fallbackMerge(ctx, validResponses, allAlternatives, avgConfidence, question, cfg, synth)
		}
	} else {
		log.Printf("[consensus] Clear consensus winner found via voting: %.2f%% votes", (maxVotes/totalVotes)*100)
	}

	// Build merged reasoning that explains the consensus
	var mergedReasoning string
	if useVoting {
		mergedReasoning = fmt.Sprintf("Consensus reached from %d workers via voting (%.1f%% agreement). Average confidence: %.2f", len(validResponses), (maxVotes/totalVotes)*100, avgConfidence)
	} else {
		mergedReasoning = fmt.Sprintf("Consensus synthesized from %d workers. The unified answer incorporates common themes and agreements from all responses. Average confidence: %.2f", len(validResponses), avgConfidence)
	}

	// Determine category (use most common)
	categoryCount := make(map[string]int)
	for _, result := range validResponses {
		categoryCount[result.Response.Category]++
	}
	mostCommonCategory := "general"
	maxCount := 0
	for cat, count := range categoryCount {
		if count > maxCount {
			maxCount = count
			mostCommonCategory = cat
		}
	}

	// Convert alternatives map to slice
	alternatives := make([]string, 0, len(allAlternatives))
	for alt := range allAlternatives {
		alternatives = append(alternatives, alt)
	}

	synthesisType := "voting"
	if !useVoting {
		synthesisType = "ai_synthesized"
	}

	// Start parallel extraction of merged reasoning if enabled
	var mergedReasoningChan chan *MergedReasoning
	if cfg.ExtractMergedReasoning && len(validResponses) > 1 {
		mergedReasoningChan = make(chan *MergedReasoning, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), mergedReasoningTimeout)
			defer cancel()
			mergedReasoningChan <- ExtractMergedReasoning(
				ctx,
				synth,
				validResponses,
				question,
				bestDecision,
			)
		}()
	}

	consensusResponse := &Response{
		Decision:   bestDecision,
		Confidence: avgConfidence,
		Category:   mostCommonCategory,
		Reasoning:  mergedReasoning,
		Metadata: map[string]interface{}{
			"consensus_strategy": "merge_match",
			"worker_count":       len(validResponses),
			"synthesis_type":     synthesisType,
			"vote_percentage":    (maxVotes / totalVotes) * 100,
		},
		Alternatives: alternatives,
	}

	// Wait for merged reasoning extraction with timeout
	if mergedReasoningChan != nil {
		select {
		case extracted := <-mergedReasoningChan:
			consensusResponse.MergedReasoning = extracted
		case <-time.After(mergedReasoningTimeout):
			log.Printf("[consensus] Merged reasoning extraction timed out in Merge, falling back to algorithmic")
			if extracted, err := ExtractMergedReasoningAlgorithmic(validResponses); err == nil {
				consensusResponse.MergedReasoning = extracted
			}
		}
	}

	resultBytes, err := json.Marshal(consensusResponse)
	if err != nil {
		log.Printf("[consensus] Failed to marshal merged consensus response: %v", err)
		return false, ""
	}

	log.Printf("[consensus] Synthesized consensus from %d workers into single unified answer", len(validResponses))
	return true, string(resultBytes)
}

// fallbackMerge provides a simple merge when AI synthesis fails.
func fallbackMerge(ctx context.Context, validResponses []*Result, allAlternatives map[string]bool, avgConfidence float64, question string, cfg Config, synth Synthesizer) (bool, string) {
	// Find most common decision using semantic similarity
	decisionCount := make(map[string]int)
	for _, result := range validResponses {
		normalized := NormalizeText(result.Response.Decision)
		// Try to find similar existing decisions
		found := false
		for existing := range decisionCount {
			if CalculateSimilarity(normalized, NormalizeText(existing)) >= 0.7 {
				decisionCount[existing]++
				found = true
				break
			}
		}
		if !found {
			decisionCount[result.Response.Decision]++
		}
	}

	// Get the most common decision
	bestDecision := ""
	maxCount := 0
	for decision, count := range decisionCount {
		if count > maxCount {
			maxCount = count
			bestDecision = decision
		}
	}

	if bestDecision == "" && len(validResponses) > 0 {
		bestDecision = validResponses[0].Response.Decision
	}

	// Determine category
	categoryCount := make(map[string]int)
	for _, result := range validResponses {
		categoryCount[result.Response.Category]++
	}
	mostCommonCategory := "general"
	maxCatCount := 0
	for cat, count := range categoryCount {
		if count > maxCatCount {
			maxCatCount = count
			mostCommonCategory = cat
		}
	}

	alternatives := make([]string, 0, len(allAlternatives))
	for alt := range allAlternatives {
		alternatives = append(alternatives, alt)
	}

	consensusResponse := &Response{
		Decision:   bestDecision,
		Confidence: avgConfidence,
		Category:   mostCommonCategory,
		Reasoning:  fmt.Sprintf("Consensus synthesized from %d workers (fallback merge)", len(validResponses)),
		Metadata: map[string]interface{}{
			"consensus_strategy": "merge_match",
			"worker_count":       len(validResponses),
			"synthesis_type":     "fallback",
		},
		Alternatives: alternatives,
	}

	// Extract merged reasoning using algorithmic method only (fast fallback)
	if cfg.ExtractMergedReasoning && len(validResponses) > 1 {
		ctx, cancel := context.WithTimeout(ctx, mergedReasoningTimeout)
		defer cancel()
		if extracted := ExtractMergedReasoning(ctx, synth, validResponses, question, bestDecision); extracted != nil {
			consensusResponse.MergedReasoning = extracted
		} else if alg, err := ExtractMergedReasoningAlgorithmic(validResponses); err == nil {
			consensusResponse.MergedReasoning = alg
		}
	}

	resultBytes, err := json.Marshal(consensusResponse)
	if err != nil {
		return false, ""
	}

	return true, string(resultBytes)
}
