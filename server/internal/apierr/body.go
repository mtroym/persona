package apierr

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// adminBodyMost is how much of an admin page's request body ReadAdminJSON reads: the forms are small, and a longer
// body is cut there and then fails as bad JSON.
const adminBodyMost = 64 << 10

// ReadAdminJSON reads an admin page's JSON body into v (an empty body leaves v as it is). On a bad body it has already
// answered 400 and returns false. The hub's, the devices manager's and the extension manager's admin pages share it.
func ReadAdminJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	b, err := io.ReadAll(io.LimitReader(r.Body, adminBodyMost))
	if err != nil {
		Write(w, http.StatusBadRequest, err)
		return false
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return true
	}
	if err := json.Unmarshal(b, v); err != nil {
		WriteCode(w, http.StatusBadRequest, "bad_request", errors.New("bad JSON: "+err.Error()))
		return false
	}
	return true
}
