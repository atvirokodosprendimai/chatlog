// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package hook

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/config"
	"github.com/atvirokodosprendimai/chatlog/internal/know"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
	"github.com/atvirokodosprendimai/chatlog/internal/wake"
)

// Events the knowledge pieces write beside the hooks' own rows, so `chatlog events` shows them.
const (
	EventKarma    = "Karma"    // a situation recognised at a tool call: tool = the tool, input = sit/<g>; a situation never seen is a candidate to drop
	EventKarmaLib = "KarmaLib" // a library printed in a session: input = its name, so it is printed once
	EventCheck    = "Check"    // a turn's misses, input = the text; an empty input is the same cleared
)

// recallBudget is the longest a prompt waits for the notes' search, which needs the server's embedder. Past it the
// search is skipped and logged; the always-on notes and the situations are read from the file and never wait on it.
var recallBudget = 1500 * time.Millisecond

func (d Deps) kv(ctx context.Context, in Input, prefixes ...string) ([]store.KV, error) {
	return d.Store.KVRead(ctx, d.project(in), nil, prefixes, time.Time{})
}

// promptParts are what the knowledge pieces add to a prompt, in their order: notes, last turn's misses, context, recall.
func (d Deps) promptParts(ctx context.Context, in Input) (parts []string, errs []error) {
	h := d.Config.Hooks
	var kvs []store.KV
	if h.On(config.PieceNotes) || h.On(config.PiecePrompt) {
		var err error
		if kvs, err = d.kv(ctx, in, know.PromptPrefix, know.SitPrefix, know.CheckPrefix); err != nil {
			errs = append(errs, err)
		}
	}
	printed := map[string]bool{} // the owed situations the reminder prints whole
	if h.On(config.PiecePrompt) {
		owed, err := wake.OwedWith(ctx, d.Store, kvs) // a global done counts too: the merged read hides it behind a project's older one
		errs = append(errs, err)
		if len(owed) > 0 {
			parts = append(parts, wake.Reminders(owed, wake.DumpDir(d.project(in))))
			for _, a := range owed[:min(len(owed), wake.MaxReminders)] {
				line, err := d.situation(ctx, in, kvs, a.Group)
				errs = append(errs, err)
				parts = append(parts, line)
				printed[a.Group] = true
			}
		}
	}
	if h.On(config.PieceNotes) {
		if t := know.Notes(kvs); t != "" {
			parts = append(parts, t)
		}
	}
	if h.On(config.PieceCheck) {
		missed, ok, err := d.Store.LastInput(ctx, in.SessionID, EventCheck)
		errs = append(errs, err)
		if ok && missed != "" {
			parts = append(parts, missed)
			errs = append(errs, d.record(ctx, store.Event{Time: d.Now(), Event: EventCheck, Session: in.SessionID, Cwd: in.Cwd})) // shown: cleared
		}
	}
	if h.On(config.PieceContext) && in.TranscriptPath != "" {
		used, err := know.Used(in.TranscriptPath)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		if line := know.Meter(used, h.Window(), h.SleepShare()); line != "" {
			parts = append(parts, line)
		}
	}
	if h.On(config.PiecePrompt) {
		p, es := d.recall(ctx, in, kvs, printed)
		parts = append(parts, p...)
		errs = append(errs, es...)
	}
	return parts, errs
}

// recall is the notes found by the prompt's words, then the situations its words name. The search needs the embedder
// and so the server: when that does not answer in time it is skipped and logged, the situations stay.
func (d Deps) recall(ctx context.Context, in Input, kvs []store.KV, printed map[string]bool) (parts []string, errs []error) {
	if d.Recall != nil && know.WantRecall(in.Prompt) {
		rctx, cancel := context.WithTimeout(ctx, recallBudget)
		start := time.Now()
		hits, err := d.Recall(rctx, d.project(in), in.Prompt, 30)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("prompt recall skipped after %d ms: %w", time.Since(start).Milliseconds(), err))
		} else if t := know.Recall(know.PickRecall(hits)); t != "" {
			parts = append(parts, t)
		}
	}
	words, badW := know.ParseWords(kvs)
	_, badS := know.ParseSigs(kvs)
	_, badC := know.ParseRules(kvs)
	_, badO := know.ParseOns(kvs)
	for _, g := range know.MatchWords(words, in.Prompt) {
		if printed[g] {
			continue // already printed whole with its reminder
		}
		line, err := d.situation(ctx, in, kvs, g)
		errs = append(errs, err)
		parts = append(parts, line)
	}
	if bad := append(append(append(badW, badS...), badC...), badO...); len(bad) > 0 {
		parts = append(parts, "kv rules NOT read (a pattern Go's regexp rejects, or the wrong shape; fix or delete): "+strings.Join(bad, " "))
	}
	return parts, errs
}

