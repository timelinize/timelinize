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
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// TODO: to list actual mount points:
// $ mount
// or
// $ findmnt (use -J for JSON output!!)

// getFileSelectorRoots gets the platform-specific filepath roots,
// like drive letters for Windows, the user's home folder for
// all, or mounted drives for Mac/Linux.
func getFileSelectorRoots() ([]fileSelectorRoot, error) {
	var roots []fileSelectorRoot

	// add file system root
	roots = append(roots, fileSelectorRoot{Label: "System Root", Path: "/", Type: "root"})

	// add user's home folder
	var homedirName string
	if home := userHomeDir(); home != "" && home != "." {
		homedirName = filepath.Base(home)
		if homedirName == "" {
			homedirName = "Home"
		}
		roots = append(roots, fileSelectorRoot{Label: homedirName, Path: home, Type: "home"})
	}

	// add any mounted folders in common mounting directories
	for _, mntDir := range []string{"/media", "/mnt", "/mount", "/run/media/" + homedirName} {
		mnts, err := os.ReadDir(mntDir)
		if err == nil {
			for _, mnt := range mnts {
				switch mnt.Name() {
				case "boot", "rootfs":
					continue
				}
				mntPath := filepath.Join(mntDir, mnt.Name())
				roots = append(roots, fileSelectorRoot{Label: mnt.Name(), Path: mntPath, Type: "mount"})
			}
		}
	}

	return roots, nil
}

func fileHidden(filename string) bool {
	return strings.HasPrefix(filepath.Base(filename), ".")
}

func dirEntryHidden(d fs.DirEntry) (bool, error) {
	return fileHidden(d.Name()), nil
}
