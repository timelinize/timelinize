/*
	Timelinize
	Copyright (c) 2013 Matthew Holt

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU Affero General Public License as published
	by the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU Affero General Public License for more details.

	You should have received a copy of the GNU Affero General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package tlzapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	mathrand "math/rand"
	"net/http"
	"strconv"
	"strings"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

// Error is a JSON-serializable representation of an error.
type Error struct {
	Err             error    `json:"-"`
	HTTPStatus      int      `json:"http_status"`               // recommended HTTP status to send to the client
	Log             string   `json:"-"`                         // optional; for logs, technical context in which the error was produced
	Message         string   `json:"message,omitempty"`         // optional; a human-readable sentence
	Recommendations []string `json:"recommendations,omitempty"` // optional
	Data            any      `json:"data,omitempty"`            // optional; any extra data that should be included or handled specially

	// generated; don't fill these out
	ID        string `json:"id,omitempty"` // for associating log entries
	ErrString string `json:"error"`        // to ensure string serialization
}

func (e Error) Error() string {
	var msg strings.Builder
	if e.Log != "" {
		msg.WriteString(e.Log)
		if e.Err != nil {
			msg.WriteString(": ")
		}
	}
	if e.Err != nil {
		msg.WriteString(e.Err.Error())
	}
	if e.Message != "" {
		msg.WriteString(fmt.Sprintf(" (%s)", e.Message))
	}
	if e.ID != "" {
		msg.WriteString(fmt.Sprintf(" {id=%s}", e.ID))
	}
	return msg.String()
}

func httpStatusfromOSErr(err error, defaultStatus int) int {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return http.StatusForbidden
	case errors.Is(err, fs.ErrNotExist):
		return http.StatusNotFound
	}
	return defaultStatus
}

func handleError(w http.ResponseWriter, r *http.Request, err error) {
	var errVal Error
	if !errors.As(err, &errVal) {
		errVal = Error{
			Err: err,
			Log: "error was not well-structured",
		}
	}

	// give this error a unique ID so we can investigate bug reports more easily
	errVal.ID = newErrorID()

	// ensure error is serialized as a string when written to the client
	errVal.ErrString = errVal.Err.Error()

	// see if we can fill in some default values if they're missing
	if errVal.HTTPStatus == 0 {
		errVal.HTTPStatus = httpStatusfromOSErr(err, http.StatusInternalServerError)
	}
	if errVal.Message == "" && errVal.Err != nil {
		errVal.Message = errVal.Err.Error()
	}

	// append standard recommendations (TODO: bug reporting doesn't help much because we don't have their logs...)
	reportRec := "If it still doesn't work, we can help! Please report this bug "
	reportRecEnd := "to support@dyanim.com and we'll take care of it."
	if errVal.ID == "" {
		reportRec += reportRecEnd
	} else {
		reportRec += fmt.Sprintf("along with this magic incantation: %s %s", errVal.ID, reportRecEnd)
	}
	errVal.Recommendations = append(errVal.Recommendations,
		"Make any relevant changes, then try again.",
		reportRec,
	)

	// log the error
	timeline.Log.Named("http").Error(errVal.Log,
		zap.Error(errVal.Err),
		zap.Int("status", errVal.HTTPStatus),
		zap.String("method", r.Method),
		zap.String("path", r.URL.Path),
		zap.String("error_id", errVal.ID),
		zap.Any("data", errVal.Data),
	)

	// write the error to the HTTP response for the frontend
	jsonBytes, err := json.Marshal(errVal)
	if err != nil {
		timeline.Log.Error("encoding error response",
			zap.Error(err),
			zap.String("original_error", errVal.Error()))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(jsonBytes)))
	status := errVal.HTTPStatus
	if status < http.StatusOK {
		status = http.StatusInternalServerError
	}
	w.WriteHeader(status)
	_, _ = w.Write(jsonBytes)
}

func newErrorID() string {
	const idLen = 8
	return randString(idLen, true)
}

// randString returns a string of n random characters.
// It is not even remotely secure or a proper distribution.
// But it's good enough for some things. It excludes certain
// confusing characters like I, l, 1, 0, O, etc., and a couple
// vowels to avoid most profanities.
func randString(n int, lowerCase bool) string {
	if n <= 0 {
		return ""
	}
	dict := []byte("abcdefghjkmnopqrstvwxyzABCDEFGHJKLMNPQRTUVWXY23456789")
	if lowerCase {
		dict = []byte("abcdefghjkmnpqrstvwxyz23456789")
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = dict[mathrand.Int63()%int64(len(dict))] //nolint:gosec
	}
	return string(b)
}
