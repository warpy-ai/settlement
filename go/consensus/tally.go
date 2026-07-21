package consensus

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"time"
)

// Tally determines whether the given results reach consensus under cfg.
// question is the original task content (used for merge synthesis and
// reasoning extraction). On success it returns true and the winning Response
// encoded as JSON. A nil Synthesizer runs fully offline.
func Tally(ctx context.Context, question string, results []Result, cfg Config, synth Synthesizer) (bool, string) {
	if len(results) == 0 {
		return false, ""
	}

	// Handle merge_match strategy: merge all responses for subjective questions
	if cfg.MatchStrategy == MergeMatch {
		return Merge(ctx, question, results, cfg, synth)
	}

	// Group responses by decision and calculate weighted votes
	type consensusGroup struct {
		totalVotes  float64
		responses   []*Result
		confidence  float64
		rateLimited bool
	}

	groups := make(map[string]*consensusGroup)
	totalVotingPower := 0.0
	rateLimitedCount := 0

	// First pass: Create initial groups
	for _, result := range results {
		if result.Response == nil {
			continue
		}

		// Check for rate limit errors
		if result.Response.Metadata != nil {
			if errorVal, ok := result.Response.Metadata["error"].(string); ok && strings.Contains(errorVal, "Rate limit reached") {
				rateLimitedCount++
				continue
			}
		}

		key := result.Response.Decision
		switch cfg.MatchStrategy {
		case NumericMatch:
			// For numeric results, group within tolerance
			value, err := ParseNumericValue(result.Response.Decision)
			if err != nil {
				log.Printf("[consensus] Failed to parse numeric value: %v", err)
				continue
			}
			found := false
			for existingKey := range groups {
				existingValue, _ := ParseNumericValue(existingKey)
				if math.Abs(value-existingValue) <= cfg.NumericTolerance {
					key = existingKey
					found = true
					break
				}
			}
			if !found {
				key = result.Response.Decision
			}

		case SemanticMatch:
			// For semantic match, normalize and find similar groups
			normalizedKey := NormalizeText(key)
			bestMatch := key
			highestSimilarity := 0.0

			for existingKey := range groups {
				similarity := CalculateSimilarity(normalizedKey, NormalizeText(existingKey))
				if similarity >= 0.6 && similarity > highestSimilarity { // Lowered threshold
					bestMatch = existingKey
					highestSimilarity = similarity
				}
			}
			key = bestMatch
		}

		weightedVote := result.VotingPower * result.Response.Confidence
		if group, exists := groups[key]; exists {
			group.totalVotes += weightedVote
			group.responses = append(group.responses, &result)
			group.confidence = (group.confidence*float64(len(group.responses)-1) + result.Response.Confidence) / float64(len(group.responses))
		} else {
			groups[key] = &consensusGroup{
				totalVotes: weightedVote,
				responses:  []*Result{&result},
				confidence: result.Response.Confidence,
			}
		}
		totalVotingPower += result.VotingPower
	}

	// Second pass: Merge very similar groups
	if cfg.MatchStrategy == SemanticMatch {
		merged := true
		for merged {
			merged = false
			for key1, group1 := range groups {
				for key2, group2 := range groups {
					if key1 == key2 {
						continue
					}
					if similarity := CalculateSimilarity(NormalizeText(key1), NormalizeText(key2)); similarity >= 0.8 {
						// Merge group2 into group1
						group1.totalVotes += group2.totalVotes
						group1.responses = append(group1.responses, group2.responses...)
						group1.confidence = (group1.confidence*float64(len(group1.responses)-len(group2.responses)) +
							group2.confidence*float64(len(group2.responses))) / float64(len(group1.responses))
						delete(groups, key2)
						merged = true
						break
					}
				}
				if merged {
					break
				}
			}
		}
	}

	// If too many rate limits, return false to retry
	if float64(rateLimitedCount)/float64(len(results)) > 0.5 {
		log.Printf("[consensus] Too many rate limited responses (%d/%d), will retry",
			rateLimitedCount, len(results))
		return false, ""
	}

	// Find the group with the highest weighted votes
	var bestResult string
	var highestVotes float64
	var bestGroup *consensusGroup
	var secondHighestVotes float64

	for decision, group := range groups {
		weightedVotes := group.totalVotes / totalVotingPower
		log.Printf("[consensus] Group '%s' has %.2f%% agreement (confidence: %.2f)",
			decision, weightedVotes*100, group.confidence)

		if weightedVotes > highestVotes {
			secondHighestVotes = highestVotes
			highestVotes = weightedVotes
			bestResult = decision
			bestGroup = group
		} else if weightedVotes > secondHighestVotes {
			secondHighestVotes = weightedVotes
		}
	}

	// Check if the highest vote percentage is significantly higher than the second highest
	// This ensures we have a clear winner
	voteDifference := highestVotes - secondHighestVotes
	hasSignificantLead := voteDifference >= 0.1 // At least 10% higher than the next best

	// Adjust minimum agreement based on number of groups
	adjustedMinAgreement := cfg.MinimumAgreement
	if len(groups) > 2 {
		// Lower the threshold when there are many similar valid answers
		adjustedMinAgreement *= 0.6
	}

	// Accept the result if it meets the minimum agreement OR has a significant lead
	if bestGroup != nil && (highestVotes >= adjustedMinAgreement || hasSignificantLead) {
		// Start parallel extraction of merged reasoning if enabled
		var mergedReasoningChan chan *MergedReasoning
		if cfg.ExtractMergedReasoning && len(bestGroup.responses) > 1 {
			mergedReasoningChan = make(chan *MergedReasoning, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), mergedReasoningTimeout)
				defer cancel()
				mergedReasoningChan <- ExtractMergedReasoning(
					ctx,
					synth,
					bestGroup.responses,
					question,
					bestResult,
				)
			}()
		}

		consensusResponse := &Response{
			Decision:   bestResult,
			Confidence: bestGroup.confidence,
			Category:   bestGroup.responses[0].Response.Category,
			Reasoning: fmt.Sprintf("Consensus reached with %.2f%% agreement among %d workers (lead: %.2f%%)",
				highestVotes*100, len(bestGroup.responses), voteDifference*100),
			Metadata: map[string]interface{}{
				"consensus_strategy":  string(cfg.MatchStrategy),
				"worker_count":        len(results),
				"agreeing_workers":    len(bestGroup.responses),
				"agreement_threshold": adjustedMinAgreement,
				"actual_agreement":    highestVotes,
				"vote_difference":     voteDifference,
				"total_groups":        len(groups),
			},
		}

		// Collect alternative answers from other groups
		for decision, group := range groups {
			if decision != bestResult {
				consensusResponse.Alternatives = append(
					consensusResponse.Alternatives,
					fmt.Sprintf("%s (%.2f%% agreement, confidence: %.2f)",
						decision, (group.totalVotes/totalVotingPower)*100, group.confidence),
				)
			}
		}

		// Wait for merged reasoning extraction with timeout
		if mergedReasoningChan != nil {
			select {
			case merged := <-mergedReasoningChan:
				consensusResponse.MergedReasoning = merged
			case <-time.After(mergedReasoningTimeout):
				log.Printf("[consensus] Merged reasoning extraction timed out, falling back to algorithmic")
				if extracted, err := ExtractMergedReasoningAlgorithmic(bestGroup.responses); err == nil {
					consensusResponse.MergedReasoning = extracted
				}
			}
		}

		resultBytes, err := json.Marshal(consensusResponse)
		if err != nil {
			log.Printf("[consensus] Failed to marshal consensus response: %v", err)
			return false, ""
		}

		return true, string(resultBytes)
	}

	return false, ""
}
