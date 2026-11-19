package feishu

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Decode wire JSON so this regression also runs against a version which did
// not know business_only: that version incorrectly renders the raw tool rows.
func TestTimelineCard_MissingBusinessPlanNeverFallsBackToTechnicalRows(t *testing.T) {
	for _, state := range []string{"running", "paused", "failed", "completed"} {
		var payload core.ProgressCardPayload
		raw := `{"style":"timeline","business_only":true,"lang":"zh","state":"` + state + `","items":[{"kind":"thinking","text":"PRIVATE_REASONING"},{"kind":"tool_use","tool":"Bash","text":"python generate-daily-profit.py --date 2026-11-18","status":"running"}],"latest_activity":"RAW_TOOL_ACTIVITY"}`
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatal(err)
		}
		card := buildTimelineCard(&payload)
		for _, technical := range []string{"PRIVATE_REASONING", "Bash", "generate-daily-profit.py", "RAW_TOOL_ACTIVITY", "工具调用", "执行过程", "技术执行明细"} {
			if strings.Contains(card, technical) {
				t.Fatalf("%s card fell back to technical progress: %s", state, card)
			}
		}
		if state == "running" && !strings.Contains(card, "正在制定业务计划") {
			t.Fatal(card)
		}
		if (state == "paused" || state == "failed") && !strings.Contains(card, "业务计划尚未建立") {
			t.Fatal(card)
		}
	}
}

func TestTimelineCard_BusinessPlanKeepsTechnicalDetailsOutOfPrimaryAndCollapsedViews(t *testing.T) {
	var payload core.ProgressCardPayload
	if err := json.Unmarshal([]byte(`{"style":"timeline","business_only":true,"lang":"zh","state":"running","steps":[{"title":"核对利润数据","status":"in_progress"},{"title":"编写经营日报","status":"pending"}],"items":[{"kind":"tool_use","tool":"Bash","text":"SECRET_TECHNICAL_COMMAND"}]}`), &payload); err != nil {
		t.Fatal(err)
	}
	card := buildTimelineCard(&payload)
	if !strings.Contains(card, "核对利润数据") || !strings.Contains(card, "编写经营日报") {
		t.Fatal(card)
	}
	for _, technical := range []string{"Bash", "SECRET_TECHNICAL_COMMAND", "技术执行明细"} {
		if strings.Contains(card, technical) {
			t.Fatal(card)
		}
	}
}
