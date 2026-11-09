package devices

import (
	"errors"
	"net/http"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
)

var errUnknownMachine = i18n.Err("devices-api.unknown_machine")

func writeJSON(w http.ResponseWriter, v any) { apierr.WriteJSON(w, v) }

func writeErr(w http.ResponseWriter, status int, err error) { apierr.Write(w, status, err) }

func writeErrCode(w http.ResponseWriter, status int, code string, err error) {
	apierr.WriteCode(w, status, code, err)
}

// readAdminJSON reads an admin page's JSON body into v (an empty body leaves v as it is).
var readAdminJSON = apierr.ReadAdminJSON

func isOffline(err error) bool { return errors.Is(err, errOffline) }
