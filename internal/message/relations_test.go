package message

import "testing"

// TestReactionVocabulary guards canonical uniqueness and the distinction between
// one displayed emoji and an arbitrary run of emoji-looking codepoints.
func TestReactionVocabulary(t *testing.T) {
	for input, want := range map[string]string{"❤": "❤️", "❤️": "❤️", "👍🏽": "👍🏽", "👩‍💻": "👩‍💻", "🇷🇺": "🇷🇺", "1⃣": "1️⃣", "👨‍👩‍👧‍👦": "👨‍👩‍👧‍👦"} {
		got, err := NormalizeReaction(input)
		if err != nil || got != want {
			t.Errorf("normalize %q: %q %v", input, got, err)
		}
	}
	for _, input := range []string{"", "hello", "😀😀", "😀 text", "1", "🏽", "🇷", "👩‍", "❤︎", " 😀", "\xff"} {
		if _, err := NormalizeReaction(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	for alias, canonical := range reactionEmoji {
		if got, err := NormalizeReaction(canonical); err != nil || got != canonical {
			t.Fatalf("noncanonical target for %q", alias)
		}
	}
}
