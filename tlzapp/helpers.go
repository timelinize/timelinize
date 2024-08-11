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
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"
)

type handler interface {
	ServeHTTP(w http.ResponseWriter, r *http.Request) error
}

// handlerFunc is like http.HandlerFunc, except these handlers return an error.
type handlerFunc func(http.ResponseWriter, *http.Request) error

func (h handlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) error {
	return h(w, r)
}

// userHomeDir returns the best guess of the user's home directory,
// or "." (current directory) if the home directory is unknown
// (to make it safe to use in filepath.Join()). The implementation of
// this function is slightly more nuanced than that of os.UserHomeDir()
// but I do not know if it is more correct/robust; the main difference
// being that this always returns a valid path even if it is the current
// directory (".").
func userHomeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	if runtime.GOOS == "windows" {
		if home := os.Getenv("USERPROFILE"); home != "" {
			return home
		}
		drive := os.Getenv("HOMEDRIVE")
		path := os.Getenv("HOMEPATH")
		if drive != "" && path != "" {
			return drive + path
		}
	}
	if runtime.GOOS == "plan9" {
		if home := os.Getenv("home"); home != "" {
			return home
		}
	}
	if runtime.GOOS == "android" {
		return "/sdcard"
	}
	if runtime.GOOS == "ios" {
		return "/"
	}
	return "."
}

// fileSelectorRoot describes a starting point for the file selector.
// Typically drives, mount points, or user home folders.
type fileSelectorRoot struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	Type  string `json:"type"`
}

// localFile represents serializable information about a local file.
type localFile struct {
	FullName string      `json:"full_name"`
	Name     string      `json:"name"`
	Size     int64       `json:"size"`
	Mode     os.FileMode `json:"mode"`
	ModTime  time.Time   `json:"mod_time"`
	IsDir    bool        `json:"is_dir"`
}

type fileListing struct {
	Dir      string      `json:"dir"`                // absolute path of directory
	Selected string      `json:"selected,omitempty"` // if file was given, which one is selected
	Up       string      `json:"up,omitempty"`       // directory above this one
	Files    []localFile `json:"files"`
}

// wrapErrorHandler turns a handlerFunc (a handler that returns an error)
// into a standard http.HandlerFunc by handling any returned error.
func wrapErrorHandler(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h.ServeHTTP(w, r); err != nil {
			handleError(w, r, err)
		}
	})
}

// httpWrap wraps a standard http.Handler with a handlerFunc signature.
func httpWrap(h http.Handler) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		h.ServeHTTP(w, r)
		return nil
	}
}

func jsonEncodeErr(err error) error {
	return Error{
		Err:        err,
		HTTPStatus: http.StatusInternalServerError,
		Log:        "Encoding JSON response",
		Message:    "Our program has a bug. It wasn't able to respond with data in JSON format.",
	}
}

func jsonResponse(w http.ResponseWriter, v any, err error) error {
	if err != nil {
		return err
	}
	respBytes, err := json.Marshal(v)
	if err != nil {
		return jsonEncodeErr(err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(respBytes)))
	w.Write(respBytes)
	return nil
}
