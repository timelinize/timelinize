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
	"syscall"
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

// getLocalLogicalDrives makes a Windows system call to get the local
// logical drives assigned a drive letter, and returns the list of
// drive letters only. From:
// https://stackoverflow.com/questions/23128148/how-can-i-get-a-listing-of-all-drives-on-windows-using-golang
func getLocalLogicalDrives() ([]string, error) {
	kernel32, _ := syscall.LoadLibrary("kernel32.dll")
	getLogicalDrivesHandle, _ := syscall.GetProcAddress(kernel32, "GetLogicalDrives")
	ret, _, callErr := syscall.Syscall(uintptr(getLogicalDrivesHandle), 0, 0, 0, 0)
	if callErr != 0 {
		return nil, callErr
	}
	return bitsToDrives(uint32(ret)), nil
}

func bitsToDrives(bitMap uint32) (drives []string) {
	availableDrives := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K",
		"L", "M", "N", "O", "P", "Q", "R", "S", "T", "U", "V", "W", "X", "Y", "Z"}
	for i := range availableDrives {
		if bitMap&1 == 1 {
			drives = append(drives, availableDrives[i])
		}
		bitMap >>= 1
	}
	return
}

func fileHidden(filename string) bool {
	// TODO: hide dot-files anyway? if strings.HasPrefix(filename, ".")...

	pointer, err := syscall.UTF16PtrFromString(filename)
	if err != nil {
		return false
	}
	attributes, err := syscall.GetFileAttributes(pointer)
	if err != nil {
		return false
	}
	return attributes&syscall.FILE_ATTRIBUTE_HIDDEN != 0
}
