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

// Package iphone implements a data source for iPhone backups. It supports
// the camera roll, messages, and address book, along with basic device info.
package iphone

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/timelinize/timelinize/datasources/imessage"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
	"howett.net/plist"
)

/*
	TODO: Other options/ideas for this data source:
	- Scan files for recognized types, like .vcf (vCards) and just import them
	- Interesting file: CameraRollDomain: Media/PhotoData/Photos.sqlite (12b144c0bd44f2b3dffd9186d3f9c05b917cee25 in my sample) -- tons of granular details including face recognition
	- Potentially useful files with telephone number:
		bf01197b61bd101045f3421ec28b29e8cc0d3930 also <-- HomeDomain: Library/Preferences/com.apple.imservice.SMS.plist
		bfecaa9c467e3acb085a5b312bd27bdd5cd7579a also <-- WirelessDomain: Library/Preferences/com.apple.commcenter.plist

*/

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "iphone",
		Title:           "iPhone",
		Icon:            "iphone.svg",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return &FileImporter{manifestMu: new(sync.Mutex)} },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter can import iPhone backups from a folder on disk.
type FileImporter struct {
	itemChan chan<- *timeline.Graph
	opt      timeline.ListingOptions
	dsOpt    *Options

	// these change as we iterate the list of input folders
	root              string
	manifest          *sql.DB
	manifestMu        *sync.Mutex // (this obviously doesn't change)
	devicePhoneNumber string
	owner             *timeline.Entity
}

// Recognize returns whether the folder is recognized.
func (FileImporter) Recognize(_ context.Context, folders []string) (timeline.Recognition, error) {
	var found, total int
	for _, folder := range folders {
		for _, filename := range []string{
			"Manifest.db",
			"Manifest.plist",
			"Info.plist",
			"Status.plist",
		} {
			total++
			if timeline.FileExists(filepath.Join(folder, filename)) {
				found++
			} else if filename == "Manifest.db" {
				return timeline.Recognition{}, nil // the manifest DB is absolutely required
			}
		}
	}
	return timeline.Recognition{Confidence: float64(found) / float64(total)}, nil
}

// FileImport imports data from the given folder.
func (fimp *FileImporter) FileImport(ctx context.Context, roots []string, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {
	fimp.dsOpt = opt.DataSourceOptions.(*Options)
	fimp.itemChan = itemChan
	fimp.opt = opt

	for _, root := range roots {
		if err := fimp.importFolder(ctx, root); err != nil {
			return fmt.Errorf("%s: %w", root, err)
		}
	}

	return nil
}

func (fimp *FileImporter) importFolder(ctx context.Context, root string) error {
	fimp.root = root

	db, err := sql.Open("sqlite3", filepath.Join(root, "Manifest.db")+"?mode=ro")
	if err != nil {
		return fmt.Errorf("opening manifest DB: %w", err)
	}
	defer db.Close()

	fimp.manifest = db

	// load device's current/latest phone number in case we need it (I haven't seen much need for it,
	// but I already wrote the code before I discovered the easier source of the phone number)
	commCenter, err := fimp.loadCommCenter(ctx)
	if err != nil {
		return err
	}
	fimp.devicePhoneNumber = imessage.NormalizePhoneNumber(commCenter.phoneNumber(), "")
	if fimp.devicePhoneNumber != "" {
		fimp.owner = &timeline.Entity{
			Attributes: []timeline.Attribute{
				{
					Name:     timeline.AttributePhoneNumber,
					Value:    fimp.devicePhoneNumber,
					Identity: true,
				},
			},
		}
	}

	if err = fimp.addressBook(ctx); err != nil {
		return fmt.Errorf("importing iPhone address book: %w", err)
	}
	if err = fimp.messages(ctx); err != nil {
		return fmt.Errorf("importing iPhone messages: %w", err)
	}
	if err = fimp.cameraRoll(ctx); err != nil {
		return fmt.Errorf("importing iPhone camera roll: %w", err)
	}

	return nil
}

// openFile opens a file given its domain and path. The manifest DB maps domain+path to file ID.
// We look up the file ID from the manifest and then use that to open the file and return it.
func (fimp *FileImporter) openFile(ctx context.Context, domain, relativePath string) (fs.File, error) {
	fileID, err := fimp.findFileID(ctx, domain, relativePath)
	if err != nil {
		return nil, err
	}
	return os.Open(fimp.fileIDToPath(fileID))
}

// findFileID looks up the file ID in the manifest for the file given by its domain and path relative to that domain.
func (fimp *FileImporter) findFileID(ctx context.Context, domain, relativePath string) (string, error) {
	fimp.manifestMu.Lock()
	defer fimp.manifestMu.Unlock()

	var id *string
	err := fimp.manifest.QueryRowContext(ctx,
		"SELECT fileID FROM Files WHERE domain=? AND relativePath=? LIMIT 1",
		domain, relativePath).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("looking up fileID for relative path '%s' in domain '%s': %w", relativePath, domain, err)
	}

	if id != nil {
		return *id, nil
	}
	return "", nil
}

