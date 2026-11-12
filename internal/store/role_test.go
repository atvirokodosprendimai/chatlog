package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestRoleMatchKeepsTheCandidatesWhoseRoleColumnHoldsAWordAndTheirRankingOrder(t *testing.T) {
	ctx, s := context.Background(), open(t)
	at := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	for i, r := range []Row{
		{UUID: "u", Words: "zebra here", Answer: "nothing"},
		{UUID: "a", Words: "nothing", Answer: "zebra here"},
		{UUID: "n", Words: "nothing", Answer: "nothing"},
	} {
		r.Session, r.Source, r.Time, r.End, r.Project, r.Sha, r.Text = "s", "f", at, at, "p", r.UUID, r.Words+" "+r.Answer
		if err := s.Apply(ctx, "f"+r.UUID, []Row{r}, Cursor{}); err != nil {
			t.Fatal(i, err)
		}
	}
	// ids are 1 (u), 2 (a), 3 (n); the order given is the caller's ranking
	if got, err := s.RoleMatch(ctx, []int64{3, 2, 1}, "zebra", "user"); err != nil || !reflect.DeepEqual(got, []int64{1}) {
		t.Fatalf("user: %v %v", got, err)
	}
	if got, _ := s.RoleMatch(ctx, []int64{3, 2, 1}, "zebra", "assistant"); !reflect.DeepEqual(got, []int64{2}) {
		t.Fatalf("assistant: %v", got)
	}
	if got, _ := s.RoleMatch(ctx, []int64{3, 2, 1}, "zebra", ""); !reflect.DeepEqual(got, []int64{3, 2, 1}) {
		t.Fatalf("no role keeps all, as given: %v", got)
	}
	if got, err := s.RoleMatch(ctx, []int64{1, 2}, "!!", "user"); err != nil || len(got) != 0 {
		t.Fatalf("a query with no words matches nothing under a role, and is not an error here: %v %v", got, err)
	}
}

func TestParseRole(t *testing.T) {
	for in, want := range map[string]string{"": "", "any": "", "user": "user", "assistant": "assistant"} {
		if got, err := ParseRole(in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"system", "User", "ANY", " user"} {
		if _, err := ParseRole(in); err == nil {
			t.Errorf("%q is not a role", in)
		}
	}
}
