// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package know

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const (
	oldPrompt = `{"type":"user","message":{"role":"user","content":"an older prompt"}}`
	oldTool   = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{}}]}}`
	prompt    = `{"type":"user","message":{"role":"user","content":"fix the build"}}`
	call1     = `{"type":"assistant","message":{"content":[{"type":"text","text":"on it"},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":3,"cache_read_input_tokens":100,"cache_creation_input_tokens":10}}}`
	result    = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"a"}]}}`
	call2     = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"a"}},{"type":"tool_use","name":"Read","input":{}}],"usage":{"input_tokens":1,"cache_read_input_tokens":200,"cache_creation_input_tokens":5}}}`
	side      = `{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"tool_use","name":"Grep","input":{}}],"usage":{"input_tokens":9000}}}`
	meta      = `{"type":"user","isMeta":true,"message":{"content":"Stop hook feedback: x"}}`
)

func TestLastTurnIsEverythingAfterTheLastTypedPrompt(t *testing.T) {
	p := write(t, oldPrompt, oldTool, prompt, call1, result, side, meta, call2, `not json`, `{"type":"summary"}`)
	got, ok, err := LastTurn(p)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if got.Prompt != "fix the build" {
		t.Errorf("prompt %q", got.Prompt)
	}
	var names []string
	for _, u := range got.Uses {
		names = append(names, u.Name)
	}
	if strings.Join(names, ",") != "Bash,Edit,Read" {
		t.Errorf("calls %v: in order, the older turn's Write and the subagent's Grep left out", names)
	}
}

func TestLastTurnReadsAPromptGivenAsTextBlocks(t *testing.T) {
	p := write(t, `{"type":"user","message":{"content":[{"type":"text","text":"hello"},{"type":"image"},{"type":"text","text":"there"}]}}`, call1)
	got, ok, _ := LastTurn(p)
	if !ok || got.Prompt != "hello\nthere" || len(got.Uses) != 1 {
		t.Errorf("%+v %v", got, ok)
	}
}

func TestLastTurnWithoutAPromptOrFile(t *testing.T) {
	if _, ok, err := LastTurn(write(t, call1, result)); ok || err != nil {
		t.Errorf("no typed prompt in the tail: %v %v", ok, err)
	}
	if _, _, err := LastTurn(filepath.Join(t.TempDir(), "missing")); !os.IsNotExist(err) {
		t.Errorf("%v", err)
	}
}

func TestTheTailCutsAtALineStart(t *testing.T) {
	big := `{"type":"user","message":{"content":"` + strings.Repeat("x", TailBytes) + `"}}`
	p := write(t, big, prompt, call2)
	got, ok, err := LastTurn(p)
	if err != nil || !ok || got.Prompt != "fix the build" || len(got.Uses) != 2 {
		t.Errorf("%+v %v %v", got, ok, err)
	}
	if n, _ := Used(p); n != 206 {
		t.Errorf("used %d", n)
	}
}

func TestUsedIsTheLastMainThreadAssistantUsage(t *testing.T) {
	if n, err := Used(write(t, prompt, call1, result, side, call2, meta)); n != 206 || err != nil {
		t.Errorf("%d %v: 1+200+5 of the last main-thread message, not the subagent's 9000", n, err)
	}
	if n, _ := Used(write(t, prompt, call1, result)); n != 113 {
		t.Errorf("%d", n)
	}
	if n, _ := Used(write(t, prompt, side)); n != 0 {
		t.Errorf("%d: only a subagent's usage is none", n)
	}
	if n, _ := Used(write(t, prompt)); n != 0 {
		t.Errorf("%d", n)
	}
}

// A turn longer than one read chunk: lines straddle the chunk edges, and none is lost or doubled.
func TestLastTurnAcrossChunkEdges(t *testing.T) {
	for _, pad := range []int{1, 777, 20_000, chunkBytes - 50, chunkBytes + 50} {
		lines := []string{oldPrompt, prompt}
		for i := 0; i < 20; i++ {
			lines = append(lines, `{"type":"assistant","message":{"content":[{"type":"text","text":"`+strings.Repeat("y", pad)+`"},{"type":"tool_use","name":"T`+string(rune('a'+i))+`","input":{}}]}}`)
		}
		p := write(t, lines...)
		got, ok, err := LastTurn(p)
		if err != nil || !ok || got.Prompt != "fix the build" || len(got.Uses) != 20 || got.Uses[0].Name != "Ta" || got.Uses[19].Name != "Tt" {
			t.Errorf("pad %d: %v %v %d uses", pad, ok, err, len(got.Uses))
		}
	}
}

func TestAFileWithoutATrailingNewlineOrWithOneLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(p, []byte(prompt+"\n"+call1), 0o600)
	if got, ok, _ := LastTurn(p); !ok || len(got.Uses) != 1 {
		t.Errorf("%+v %v", got, ok)
	}
	os.WriteFile(p, []byte(prompt), 0o600)
	if got, ok, _ := LastTurn(p); !ok || got.Prompt != "fix the build" {
		t.Errorf("a single line is a line: %+v %v", got, ok)
	}
	os.WriteFile(p, nil, 0o600)
	if _, ok, err := LastTurn(p); ok || err != nil {
		t.Errorf("empty: %v %v", ok, err)
	}
}
