package moderation

import "testing"

func TestNormalizeBlacklistWord(t *testing.T) {
	word, err := (Create{Word: "  ЁЖ  "}).Normalize()
	if err != nil || word != "ёж" {
		t.Fatal(word, err)
	}
	for _, value := range []string{"", "two words", "bad!", "a/b"} {
		if _, err := (Create{Word: value}).Normalize(); err == nil {
			t.Fatal("invalid blacklist entry accepted", value)
		}
	}
	words := Words("Это ЁЖ, но не ёжик")
	if len(words) != 5 || words[1] != "ёж" || words[4] != "ёжик" {
		t.Fatal(words)
	}
}
