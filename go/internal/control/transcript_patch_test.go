package control

import (
	"encoding/json"
	"testing"
)

// TestRenderEventStructuredPatch:Edit 的 tool_result 行,顶层 toolUseResult.structuredPatch
// 应被透传到对应 tool_result 块的 Patch 字段(原样 JSON,含上下文行)。
func TestRenderEventStructuredPatch(t *testing.T) {
	line := []byte(`{"type":"user","uuid":"u1","timestamp":"2026-01-01T00:00:00Z",` +
		`"toolUseResult":{"structuredPatch":[{"oldStart":2,"oldLines":3,"newStart":2,"newLines":4,"lines":[" ctx","-old","+new"]}],"originalFile":null},` +
		`"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tA","content":"ok"}]}}`)
	it, ok := renderEvent(line)
	if !ok {
		t.Fatal("renderEvent should produce an item")
	}
	var tr *tBlock
	for i := range it.Blocks {
		if it.Blocks[i].Type == "tool_result" {
			tr = &it.Blocks[i]
			break
		}
	}
	if tr == nil {
		t.Fatal("expected a tool_result block")
	}
	if len(tr.Patch) == 0 {
		t.Fatal("expected Patch to be populated from structuredPatch")
	}
	var hunks []struct {
		OldStart int      `json:"oldStart"`
		NewStart int      `json:"newStart"`
		Lines    []string `json:"lines"`
	}
	if err := json.Unmarshal(tr.Patch, &hunks); err != nil {
		t.Fatalf("Patch should be a valid hunk array: %v", err)
	}
	if len(hunks) != 1 || hunks[0].OldStart != 2 || hunks[0].NewStart != 2 || len(hunks[0].Lines) != 3 {
		t.Fatalf("unexpected hunk passthrough: %+v", hunks)
	}
}

// TestRenderEventEmptyPatch:空 structuredPatch(无改动)不应塞 Patch(前端据此回退 old/new diff)。
func TestRenderEventEmptyPatch(t *testing.T) {
	line := []byte(`{"type":"user","uuid":"u2","timestamp":"2026-01-01T00:00:00Z",` +
		`"toolUseResult":{"structuredPatch":[],"originalFile":null},` +
		`"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tB","content":"ok"}]}}`)
	it, ok := renderEvent(line)
	if !ok {
		t.Fatal("renderEvent should produce an item")
	}
	for _, b := range it.Blocks {
		if b.Type == "tool_result" && len(b.Patch) != 0 {
			t.Fatalf("empty structuredPatch should not populate Patch, got %s", string(b.Patch))
		}
	}
}
