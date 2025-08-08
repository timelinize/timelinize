package iphone

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"strings"

	"github.com/timelinize/timelinize/datasources/applephotos"
	"github.com/timelinize/timelinize/timeline"
)

func (fimp *FileImporter) photosLibrary(ctx context.Context, dirEntry timeline.DirEntry) error {
	photosDBFileID, err := fimp.findFileID(ctx, cameraRollDomain, "Media/PhotoData/Photos.sqlite")
	if err != nil {
		return err
	}

	db, err := sql.Open("sqlite3", fimp.fileIDToPath(photosDBFileID)+"?mode=ro")
	if err != nil {
		return fmt.Errorf("opening photos DB: %w", err)
	}
	defer db.Close()

	var owner timeline.Entity
	if fimp.owner != nil {
		owner = *fimp.owner
	}

	applePhotosImporter := applephotos.FileImporter{
		MediaFileExists: func(dir, filename string) (string, string, bool) {
			relPath := path.Join("Media", dir, filename)
			fileID, err := fimp.findFileID(ctx, cameraRollDomain, relPath)
			return relPath, fimp.fileIDToRelativePath(fileID), err == nil
		},
		SidecarFilename: func(mainFilename string) string {
			// on iPhone, the live photo sidecar to a .HEIC file is the same filename but with a ".MOV" extension
			fileExt := path.Ext(mainFilename)
			filenameNoExt := strings.TrimSuffix(mainFilename, fileExt)
			return filenameNoExt + ".MOV"
		},
	}

	return applePhotosImporter.ProcessPhotosDB(ctx, owner, new(applephotos.Options), db, dirEntry, fimp.opt)
}
