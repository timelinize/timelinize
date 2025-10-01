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
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// getFileSelectorRoots gets the platform-specific filepath roots,
// like drive letters for Windows, the user's home folder for
// all, or mounted drives for Mac/Linux.
func getFileSelectorRoots() ([]fileSelectorRoot, error) {
	var roots []fileSelectorRoot

	// add user's home folder
	if homeDir := userHomeDir(); homeDir != "." {
		roots = append(roots, fileSelectorRoot{Label: filepath.Base(homeDir), Path: homeDir, Type: "home"})
	}

	// add local logical drives
	locLogDriveLetters, err := getLocalLogicalDrives()
	if err != nil {
		return roots, fmt.Errorf("listing local logical drives: %v", err)
	}
	for _, letter := range locLogDriveLetters {
		path := fmt.Sprintf(`%s:\`, letter)
		label := path
		roots = append(roots, fileSelectorRoot{Label: label, Path: path, Type: "logical"})
	}

	// TODO: Network shares / mounts? And use volume IDs instead of drive letters internally?
	// See https://superuser.com/questions/465730/access-to-a-disk-drive-using-volume-id-instead-of-a-drive-letter-in-windows/465741

	return roots, nil
}

// getLocalLogicalDrives calls the Windows API GetLogicalDrives and returns
// a slice of drive letters.
func getLocalLogicalDrives() ([]string, error) {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	procGetLogicalDrives := kernel32.NewProc("GetLogicalDrives")
	ret, _, err := procGetLogicalDrives.Call()
	if err != windows.ERROR_SUCCESS && err != nil {
		return nil, err
	}
	return bitsToDrives(uint32(ret)), nil
}

// bitsToDrives converts a bitmask from GetLogicalDrives into a list of drive letters.
func bitsToDrives(bitMap uint32) []string {
	letters := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K",
		"L", "M", "N", "O", "P", "Q", "R", "S", "T", "U", "V", "W", "X", "Y", "Z"}

	var drives []string
	for _, letter := range letters {
		if bitMap&1 == 1 {
			drives = append(drives, letter)
		}
		bitMap >>= 1
	}
	return drives
}

func fileHidden(filename string) bool {
	// TODO: hide dot-files anyway? if strings.HasPrefix(filename, ".")...

	pointer, err := windows.UTF16PtrFromString(filename)
	if err != nil {
		return false
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if err != nil {
		return false
	}
	return attributes&windows.FILE_ATTRIBUTE_HIDDEN != 0
}
