package consensus

import (
	"math"
	"testing"
)

func TestNormalizeText(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"lowercases", "Hello World", "hello world"},
		{"strips punctuation", "yes, it works!", "yes works"},
		{"removes stop words", "the answer is in the box", "answer box"},
		{"empty", "", ""},
		{"only stop words", "the a an of", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeText(tt.in); got != tt.want {
				t.Errorf("NormalizeText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCalculateSimilarity(t *testing.T) {
	t.Run("identical texts score 1.0", func(t *testing.T) {
		if got := CalculateSimilarity("approve change", "approve change"); math.Abs(got-1.0) > 1e-9 {
			t.Errorf("got %v, want 1.0", got)
		}
	})
	t.Run("both empty score 1.0", func(t *testing.T) {
		if got := CalculateSimilarity("", ""); got != 1.0 {
			t.Errorf("got %v, want 1.0", got)
		}
	})
	t.Run("disjoint texts score 0", func(t *testing.T) {
		if got := CalculateSimilarity("apple banana", "car door"); got != 0 {
			t.Errorf("got %v, want 0", got)
		}
	})
	t.Run("symmetric", func(t *testing.T) {
		a, b := "the quick brown fox", "quick brown dog"
		if CalculateSimilarity(a, b) != CalculateSimilarity(b, a) {
			t.Error("similarity is not symmetric")
		}
	})
	t.Run("partial overlap is between 0 and 1", func(t *testing.T) {
		got := CalculateSimilarity("approve this change", "approve that change")
		if got <= 0 || got >= 1 {
			t.Errorf("got %v, want in (0,1)", got)
		}
	})
}

func TestSplitIntoSentences(t *testing.T) {
	got := SplitIntoSentences("This is sentence one. This is sentence two! Is this three? tiny.")
	if len(got) != 3 {
		t.Fatalf("got %d sentences (%v), want 3 (short fragment dropped)", len(got), got)
	}
	if got[0] != "This is sentence one" {
		t.Errorf("first sentence = %q", got[0])
	}
	if SplitIntoSentences("") != nil {
		t.Error("empty input should return nil")
	}
}

func TestParseNumericValue(t *testing.T) {
	tests := []struct {
		in      string
		want    float64
		wantErr bool
	}{
		{"42", 42, false},
		{"$1,234.56", 1234.56, false},
		{"-3.5", -3.5, false},
		{"about 100 units", 100, false},
		{"no numbers here", 0, true},
		{"", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseNumericValue(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseNumericValue(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseNumericValue(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestCleanJSONResponse(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", `{"a":1}`, `{"a":1}`},
		{"json fence", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"bare fence", "```\n{\"a\":1}\n```", `{"a":1}`},
		{"whitespace", "  {\"a\":1}  ", `{"a":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanJSONResponse(tt.in); got != tt.want {
				t.Errorf("cleanJSONResponse(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
