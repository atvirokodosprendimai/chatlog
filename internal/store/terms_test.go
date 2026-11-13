package store

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTermsAreUniqueAndBounded(t *testing.T) {
	if got := Terms("zebra Zebra zebra horse zebra"); !reflect.DeepEqual(got, []string{"zebra", "Zebra", "horse"}) {
		t.Fatalf("a repeated word is one term (the match is OR, a repeat adds nothing): %v", got)
	}
	var words []string
	for i := 0; i < 1000; i++ {
		words = append(words, "w"+string(rune('a'+i%26))+string(rune('a'+i/26%26)))
	}
	if got := Terms(strings.Join(words, " ")); len(got) != MaxTerms {
		t.Fatalf("%d terms, want the first %d", len(got), MaxTerms)
	}
	start := time.Now()
	Terms(strings.Repeat("a ", 100000))
	if d := time.Since(start); d > time.Second {
		t.Fatalf("100000 repeats took %v", d)
	}
}
