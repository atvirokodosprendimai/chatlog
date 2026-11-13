package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/store"
	"github.com/atvirokodosprendimai/chatlog/internal/tei"
	"github.com/atvirokodosprendimai/chatlog/internal/vector"
)

// midnight is the start of 2026-11-12 where the test runs: the day edge until and since are read at.
var midnight = time.Date(2026, 11, 12, 0, 0, 0, 0, time.Local)

// seeded has four exchanges; the words and the answer say "zebra" in different places. ids are 1 to 4 in this order.
func seeded(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	put := func(uuid string, at time.Time, words, answer string) {
		r := store.Row{UUID: uuid, Session: "s", Source: "f", Time: at, End: at, Project: "p", Words: words, Answer: answer, Sha: uuid, Text: words + "\n" + answer}
		if err := st.Apply(context.Background(), "f-"+uuid, []store.Row{r}, store.Cursor{}); err != nil {
			t.Fatal(err)
		}
	}
	put("m", midnight.AddDate(0, 0, -2).Add(10*time.Hour), "zebra in the user's words", "an answer without it")
	put("a", midnight.AddDate(0, 0, -1).Add(10*time.Hour), "plain question", "zebra in the answer")
	put("b", midnight.Add(10*time.Hour), "zebra again in the user's words", "and zebra in the answer")
	put("z", midnight, "zebra exactly at midnight", "nothing")
	return &Service{Store: st}
}

func uuids(r Response) string {
	var u []string
	for _, h := range r.Hits {
		u = append(u, h.UUID)
	}
	slices.Sort(u)
	return strings.Join(u, ",")
}

func TestSearchRoleUserFindsOnlyTheUsersWordsAndAssistantOnlyTheAnswer(t *testing.T) {
	svc := seeded(t)
	ctx := context.Background()
	for role, want := range map[string]string{"": "a,b,m,z", "any": "a,b,m,z", "user": "b,m,z", "assistant": "a,b"} {
		got, err := svc.Search(ctx, Request{Query: "zebra", Mode: ModeFTS, Role: role})
		if err != nil || uuids(got) != want {
			t.Fatalf("role %q: %q want %q (%v)", role, uuids(got), want, err)
		}
	}
	got, _ := svc.Search(ctx, Request{Query: "zebra", Mode: ModeFTS, Role: "user"})
	if got.Role != "user" {
		t.Fatalf("the answer says which role it applied, so a client of an older server can tell it was not: %q", got.Role)
	}
}

func TestSearchRefusesWhatItCannotApplyAndSaysWhatToWrite(t *testing.T) {
	svc := seeded(t)
	ctx := context.Background()
	for name, c := range map[string]struct {
		r    Request
		want string
	}{
		"a role that is not one":    {Request{Query: "zebra", Mode: ModeFTS, Role: "system"}, "role"},
		"a role and no words":       {Request{Query: "!!", Mode: ModeFTS, Role: "user"}, "words"},
		"a bad until":               {Request{Query: "zebra", Mode: ModeFTS, Until: "tomorrow"}, "YYYY-MM-DD"},
		"a bad since":               {Request{Query: "zebra", Mode: ModeFTS, Since: "tomorrow"}, "YYYY-MM-DD"},
		"since after until":         {Request{Query: "zebra", Mode: ModeFTS, Since: "2026-11-12", Until: "2026-11-12"}, "until"},
		"since after until, by day": {Request{Query: "zebra", Mode: ModeFTS, Since: "2026-11-13", Until: "2026-11-12"}, "earlier"},
	} {
		if _, err := svc.Search(ctx, c.r); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error saying %q", name, err, c.want)
		}
	}
}

func TestSearchUntilPairsWithSinceAsAHalfOpenRange(t *testing.T) {
	svc := seeded(t)
	ctx := context.Background()
	got, err := svc.Search(ctx, Request{Query: "zebra", Mode: ModeFTS, Since: "2026-11-11", Until: "2026-11-12"})
	if err != nil || uuids(got) != "a" {
		t.Fatalf("[11, 12): the 11th only, the row at exactly midnight of the 12th is out: %q %v", uuids(got), err)
	}
	if got, _ = svc.Search(ctx, Request{Query: "zebra", Mode: ModeFTS, Until: "2026-11-11"}); uuids(got) != "m" {
		t.Fatalf("until alone: %q", uuids(got))
	}
	// the stored time has whole seconds: a bound with a fraction must not move a row across it
	frac := midnight.Add(500 * time.Millisecond).Format(time.RFC3339Nano)
	if got, _ = svc.Search(ctx, Request{Query: "zebra", Mode: ModeFTS, Since: "2026-11-11", Until: frac}); uuids(got) != "a,z" {
		t.Fatalf("until a half second after midnight keeps the row at midnight: %q", uuids(got))
	}
}

func TestSearchByMeaningUnderARoleKeepsOnlyWhatSaysAWordThere(t *testing.T) {
	svc := seeded(t)
	var fake *httptest.Server
	fake = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Inputs []string `json:"inputs"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		out := make([][]float32, len(req.Inputs))
		for i := range out {
			out[i] = []float32{1, 0}
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer fake.Close()
	vec, err := vector.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for id := int64(1); id <= 4; id++ { // every exchange is a perfect semantic match: only the role can tell them apart
		if err := vec.Add(context.Background(), id, []float32{1, 0}); err != nil {
			t.Fatal(err)
		}
	}
	svc.Vec, svc.TEI = vec, tei.New(fake.URL, fake.URL)
	ctx := context.Background()
	got, err := svc.Search(ctx, Request{Query: "zebra", Mode: ModeVector, Role: "user"})
	if err != nil || uuids(got) != "b,m,z" || len(got.Degraded) != 0 {
		t.Fatalf("user: %q %v %v", uuids(got), got.Degraded, err)
	}
	if got, _ = svc.Search(ctx, Request{Query: "zebra", Mode: ModeVector, Role: "assistant"}); uuids(got) != "a,b" {
		t.Fatalf("assistant: %q", uuids(got))
	}
	if got, _ = svc.Search(ctx, Request{Query: "zebra", Mode: ModeVector}); uuids(got) != "a,b,m,z" {
		t.Fatalf("no role: every candidate: %q", uuids(got))
	}
}