// situation is the line of one situation as a prompt prints it: its note and the keys linked to it.
func (d Deps) situation(ctx context.Context, in Input, kvs []store.KV, g string) (string, error) {
	linked, err := d.Store.KVLinkedTo(ctx, d.project(in), know.SitPrefix+g, time.Time{})
	return "situation " + know.Situation(g, value(kvs, know.SitPrefix+g), linked), err
}

// arm owes every event situation whose pattern fits the SessionStart source: it writes sit/<g>/armed for the project,
// and each prompt says so until sit/<g>/done is written later (docs/wake.md).
func (d Deps) arm(ctx context.Context, in Input, now time.Time) error {
	kvs, err := d.kv(ctx, in, know.SitPrefix)
	if err != nil {
		return err
	}
	ons, _ := know.ParseOns(kvs) // a bad one is reported at the next prompt
	set := map[string]string{}
	for _, g := range know.MatchOns(ons, in.Source) {
		set[know.ArmedKey(g)] = know.ArmValue(in.Source, now)
	}
	if len(set) == 0 {
		return nil
	}
	_, err = d.Store.KVWrite(ctx, d.project(in), set, nil)
	return err
}

func value(kvs []store.KV, key string) string {
	for _, e := range kvs {
		if e.Key == key {
			return e.Value
		}
	}
	return ""
}

// karma is a tool call that fits a situation's signature: the situation, its rules by key, and a linked library whole,
// once a session. Each recognition is a row, so `chatlog events` can say which situations ever fire.
func (d Deps) karma(ctx context.Context, in Input) (string, error) {
	kvs, err := d.kv(ctx, in, know.SitPrefix)
	if err != nil {
		return "", err
	}
	sigs, _ := know.ParseSigs(kvs) // a bad one is reported at the next prompt, not at every call
	groups := know.MatchSigs(sigs, in.ToolName, know.ToolText(in.ToolName, in.ToolInput))
	if len(groups) == 0 {
		return "", nil
	}
	var out []string
	var errs []error
	for _, g := range groups {
		linked, err := d.Store.KVLinkedTo(ctx, d.project(in), know.SitPrefix+g, time.Time{})
		errs = append(errs, err)
		out = append(out, know.Situation(g, value(kvs, know.SitPrefix+g), linked))
		errs = append(errs, d.record(ctx, store.Event{Time: d.Now(), Event: EventKarma, Session: in.SessionID, Cwd: in.Cwd, Tool: in.ToolName, Input: know.SitPrefix + g}))
		for _, lib := range know.Lib(linked) {
			if in.SessionID == "" {
				continue
			}
			seen, err := d.Store.HasEvent(ctx, in.SessionID, EventKarmaLib, lib)
			errs = append(errs, err)
			if seen {
				continue
			}
			rows, err := d.kv(ctx, in, know.LibPrefix+lib+"/")
			errs = append(errs, err)
			if len(rows) == 0 {
				continue
			}
			out = append(out, know.LibText(lib, rows))
			errs = append(errs, d.record(ctx, store.Event{Time: d.Now(), Event: EventKarmaLib, Session: in.SessionID, Cwd: in.Cwd, Input: lib}))
		}
	}
	return "karma (situation recognised; its move, then its rules by key): " + strings.Join(out, " || "), errors.Join(errs...)
}

// check audits the turn that just ended and stores what it missed, to be printed at the next prompt. It never blocks.
func (d Deps) check(ctx context.Context, in Input) error {
	if in.TranscriptPath == "" {
		return nil
	}
	turn, ok, err := know.LastTurn(in.TranscriptPath)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !ok) {
		return nil
	}
	if err != nil {
		return err
	}
	kvs, err := d.kv(ctx, in, know.CheckPrefix)
	if err != nil {
		return err
	}
	rules, _ := know.ParseRules(kvs)
	missed := know.Missed(know.Audit(turn, rules))
	// the transcript's tail can still end in an earlier turn (an injected prompt is a meta entry the walk passes over):
	// the prompt this session's hook was given last says which turn just ended
	if p, ok, err := d.Store.LastInput(ctx, in.SessionID, "UserPromptSubmit"); err == nil && ok && rules.Skipped(p) {
		missed = ""
	}
	if missed == "" {
		// a miss not yet shown is superseded by this clean turn
		if prev, ok, err := d.Store.LastInput(ctx, in.SessionID, EventCheck); err != nil || !ok || prev == "" {
			return err
		}
	}
	return d.record(ctx, store.Event{Time: d.Now(), Event: EventCheck, Session: in.SessionID, Cwd: in.Cwd, Input: Field(missed, d.Config.Hooks.Max())})
}
