package audacityadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCallAndRefusal(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/v1/admin/invite_user":
			_ = json.NewEncoder(w).Encode(Outcome{Status: "executed", OK: true, Summary: "s", Message: "sent"})
		case "/v1/admin/ops":
			_, _ = w.Write([]byte(`{"ops":[{"id":"list_my_api_keys","domain":"personal","kind":"read","blurb":"k","alwaysConfirm":false,"secretReturning":false,"inputSchema":{}}]}`))
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"status":"refused","code":"not_allowed","error":"no"}`))
		}
	}))
	defer srv.Close()
	c, err := New(Options{ManagementToken: "aireserve_mcp_t", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Members.InviteUser(context.Background(), map[string]any{"email": "a@b.c"})
	if err != nil || !out.OK || gotPath != "/v1/admin/invite_user" || gotAuth != "Bearer aireserve_mcp_t" {
		t.Fatalf("invite: %v %+v %s %s", err, out, gotPath, gotAuth)
	}
	ops, err := c.Ops(context.Background())
	if err != nil || len(ops) != 1 || ops[0].ID != "list_my_api_keys" {
		t.Fatalf("ops: %v %+v", err, ops)
	}
	_, err = c.Personal.GetMyBudgetStatus(context.Background(), nil)
	var e *Error
	if err == nil || !errorsAs(err, &e) || e.Code != "not_allowed" || e.Status != 403 {
		t.Fatalf("refusal: %v", err)
	}
	t.Setenv("AIRESERVE_MCP_TOKEN", "")
	if _, err := New(Options{}); err == nil {
		t.Fatal("expected missing-token error")
	}
}

func errorsAs(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}
