package consensus

import (
	"strconv"
	"strings"
	"unicode"
)

// NormalizeText removes punctuation, extra spaces, and converts to lowercase.
func NormalizeText(text string) string {
	// Convert to lowercase
	text = strings.ToLower(text)

	// Remove punctuation and extra spaces
	text = strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) {
			return ' '
		}
		return r
	}, text)

	// Split into words and remove common words that don't affect meaning
	words := strings.Fields(text)
	filtered := make([]string, 0, len(words))
	stopWords := map[string]bool{
		"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
		"be": true, "by": true, "for": true, "in": true, "is": true, "it": true,
		"of": true, "on": true, "or": true, "that": true, "the": true, "this": true,
		"to": true, "was": true, "were": true, "will": true, "with": true,
	}

	for _, word := range words {
		if !stopWords[word] {
			filtered = append(filtered, word)
		}
	}

	return strings.Join(filtered, " ")
}

// CalculateSimilarity returns a similarity score between 0 and 1.
func CalculateSimilarity(text1, text2 string) float64 {
	words1 := strings.Fields(text1)
	words2 := strings.Fields(text2)

	// Create word frequency maps
	freq1 := make(map[string]int)
	freq2 := make(map[string]int)

	for _, word := range words1 {
		freq1[word]++
	}
	for _, word := range words2 {
		freq2[word]++
	}

	// Calculate intersection and union using word frequencies
	intersection := 0.0
	union := 0.0

	// Count intersection
	for word, count1 := range freq1 {
		if count2, exists := freq2[word]; exists {
			intersection += float64(min(count1, count2))
		}
		union += float64(count1)
	}

	// Add remaining words from freq2 to union
	for word, count2 := range freq2 {
		if _, exists := freq1[word]; !exists {
			union += float64(count2)
		}
	}

	if union == 0 {
		return 1.0
	}

	// Weight longer matches more heavily
	lengthFactor := float64(min(len(words1), len(words2))) / float64(max(len(words1), len(words2)))
	similarity := (intersection / union) * (0.7 + 0.3*lengthFactor)

	return similarity
}

// SplitIntoSentences splits text into sentences for reasoning extraction.
func SplitIntoSentences(text string) []string {
	// Simple sentence splitting on common delimiters
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	// Replace common sentence-ending patterns with a delimiter
	delimiters := []string{". ", "! ", "? ", ".\n", "!\n", "?\n"}
	for _, d := range delimiters {
		text = strings.ReplaceAll(text, d, "|||")
	}

	parts := strings.Split(text, "|||")
	sentences := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if len(part) > 10 { // Skip very short fragments
			sentences = append(sentences, part)
		}
	}
	return sentences
}

// ParseNumericValue attempts to extract a numeric value from a string.
func ParseNumericValue(s string) (float64, error) {
	// Remove any currency symbols, commas, and other non-numeric characters
	s = strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) || r == '.' || r == '-' {
			return r
		}
		return -1
	}, s)

	return strconv.ParseFloat(s, 64)
}

// cleanJSONResponse removes markdown code blocks from an LLM response.
func cleanJSONResponse(content string) string {
	content = strings.TrimSpace(content)

	// Remove markdown code blocks (```json ... ``` or ``` ... ```)
	if strings.HasPrefix(content, "```") {
		// Find the first newline after ``` (or ```json, ```json, etc.)
		lines := strings.Split(content, "\n")
		if len(lines) > 0 {
			// Skip the first line (which contains ``` or ```json)
			if len(lines) > 1 {
				content = strings.Join(lines[1:], "\n")
			} else {
				// Single line, just remove the ```
				content = strings.TrimPrefix(content, "```")
			}
		}

		// Remove trailing ``` (might be on its own line or at end of last line)
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}

	return strings.TrimSpace(content)
}
