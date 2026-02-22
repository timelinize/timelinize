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
		Name:            "apple_photos",
		Title:           "Apple Photos",
		Icon:            "apple_photos.svg",
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

// FileImporter can import from the Apple Photos database, whether from Mac or iPhone.
type FileImporter struct {
	// These callback functions allow for reuse in multiple settings: importing directly
	// from a Photos library on a Mac, or from a Photos library on an iPhone backup. Both have
	// different file systems, so these pluggable functions make it possible for us to figure
	// out relevant file paths.
	MediaFileExists func(dir, filename string) (originalLoc, intermediateLoc string, exists bool)
	SidecarFilename func(mainFilename string) string
}

// Recognize returns whether this file or folder is supported.
func (FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	// the Photos.sqlite database file is required (as a file, not a dir)
	if info, err := dirEntry.Stat("database/Photos.sqlite"); err != nil || info.IsDir() {
		return timeline.Recognition{}, nil
	}
	confidence := 0.6
	if dirEntry.IsDir() && path.Ext(dirEntry.Name()) == ".photoslibrary" {
		confidence += .3
	}
	if info, err := dirEntry.Stat(mediaFilesDir); err == nil && info.IsDir() {
		confidence += .1
	}
	return timeline.Recognition{Confidence: confidence}, nil
}

// FileImport imports data from the given file or folder.
func (fimp *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	dsOpt := params.DataSourceOptions.(*Options)

	// set up the file importer for a straight import from a Mac
	fimp.MediaFileExists = func(dir, filename string) (string, string, bool) {
		// to check for the existence of the actual media file, both a directory and a filename
		// are passed in separately because that's what information we have at the time; then
		// later when checking for a sidecar file,
		mediaRelPath := filename
		if dir != "" {
			mediaRelPath = path.Join(mediaFilesDir, dir, filename)
		}
		// we don't really know the "original" location of the photo, since the photos app is
		// typically just doing cloud sync -- it's unlikely that this Mac took the original
		// picture... anyway, hard to say which is which in this case, but at least by choosing
		// intermediate, we're not crowding out any possible original location of the file
		return "", mediaRelPath, timeline.FileExistsFS(dirEntry.FS, mediaRelPath)
	}
	fimp.SidecarFilename = func(mainFilename string) string {
		// on Mac, the sidecar files have the same filename as the actual picture, but with a "_3"
		// appended before the extension, which is typically ".mov".
		// (there is probably a more proper way to get them using the DB, but I'm not sure how)
		fileExt := path.Ext(mainFilename)
		filenameNoExt := strings.TrimSuffix(mainFilename, fileExt)
		return filenameNoExt + "_3.mov"
	}

	// open photos database
	photosDBPath := filepath.Join(dirEntry.FullPath(), "database", "Photos.sqlite")
	db, err := sql.Open("sqlite3", photosDBPath+"?mode=ro")
	if err != nil {
		return fmt.Errorf("opening photo library DB at %s: %w", photosDBPath, err)
	}
	defer db.Close()

	return fimp.ProcessPhotosDB(ctx, timeline.Entity{}, dsOpt, db, dirEntry, params)
}