// fileIDToPath converts a fileID (the checksum by which it is organized in the backup) to
// the absolute path it can be accessed on disk. The return value uses the OS path separator.
func (fimp FileImporter) fileIDToPath(fileID string) string {
	return filepath.Join(fimp.root, filepath.FromSlash(fimp.fileIDToRelativePath(fileID)))
}

// fileIDToRelativePath returns the path to the file relative to the root folder of the backup.
// The term "relative path" here is NOT the same as the "relativePath" field in the manifest DB.
// This relative path is relative to the backup root here, NOT the original path on the iPhone.
// The return value uses the forward slash ("/") as a path separator (as used with fs.FS).
func (fimp FileImporter) fileIDToRelativePath(fileID string) string {
	const requiredLen = 2
	if len(fileID) < requiredLen {
		return fileID // I dunno; this is probably safer than an empty path element?
	}
	return path.Join(fileID[:2], fileID)
}

// loadCommCenter loads information about the device's wireless connectivity / cellular service.
func (fimp FileImporter) loadCommCenter(ctx context.Context) (commCenterInfo, error) {
	commCenterPList, err := fimp.openFile(ctx, wirelessDomain, commCenterFile)
	if err != nil {
		return commCenterInfo{}, err
	}
	defer commCenterPList.Close()

	commCenterPListSeeker, ok := commCenterPList.(io.ReadSeeker)
	if !ok {
		return commCenterInfo{}, fmt.Errorf("commcenter.plist file is not seekable :( %T", commCenterPList)
	}

	dec := plist.NewDecoder(commCenterPListSeeker)

	var commCenter commCenterInfo
	if err := dec.Decode(&commCenter); err != nil {
		return commCenterInfo{}, fmt.Errorf("decoding commcenter.plist file: %w", err)
	}

	return commCenter, nil
}

// domainFS is a fs.FS and fs.StatFS for a specific domain in an iPhone backup.
type domainFS struct {
	fi     *FileImporter
	domain string
}

func (dfs domainFS) Open(name string) (fs.File, error) {
	return dfs.fi.openFile(context.Background(), dfs.domain, name)
}

func (dfs domainFS) Stat(name string) (fs.FileInfo, error) {
	fileID, err := dfs.fi.findFileID(context.Background(), dfs.domain, name)
	if err != nil {
		return nil, err
	}
	return os.Stat(dfs.fi.fileIDToPath(fileID))
}

type commCenterInfo struct {
	SIMPhoneNumber string `plist:"SIMPhoneNumber"`
	PhoneNumber    string `plist:"PhoneNumber"`
}

func (c commCenterInfo) phoneNumber() string {
	phone := c.SIMPhoneNumber
	if phone == "" {
		phone = c.PhoneNumber
	}
	return phone
}

const (
	homeDomain       = "HomeDomain"       // seems to be like the "default" or "system" domain
	cameraRollDomain = "CameraRollDomain" // stores user photos/videos and adjustments, thumbnails
	wirelessDomain   = "WirelessDomain"   // information related to cellular service/connectivity
	mediaDomain      = "MediaDomain"      // contains message attachments

	relativePathContactsDB = "Library/AddressBook/AddressBook.sqlitedb"       // fileID for me: 31bb7ba8914766d4ba40d6dfb6113c8b614be442
	relativePathMessagesDB = "Library/SMS/sms.db"                             // fileID for me: 3d0d7e5fb2ce288813306e4d4636395e047a3d28
	commCenterFile         = "Library/Preferences/com.apple.commcenter.plist" // fileID for me: bfecaa9c467e3acb085a5b312bd27bdd5cd7579a
)
