package knowledge

import (
	"strings"
	"unicode"
)

// Topic overlap alone cannot establish that a procedure answers a definition.
// These explicit question forms require explanatory evidence in the source;
// an unknown answer is safer than returning unrelated instructions.
func isDefinitionRequest(query string) bool {
	for _, phrase := range []string{"что такое", "что означает", "что значит", "объясните термин", "объясни термин", "расшифру", "расшифров", "дайте определение", "дать определение", "what is ", "what are ", "define ", "definition of ", "meaning of "} {
		if strings.Contains(query, phrase) {
			return true
		}
	}
	return strings.Contains(query, "what does ") && strings.Contains(query, " mean")
}

func explainsRequestedTopic(document Document, query string, queryWords []string) bool {
	terms := append(append([]string{}, document.Keywords...), strings.Fields(strings.ToLower(document.Title))...)
	topics := []string{}
	for _, term := range terms {
		term = strings.ToLower(strings.Trim(term, ".,!?():;\"'"))
		if len([]rune(term)) >= 3 && !isStopWord(term) && matchesTerm(query, queryWords, term) {
			topics = append(topics, term)
		}
	}
	// A definition of another topic elsewhere in the same article is not evidence.
	for _, sentence := range strings.FieldsFunc(strings.ToLower(document.Content), func(r rune) bool { return r == '.' || r == '\n' || r == '!' || r == '?' }) {
		sentence = strings.TrimSpace(sentence)
		if strings.HasPrefix(sentence, "#") || !hasExplanationRelation(sentence) {
			continue
		}
		words := strings.FieldsFunc(sentence, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		for _, topic := range topics {
			if matchesTerm(sentence, words, topic) {
				return true
			}
		}
	}
	return false
}

func hasExplanationRelation(sentence string) bool {
	for _, relation := range []string{" — ", " – ", " - ", " это ", " означает ", " значит ", " расшифровывается ", " представляет собой ", " stands for ", " is a ", " is an ", " refers to "} {
		if strings.Contains(sentence, relation) {
			return true
		}
	}
	return false
}
