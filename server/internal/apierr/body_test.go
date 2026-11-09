package apierr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An admin body: empty leaves the target as it was; bad JSON, and a body cut at the limit, answer 400 bad_request.
func TestReadAdminJSON(t *testing.T) {
	type form struct {
		Name string `json:"name"`
	}
	long := `{"name":"` + strings.Repeat("x", adminBodyMost) + `"}`
	for _, tc := range []struct {
		name, body string
		ok         bool
		want       string // the form's name after the call
		errPrefix  string
	}{
		{"empty", " \n", true, "kept", ""},
		{"good", `{"name":"new"}`, true, "new", ""},
		{"bad", `{"name":`, false, "kept", "bad JSON: "},
		{"too long", long, false, "kept", "bad JSON: "},
	} {
		rec := httptest.NewRecorder()
		v := form{Name: "kept"}
		ok := ReadAdminJSON(rec, httptest.NewRequest("POST", "/", strings.NewReader(tc.body)), &v)
		if ok != tc.ok || v.Name != tc.want {
			t.Fatalf("%s: ok %v, name %q", tc.name, ok, v.Name)
		}
		if ok {
			if rec.Body.Len() != 0 {
				t.Fatalf("%s: answered %q", tc.name, rec.Body.String())
			}
			continue
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if msg, _ := body["error"].(string); rec.Code != http.StatusBadRequest || body["code"] != "bad_request" || !strings.HasPrefix(msg, tc.errPrefix) {
			t.Fatalf("%s: %d %v", tc.name, rec.Code, body)
		}
	}
}
