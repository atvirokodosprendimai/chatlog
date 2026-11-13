package mcpserver

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func call(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}}
}

func TestKVWriteRequestRefusesAGuardOfTheWrongTypeInsteadOfDroppingIt(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"if_absent a string":    {"set": map[string]any{"a": "v"}, "if_absent": "a"},
		"if_absent not strings": {"set": map[string]any{"a": "v"}, "if_absent": []any{1}},
		"if_value a string":     {"set": map[string]any{"a": "v"}, "if_value": "a=v"},
		"if_value not strings":  {"set": map[string]any{"a": "v"}, "if_value": map[string]any{"a": 1}},
		"body not strings":      {"set": map[string]any{"a": "v"}, "body": map[string]any{"a": 1}},
		"body a string":         {"set": map[string]any{"a": "v"}, "body": "text"},
		"set a string":          {"set": "a"},
		"del a string":          {"set": map[string]any{"a": "v"}, "del": "b"},
		"unknown argument":      {"set": map[string]any{"a": "v"}, "key": "x"},
	} {
		if _, err := kvWriteRequest(call(args), "p"); err == nil {
			t.Errorf("%s: a condition that is not understood must not become an unconditional write", name)
		}
	}
	req, err := kvWriteRequest(call(map[string]any{"set": map[string]any{"a": "v"}, "if_absent": []any{"a"}, "if_value": map[string]any{"b": "w"}}), "p")
	if err != nil || len(req.IfAbsent) != 1 || req.IfValue["b"] != "w" || req.Set["a"] != "v" {
		t.Fatalf("%+v %v", req, err)
	}
}
