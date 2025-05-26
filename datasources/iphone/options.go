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

package iphone

// Options configures the data source.
type Options struct {
	SkipSpam bool `json:"skip_spam"` // TODO: not implemented (haven't seen is_spam=1 in a DB yet)

	// If enabled, import photos and videos directly from the camera roll
	// (DCIM folder) instead of using the Photos app library. This is how
	// iPhone photo/videos were imported before Photos library support was
	// implemented. It may provide less metadata than the Photos library,
	// but may also import files not indexed by the Photos library.
	CameraRoll bool `json:"camera_roll"`
}
