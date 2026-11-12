// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package wake

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/know"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

// Note is a key a list file writes beside the list: the situation that arms the wake, say.
type Note struct{ Key, Value string }

// ParseListFile reads a list file, one entry a line: `file PATH`, `prefix P`, `step TEXT`, `note KEY VALUE`; blank lines and
// lines starting with # are skipped. An error names the line, and the caller writes nothing.
func ParseListFile(r io.Reader) (List, []Note, error) {
	var l List
	var notes []Note
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		verb, rest, _ := strings.Cut(line, " ")
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return l, notes, fmt.Errorf("line %d: %q has nothing after %s", n, line, verb)
		}
		switch verb {
		case KindFile:
			l.Files = append(l.Files, rest)
		case KindPrefix:
			l.Prefixes = append(l.Prefixes, rest)
		case KindStep:
			l.Steps = append(l.Steps, rest)
		case "note":
			key, val, ok := strings.Cut(rest, " ")
			if !ok || strings.TrimSpace(val) == "" {
				return l, notes, fmt.Errorf("line %d: note takes a key and a value", n)
			}
			notes = append(notes, Note{key, strings.TrimSpace(val)})
		default:
			return l, notes, fmt.Errorf("line %d: %q: a line starts with file, prefix, step or note", n, verb)
		}
	}
	return l, notes, sc.Err()
}

// Keys is the list as the global notes hold it: global/wake/<kind>/<NN>, numbered in order, zero-padded to two digits
// (more when a kind has more than 99) so the keys sort as the list reads.
func (l List) Keys() map[string]string {
	set := map[string]string{}
	for kind, vals := range map[string][]string{KindFile: l.Files, KindPrefix: l.Prefixes, KindStep: l.Steps} {
		width := max(2, len(strconv.Itoa(len(vals))))
		for i, v := range vals {
			set[fmt.Sprintf("%s%s/%0*d", ListPrefix, kind, width, i+1)] = v
		}
	}
	return set
}

// Stale lists the keys of the list that the set does not carry any more, sorted: a loaded list replaces the one before.
func Stale(ctx context.Context, st *store.Store, set map[string]string) ([]string, error) {
	kvs, err := st.KVRead(ctx, store.GlobalProject, nil, []string{ListPrefix}, time.Time{})
	if err != nil {
		return nil, err
	}
	var del []string
	for _, e := range kvs {
		if k := store.GlobalPrefix + strings.TrimPrefix(e.Key, store.GlobalPrefix); set[k] == "" {
			del = append(del, k)
		}
	}
	slices.Sort(del)
	return del, nil
}

// OwedWith is the owed situations of the merged read kvs (a project's and the global notes under sit/), once the global
// notes' own done rows are counted: the project's merged read hides a global done behind the project's older one.
func OwedWith(ctx context.Context, st *store.Store, kvs []store.KV) ([]know.Arm, error) {
	owed := know.Owed(kvs)
	if len(owed) == 0 {
		return nil, nil
	}
	global, err := st.KVRead(ctx, store.GlobalProject, nil, []string{store.GlobalPrefix + know.SitPrefix}, time.Time{})
	return know.Owed(append(slices.Clone(kvs), global...)), err
}