func (fimp *FileImporter) ProcessPhotosDB(ctx context.Context, owner timeline.Entity, dsOpt *Options, photosDB *sql.DB, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	if owner.IsEmpty() {
		if dsOpt.OwnerEntityID > 0 {
			// use preset owner entity configured by user
			owner = timeline.Entity{ID: dsOpt.OwnerEntityID}
		} else {
			// ZPERSON.ZISMECONFIDENCE seems to be a value of 1.0 for the owner of the library! Maybe we can use it to infer the owner entity if not preset.
			var personDetectionType, personGenderType *int
			var personDisplayName, personFullName, personUUID, personURI *string
			err := photosDB.QueryRowContext(ctx, `
			SELECT ZDETECTIONTYPE, ZGENDERTYPE, ZDISPLAYNAME, ZFULLNAME, ZPERSONUUID, ZPERSONURI
			FROM ZPERSON
			WHERE ZISMECONFIDENCE=1.0 AND (ZDISPLAYNAME IS NOT NULL OR ZFULLNAME IS NOT NULL)
			LIMIT 1`).Scan(&personDetectionType, &personGenderType, &personDisplayName, &personFullName, &personUUID, &personURI)
			if err != nil {
				params.Log.Error("could not infer owner entity from DB", zap.Error(err))
			} else {
				owner = makeEntity(personDetectionType, personGenderType, personFullName, personDisplayName, personUUID, personURI)
			}
		}
	}

	var trashedState int
	if dsOpt.IncludeTrashed {
		trashedState = 1
	}

	args := []any{trashedState}

	// honor configured timeframe, which can greatly speed up such imports
	var andClause string
	if !params.Timeframe.IsEmpty() {
		if params.Timeframe.Since != nil {
			appleTsStart := imessage.TimeToCocoaSecondsWithMilli(*params.Timeframe.Since)
			andClause += "\n\t\t\tAND ZASSET.ZDATECREATED >= ?"
			args = append(args, appleTsStart)
		}
		if params.Timeframe.Until != nil {
			appleTsEnd := imessage.TimeToCocoaSecondsWithMilli(*params.Timeframe.Until)
			andClause += "\n\t\t\tAND ZASSET.ZDATECREATED < ?"
			args = append(args, appleTsEnd)
		}
	}

	// sort by the same column that we use to keep track of duplicate asset rows while iterating
	// (it's unclear whether it's better to use ZASSET.Z_PK or ZASSET.ZUUID for this; Z_PK is the
	// row ID, ZUUID is a UUID assigned to the image; both are nullable, but have no null rows)
	// (there are a lot more columns we can get interesting data from, I just chose a few of my favorites)
	//nolint:gosec // string concatenation is done safely
	rows, err := photosDB.QueryContext(ctx, `
		SELECT
			ZASSET.Z_PK, ZASSET.ZDATECREATED, ZASSET.ZLATITUDE, ZASSET.ZLONGITUDE, ZASSET.ZMODIFICATIONDATE, ZASSET.ZOVERALLAESTHETICSCORE,
			ZASSET.ZDIRECTORY, ZASSET.ZFILENAME, ZASSET.ZORIGINALCOLORSPACE, ZASSET.ZUNIFORMTYPEIDENTIFIER, ZASSET.ZUUID,
			AAA.ZORIGINALFILENAME,
			ZPERSON.ZDETECTIONTYPE, ZPERSON.ZGENDERTYPE, ZPERSON.ZDISPLAYNAME, ZPERSON.ZFULLNAME, ZPERSON.ZPERSONUUID, ZPERSON.ZPERSONURI,
			FACE.ZEYESSTATE, FACE.ZFACEEXPRESSIONTYPE, FACE.ZFACIALHAIRTYPE,
			FACE.ZGLASSESTYPE, FACE.ZHASFACEMASK, FACE.ZHASSMILE, FACE.ZISLEFTEYECLOSED, FACE.ZISRIGHTEYECLOSED,
			FACE.ZPOSETYPE, FACE.ZSMILETYPE, FACE.ZBODYCENTERX, FACE.ZBODYCENTERY, FACE.ZBODYHEIGHT, FACE.ZBODYWIDTH,
			FACE.ZCENTERX, FACE.ZCENTERY, FACE.ZGAZECENTERX, FACE.ZGAZECENTERY, FACE.ZSIZE
		FROM ZASSET
		JOIN ZADDITIONALASSETATTRIBUTES AS AAA ON AAA.Z_PK = ZASSET.ZADDITIONALATTRIBUTES
		LEFT JOIN ZDETECTEDFACE AS FACE ON FACE.ZASSETFORFACE = ZASSET.Z_PK
		LEFT JOIN ZPERSON ON ZPERSON.Z_PK = FACE.ZPERSONFORFACE
		WHERE (ZASSET.ZTRASHEDSTATE=0 OR ZASSET.ZTRASHEDSTATE=?)`+andClause+`
		ORDER BY ZASSET.Z_PK
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	// Additional Photos DB speculation:
	// ZDETECTEDFACE table: I think ZPERSONBEINGKEYFACE might be which photo is their like cover picture for that face. Maybe related to ZPERSON.ZKEYFACE
	// ZPERSON table: the ZAGETYPE column seems to be something like... child is 2, infant is 1, 5 is middle-aged adult? I dunno

	// since we're left-joining detected faces for each asset row (image), we will likely have
	// multiple asset rows if there are multiple faces in the picture, so we have to keep track
	// of which asset row we're on, using the same column we sort by
	var currentAssetID string
	var currentGraph *timeline.Graph
	for rows.Next() {
		var assetRowID string
		var lat, lon, aestheticScore, fBodyCenterX, fBodyCenterY, fBodyHeight, fBodyWidth, fCenterX, fCenterY, fGazeCenterX, fGazeCenterY, fSize *float64
		var dateCreated, modDate, dir, filename, colorspace, uniformTypeIdentifier, uuid, originalFilename *string
		var pDetectionType, fEyesState, fExprType, fFacialHairType, pGenderType, fGlassesType, fHasFacemask, fHasSmile, fLeftEyeClosed, fRightEyeClosed, fPoseType, fSmileType *int
		var pDisplayName, pFullName, pUUID, pURI *string

		err := rows.Scan(&assetRowID, &dateCreated, &lat, &lon, &modDate, &aestheticScore,
			&dir, &filename, &colorspace, &uniformTypeIdentifier, &uuid,
			&originalFilename,
			&pDetectionType, &pGenderType, &pDisplayName, &pFullName, &pUUID, &pURI,
			&fEyesState, &fExprType, &fFacialHairType,
			&fGlassesType, &fHasFacemask, &fHasSmile, &fLeftEyeClosed, &fRightEyeClosed,
			&fPoseType, &fSmileType, &fBodyCenterX, &fBodyCenterY, &fBodyHeight, &fBodyWidth,
			&fCenterX, &fCenterY, &fGazeCenterX, &fGazeCenterY, &fSize)
		if err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}

		// see if the file actually exists in the library; if not, the Photos app probably didn't have it downloaded,
		// usually because the "Download Originals to Mac" setting was disabled ("Optimize Mac Storage" is more common)
		if filename == nil || dir == nil {
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

			mediaOriginalPath, mediaRelPath, exists := fimp.MediaFileExists(*dir, *filename)
			if !exists {
				params.Log.Warn("media file not found in library; ensure Photos app is configured to 'Download and Keep Originals' (iPhone) or 'Download Originals to Mac' (Mac) and then try import again",
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
				// I probably haven't enumerated *every* possible value, but that's okay since the processor
				// will fill in any missing media types (this just saves a little extra peeking work)
				mediaType = uniformTypeIdentifierToMIME[*uniformTypeIdentifier]
			}

			// put most of the item together
			item := &timeline.Item{
				Classification:       timeline.ClassMedia,
				OriginalLocation:     mediaOriginalPath,
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
			if modDate != nil {
				if ts, err := imessage.ParseCocoaDate(*modDate); err == nil {
					item.Metadata["Modified"] = ts
				}
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
				if ts, err := imessage.ParseCocoaDate(*dateCreated); err == nil {
					item.Timestamp = ts
				}
			}

			// if the file didn't contain location data, the database might have
			// (for some reason, the zero-value of lat/lon is stored as "-180.0" in their DB)
			if item.Location.Latitude == nil && lat != nil && *lat > -180.0 {
				item.Location.Latitude = lat
			}
			if item.Location.Longitude == nil && lon != nil && *lon > -180.0 {
				item.Location.Longitude = lon
			}

			currentGraph = &timeline.Graph{Item: item}

			// Live photo sidecar file. They exist with a similar filename in the same folder as the actual picture.
			sidecarFilename := fimp.SidecarFilename(*filename)
			if origLoc, intermediateLoc, exists := fimp.MediaFileExists(*dir, sidecarFilename); exists {
				sidecarItem := &timeline.Item{
					Classification:       timeline.ClassMedia,
					Owner:                item.Owner,
					OriginalLocation:     origLoc,
					IntermediateLocation: intermediateLoc,
					Content: timeline.ItemData{
						Filename: sidecarFilename,
						Data: func(_ context.Context) (io.ReadCloser, error) {
							return dirEntry.Open(intermediateLoc)
						},
					},
				}

				_, err = media.ExtractAllMetadata(params.Log, dirEntry.FS, intermediateLoc, item, timeline.MetaMergeAppend)
				if err != nil {
					params.Log.Debug("extracting sidecar metadata",
						zap.String("asset_sidecar_path", intermediateLoc),
						zap.Error(err))
				}

				currentGraph.ToItem(media.RelMotionPhoto, sidecarItem)
			}
		}

		// for every occurrence of the row, if there is a person (or animal!) detected, relate them to the image
		if pUUID != nil && currentGraph != nil && currentGraph.Item != nil {
			entity := makeEntity(pDetectionType, pGenderType, pFullName, pDisplayName, pUUID, pURI)

			rel := timeline.Relationship{
				Relation: timeline.RelIncludes,
				To:       &timeline.Graph{Entity: &entity},
				Metadata: timeline.Metadata{
					// TODO: Maybe some slight interpretation on these values / make them more meaningful? will have to reverse-engineer some of them
					"Body center X":    fBodyCenterX,
					"Body center Y":    fBodyCenterY,
					"Body height":      fBodyHeight,
					"Body width":       fBodyWidth,
					"Center X":         fCenterX,
					"Center Y":         fCenterY,
					"Size":             fSize,
					"Eyes state":       fEyesState,
					"Expression":       fExprType,
					"Facial hair":      fFacialHairType,
					"Glasses":          fGlassesType,
					"Facemask":         fHasFacemask != nil && *fHasFacemask == 1,
					"Smile":            fHasSmile != nil && *fHasSmile == 1,
					"Left eye closed":  fLeftEyeClosed != nil && *fLeftEyeClosed == 1,
					"Right eye closed": fRightEyeClosed != nil && *fRightEyeClosed == 1,
					"Pose":             fPoseType,
					"Smile type":       fSmileType,
				},
				// TODO: should the value be some JSON-encoded object that the UI can use to highlight the person in the image?
			}
			if fGazeCenterX != nil && *fGazeCenterX > -1.0 {
				rel.Metadata["Gaze center X"] = fGazeCenterX
			}
			if fGazeCenterY != nil && *fGazeCenterY > -1.0 {
				rel.Metadata["Gaze center Y"] = fGazeCenterY
			}

			currentGraph.Edges = append(currentGraph.Edges, rel)
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

func makeEntity(personDetectionType, personGenderType *int, personFullName, personDisplayName, personUUID, personURI *string) timeline.Entity {
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

	return timeline.Entity{
		Type: entityType,
		Name: entityName,
		Metadata: timeline.Metadata{
			// this seems to be their way of connecting it to a contact in the Address Book DB
			"Apple Photos Person URI": personURI,
		},
		Attributes: []timeline.Attribute{
			{
				Name:     "apple_photos_zperson",
				Value:    personUUID,
				Identity: true,
			},
			{
				Name:  timeline.AttributeGender,
				Value: gender,
			},
		},
	}
}

var uniformTypeIdentifierToMIME = map[string]string{
	"public.heic":               "image/heic",
	"public.jpeg":               "image/jpeg",
	"public.png":                "image/png",
	"com.apple.quicktime-movie": "video/quicktime",
}

// the name of the directory in the photos library that contains the full-size media files
const mediaFilesDir = "originals"
