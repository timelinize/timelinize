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

package applephotos

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/timelinize/timelinize/datasources/imessage"
	"github.com/timelinize/timelinize/datasources/media"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "applephotos",
		Title:           "Apple Photos",
		Icon:            "applephotos.svg",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// Options configures the data source.
type Options struct {
	// The ID of the owner entity. REQUIRED if entity is to be related in DB.
	OwnerEntityID uint64 `json:"owner_entity_id"`

	// If true, items that are trashed will be imported.
	IncludeTrashed bool `json:"include_trashed"`
}

// FileImporter can import from the Apple Contacts database.
type FileImporter struct{}

// Recognize returns whether this file or folder is supported.
func (FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	var confidence float64
	if dirEntry.IsDir() && path.Ext(dirEntry.Name()) == ".photoslibrary" {
		confidence += .2
	}
	if info, err := fs.Stat(dirEntry.FS, "database/Photos.sqlite"); err == nil && !info.IsDir() {
		confidence += .6
	}
	if info, err := fs.Stat(dirEntry.FS, "originals"); err == nil && info.IsDir() {
		confidence += .2
	}
	return timeline.Recognition{Confidence: confidence}, nil
}

// FileImport imports data from the given file or folder.
func (fimp *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	dsOpt := params.DataSourceOptions.(*Options)

	// open photos database
	photosDBPath := filepath.Join(dirEntry.FullPath(), "database", "Photos.sqlite")
	db, err := sql.Open("sqlite3", photosDBPath+"?mode=ro")
	if err != nil {
		return fmt.Errorf("opening photo library DB at %s: %w", photosDBPath, err)
	}
	defer db.Close()

	var owner timeline.Entity
	if dsOpt.OwnerEntityID == 0 {
		// TODO: ZPERSON.ZISMECONFIDENCE seems to be a value of 1.0 for the owner of the library! Use it to make an owner entity.
	}
	if dsOpt.OwnerEntityID > 0 {
		owner = timeline.Entity{ID: dsOpt.OwnerEntityID}
	}

	var trashedState int
	if dsOpt.IncludeTrashed {
		trashedState = 1
	}

	// TODO: Additional Photos DB speculation:
	// ZDETECTEDFACE table: I think ZPERSONBEINGKEYFACE might be which photo is their like cover picture for that face.
	// ZPERSON table: the ZAGETYPE column seems to be something like... child is 2, infant is 1, 5 is middle-aged adult? I dunno

	// sort by the same column that we use to keep track of duplicate asset rows while iterating
	// (it's unclear whether it's better to use ZASSET.Z_PK or ZASSET.ZUUID for this; Z_PK is the
	// row ID, ZUUID is a UUID assigned to the image; both are nullable, but have no null rows)
	rows, err := db.QueryContext(ctx, `
		SELECT
			ZASSET.Z_PK, ZASSET.ZDATECREATED, ZASSET.ZLATITUDE, ZASSET.ZLONGITUDE, ZASSET.ZMODIFICATIONDATE, ZASSET.ZOVERALLAESTHETICSCORE,
			ZASSET.ZDIRECTORY, ZASSET.ZFILENAME, ZASSET.ZORIGINALCOLORSPACE, ZASSET.ZUNIFORMTYPEIDENTIFIER, ZASSET.ZUUID,
			AAA.ZORIGINALFILENAME,
			ZPERSON.ZDETECTIONTYPE, ZPERSON.ZGENDERTYPE, ZPERSON.ZDISPLAYNAME, ZPERSON.ZFULLNAME, ZPERSON.ZPERSONUUID, ZPERSON.ZPERSONURI
		FROM ZASSET
		JOIN ZADDITIONALASSETATTRIBUTES AS AAA ON AAA.Z_PK = ZASSET.ZADDITIONALATTRIBUTES
		LEFT JOIN ZDETECTEDFACE AS FACE ON FACE.ZASSETFORFACE = ZASSET.Z_PK
		LEFT JOIN ZPERSON ON ZPERSON.Z_PK = FACE.ZPERSONFORFACE
		WHERE ZASSET.ZTRASHEDSTATE=?
		ORDER BY ZASSET.Z_PK
	`, trashedState)
	if err != nil {
		return err
	}
	defer rows.Close()

	// since we're left-joining detected faces for each asset row (image), we will likely have
	// multiple asset rows if there are multiple faces in the picture, so we have to keep track
	// of which asset row we're on, using the same column we sort by
	var currentAssetID string
	var currentGraph *timeline.Graph
	for rows.Next() {
		var assetRowID string
		var lat, lon, aestheticScore *float64
		var dateCreated, modDate, dir, filename, colorspace, uniformTypeIdentifier, uuid, originalFilename *string
		var personDetectionType, personGenderType *int
		var personDisplayName, personFullName, personUUID, personURI *string

		err := rows.Scan(&assetRowID, &dateCreated, &lat, &lon, &modDate, &aestheticScore,
			&dir, &filename, &colorspace, &uniformTypeIdentifier, &uuid,
			&originalFilename,
			&personDetectionType, &personGenderType, &personDisplayName, &personFullName, &personUUID, &personURI)
		if err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}

		// see if the file actually exists in the library; if not, the Photos app probably didn't have it downloaded,
		// usually because the "Download Originals to Mac" setting was disabled ("Optimize Mac Storage" is more common)
		if filename == nil || len(*filename) == 0 {
			continue
		}

		// see if we've finished an asset and are moving to the next one
		if assetRowID != currentAssetID {
			// finished asset, starting new one
			currentAssetID = assetRowID

			// if graph is non-empty (at least with previous asset's data), process it
			if currentGraph != nil && currentGraph.Item != nil && currentGraph.Item.Content.Data != nil {
				params.Pipeline <- currentGraph
				currentGraph = nil
			}

			// populate new graph with this asset; we can get most of the item filled out from just this row

			if dir == nil {
				dirTmp := string((*filename)[0])
				dir = &dirTmp
			}
			mediaRelPath := path.Join("originals", *dir, *filename)
			if !timeline.FileExistsFS(dirEntry.FS, mediaRelPath) {
				params.Log.Info("media file not found in library; ensure 'Download Originals to Mac' option is selected in Photos settings",
					zap.Stringp("asset_uuid", uuid),
					zap.String("asset_path", mediaRelPath))
				continue
			}

			// assemble a few basic item details, I think almost always these are non-nil, but technically they can be null in the DB
			itemFilename := *filename
			if originalFilename != nil {
				itemFilename = *originalFilename
			}
			var mediaType string
			if uniformTypeIdentifier != nil {
				// I probably haven't enumerated *every* possible value, but that's okay since the
				// processor will fill in any missing media types (it's just a little extra work to peek)
				mediaType = uniformTypeIdentifierToMIME[*uniformTypeIdentifier]
			}

			// put most of the item together
			item := &timeline.Item{
				Classification:       timeline.ClassMedia,
				IntermediateLocation: mediaRelPath,
				Owner:                owner,
				Content: timeline.ItemData{
					Filename:  itemFilename,
					MediaType: mediaType,
					Data: func(_ context.Context) (io.ReadCloser, error) {
						return dirEntry.Open(mediaRelPath)
					},
				},
				Metadata: make(timeline.Metadata),
			}
			if uuid != nil {
				item.ID = *uuid
			}
			if colorspace != nil {
				item.Metadata["Color space"] = *colorspace
			}
			if aestheticScore != nil {
				item.Metadata["Aesthetic score"] = *aestheticScore
			}

			// get all the metadata out of the file; and prefer timestamp in EXIF data, since I'm not 100% sure that
			// ZDATECREATED from the DB is always the right value
			_, err = media.ExtractAllMetadata(params.Log, dirEntry.FS, mediaRelPath, item, timeline.MetaMergeAppend)
			if err != nil {
				params.Log.Error("extracting media metadata",
					zap.Error(err),
					zap.Stringp("asset_uuid", uuid),
					zap.String("asset_path", mediaRelPath))
			}
			if item.Timestamp.IsZero() && dateCreated != nil {
				if ts, err := imessage.ParseAppleDate(*dateCreated); err == nil {
					item.Timestamp = ts
				}
			}

			// if the file didn't contain location data, the database might have
			if item.Location.IsEmpty() {
				// for some reason, the zero-value of lat/lon is stored as "-180.0" in their DB
				if lat != nil && *lat > -180.0 {
					item.Location.Latitude = lat
				}
				if lon != nil && *lon > -180.0 {
					item.Location.Longitude = lon
				}
			}

			currentGraph = &timeline.Graph{Item: item}

			// Live photo sidecar file. They have the same filename in the same folder as the
			// actual picture, but with a "_3" appended before the extension, which is typically .mov.
			// (there is probably a more proper way to get them using the DB, but I'm not sure how)
			fileExt := path.Ext(mediaRelPath)
			filenameNoExt := strings.TrimSuffix(mediaRelPath, fileExt)
			sidecarPath := filenameNoExt + "_3.mov"
			if timeline.FileExistsFS(dirEntry.FS, sidecarPath) {
				sidecarItem := &timeline.Item{
					Classification:       timeline.ClassMedia,
					Owner:                item.Owner,
					IntermediateLocation: sidecarPath,
					Content: timeline.ItemData{
						Filename: strings.TrimSuffix(itemFilename, strings.ToUpper(fileExt)) + ".MOV",
						Data: func(_ context.Context) (io.ReadCloser, error) {
							return dirEntry.FS.Open(sidecarPath)
						},
					},
				}

				_, err = media.ExtractAllMetadata(params.Log, dirEntry.FS, sidecarPath, sidecarItem, timeline.MetaMergeAppend)
				if err != nil {
					params.Log.Debug("extracting sidecar metadata",
						zap.String("asset_sidecar_path", sidecarPath),
						zap.Error(err))
				}

				currentGraph.ToItem(media.RelMotionPhoto, sidecarItem)
			}
		}

		// for every occurrence of the row, if there is a person (or animal!) detected, relate them to the image
		if personUUID != nil && currentGraph != nil && currentGraph.Item != nil {
			var entityType string
			if personDetectionType != nil {
				switch *personDetectionType {
				case 1:
					entityType = "person"
				case 4:
					// TODO: I don't actually know for sure what 4 is, they show up for cats for me, but they could also be "pet" or "animal"
					// (should we be more specific with our entity types?)
					entityType = "creature"
				}
			}

			var entityName string
			if personFullName != nil {
				entityName = *personFullName
			} else if personDisplayName != nil {
				entityName = *personDisplayName
			}

			var gender string
			if personGenderType != nil {
				switch *personGenderType {
				case 1:
					gender = "male"
				case 2:
					gender = "female"
				}
			}

			// TODO: Let's also grab the centers/heights/widths from the ZDETECTEDFACE table and include them as the value (JSON-encoded?)
			// so we can know where in the photo the face/person is at
			currentGraph.ToEntity(timeline.RelIncludes, &timeline.Entity{
				Type: entityType,
				Name: entityName,
				Metadata: timeline.Metadata{
					// this seems to be their way of connecting it to a contact in the Address Book DB
					"Apple Photos Person URI": personURI,
				},
				Attributes: []timeline.Attribute{
					{
						Name:     "applephotos_uuid", // TODO: Or "applephotos_zperson"?
						Value:    personUUID,
						Identity: true,
					},
					{
						Name:  timeline.AttributeGender,
						Value: gender,
					},
				},
			})
		}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("scanning rows: %w", err)
	}

	// send any final graph down the pipeline before we exit
	if currentGraph != nil && currentGraph.Item != nil && currentGraph.Item.Content.Data != nil {
		params.Pipeline <- currentGraph
	}

	return nil
}

var uniformTypeIdentifierToMIME = map[string]string{
	"public.heic":               "image/heic",
	"public.jpeg":               "image/jpeg",
	"public.png":                "image/png",
	"com.apple.quicktime-movie": "video/quicktime",
}
