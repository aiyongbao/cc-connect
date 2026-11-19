package planguard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/chenhg5/cc-connect/core"
)

// RunMCP exposes one real planning tool even when an agent/model no longer
// offers its built-in plan API. It has no business-data or shell capability.
func RunMCP(path string, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	enc := json.NewEncoder(output)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			return fmt.Errorf("read planning request: %w", err)
		}
		if len(req.ID) == 0 {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": ServerName, "version": "1"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			var schema any
			_ = json.Unmarshal([]byte(planSchema), &schema)
			result = map[string]any{"tools": []any{map[string]any{
				"name": "update_plan", "description": "Publish or update the user's business plan BEFORE substantive work. Write 2-6 brief outcome-based steps in the user's language (one for a single tool action). Report real pending/in_progress/completed status; never infer completion from a tool or turn ending.", "inputSchema": schema,
			}}}
		case "tools/call":
			result = callPlanTool(path, req.Params.Name, req.Params.Arguments)
		default:
			if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}}); err != nil {
				return err
			}
			continue
		}
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			return err
		}
	}
	return scanner.Err()
}

const planSchema = `{"type":"object","properties":{"plan":{"type":"array","minItems":1,"maxItems":6,"items":{"type":"object","properties":{"step":{"type":"string","minLength":1,"maxLength":240},"status":{"type":"string","enum":["pending","in_progress","completed"]}},"required":["step","status"],"additionalProperties":false}}},"required":["plan"],"additionalProperties":false}`

func callPlanTool(path, name string, arguments json.RawMessage) map[string]any {
	plan := core.ParseTaskProgress("cc_connect_plan", arguments)
	err := error(nil)
	if name != "update_plan" || plan == nil || len(plan.Steps) < 1 || len(plan.Steps) > 6 {
		err = fmt.Errorf("provide 1-6 nonempty business steps with real pending, in_progress, or completed status")
	} else {
		err = withState(path, func(s *state) error {
			if !s.Enabled {
				return fmt.Errorf("no active business planning turn")
			}
			if !acceptPlan(s, plan) {
				return fmt.Errorf("a new plan must include an unfinished business step before work can start")
			}
			return nil
		})
	}
	text := "Business plan published. Keep it updated as work starts or finishes."
	if err != nil {
		text = "Business plan was not published: " + err.Error()
	}
	return map[string]any{"isError": err != nil, "content": []any{map[string]any{"type": "text", "text": text}}}
}
