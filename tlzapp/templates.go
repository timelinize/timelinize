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
	"net/http"
	"time"

	"github.com/timelinize/timelinize/timeline"
)

var templateData = map[string]templateDataFunc{
	"/": func(_ *http.Request) (any, error) {
		return struct {
			Build  time.Time
			Expiry time.Time
		}{
			Build:  timeline.BuildPeriod,
			Expiry: timeline.Expiry,
		}, nil
	},
}

type templateDataFunc func(r *http.Request) (any, error)
