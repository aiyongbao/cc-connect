package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

// Only an automatically bootstrapped topic root is optional on resume. An
// explicit quote of a different message or a non-isolated quote stays normal
// context so the fix cannot silently discard the user's chosen reference.
func TestDispatchMessage_OnlyThreadRootBecomesInitialContext(t *testing.T) {
	for _, tc := range []struct {
		name        string
		isolated    bool
		parent      string
		wantInitial bool
	}{
		{"root bootstrap", true, "om_root", true},
		{"explicit child quote", true, "om_child", false},
		{"non-isolated quote", false, "om_root", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
					writeJSON(t, w, map[string]any{"code": 0, "tenant_access_token": "test-token", "expire": 7200})
				case r.URL.Path == "/open-apis/im/v1/messages/"+tc.parent:
					writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"items": []map[string]any{{
						"msg_type": "text", "parent_id": "",
						"sender": map[string]any{"id": "user", "sender_type": "user"},
						"body":   map[string]any{"content": `{"text":"quoted reference"}`},
					}}}})
				default:
					writeJSON(t, w, map[string]any{"code": 0})
				}
			}))
			defer srv.Close()
			var got *core.Message
			p := &Platform{
				platformName: "feishu", domain: srv.URL, appID: "app", appSecret: "test-secret", botOpenID: "bot",
				threadIsolation: tc.isolated,
				client:          lark.NewClient("app", "test-secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
				handler:         func(_ core.Platform, msg *core.Message) { got = msg },
			}
			const key = "feishu:oc_chat:root:om_root"
			rctx := replyContext{messageID: "om_retry", chatID: "oc_chat", sessionKey: key, bootstrapThread: true}
			p.dispatchMessageWithHistory(context.Background(), "text", `{"text":"retry"}`, nil, "om_retry", key, "user", "oc_chat", rctx, tc.parent, 0, groupHistoryContext{})
			if got == nil || got.Content != "retry" {
				t.Fatalf("current user message lost: %+v", got)
			}
			if tc.wantInitial {
				if got.InitialContext == nil || !strings.Contains(got.InitialContext.Text, "quoted reference") || got.ExtraContent != "" {
					t.Fatalf("root must be optional initial context: %+v", got)
				}
			} else if got.InitialContext != nil || !strings.Contains(got.ExtraContent, "quoted reference") {
				t.Fatalf("normal quote must survive resume: %+v", got)
			}
		})
	}
}
