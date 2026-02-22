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

package timeline

import (
	"golang.org/x/sys/windows"
)

func getFileSystemType(path string) (string, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}

	var fsname [windows.MAX_PATH + 1]uint16
	err = windows.GetVolumeInformation(
		pathPtr,
		nil, 0,
		nil, nil, nil,
		&fsname[0], windows.MAX_PATH+1,
	)
	if err != nil {
		return "", err
	}

	return windows.UTF16ToString(fsname[:]), nil
}
