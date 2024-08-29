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

package media

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

/*
	Motion pictures ("live photos" and similar) are little videos that some cameras take along with a still capture.
	This is a well-known feature of Google, Samsung, and Apple phone cameras, but there may be others too.

	They are typically stored embedded in the picture file, alongside as a sidecar video file, or both!
	Google and Samsung cameras embed the motion photo in the picture file in an MP4 container. Apple iPhone
	stores the video alongside the photo with the same filename but with a different extension.

	Google Photos (or Takeout?) extracts the embedded videos and places them in a sidecar file so they are
	BOTH embedded and sidecars in that case.

	Some examples:

	iPhone: IMG_1234.HEIC has a sidecar, IMG_1234.MOV
	Google:
		Older: MVIMG_*.jpg has an embedded video, but might also have a sidecar MVIMG_*.mp4. (Actual "jpg" and "mp4" extensions may vary.)
		Newer: *.MP.jpg has an embedded video, but might also have a sidecar *.MP. (Actual image extension may vary.)
	Samsung: I haven't seen any hint in the filename, but they have embedded videos.

	If a video is both embedded and a sidecar, we only need one (we prefer embedded, because we can handle them).
	Though some vendors like Google may put information about the motion photo in EXIF metadata to indicate whether
	one exists, and possibly even its byte offset in the file, there is no standard. What I've found works pretty
	well is to look for the header/beginning of the MP4 container (ASCII byte sequence like "ftypmp4" or "ftypisom")
	and to extract 4 bytes earlier to the end of the file.
*/

// IsSidecarVideo returns true if the given filePath is a video file that accompanies a separate but related
// picture file as its "motion picture" or "live photo".
func IsSidecarVideo(fsys fs.FS, filePath string) bool {
	// if this is a .MOV or .MP4 file, and if there is a file in the same dir with the same name but with a
	// .HEIC, .HEIF, .JPG, or .JPEG extension, this is likely a sidecar "live photo" file (iPhone uses HEIC and JPG)
	vidExt := path.Ext(filePath)
	vidExtLower := strings.ToLower(vidExt)

	switch vidExtLower {
	case ".mov", ".mp4":
		// IMG_1234.MOV is a sidecar for IMG_1234.[HEIC|JPG|JPEG|HEIF]
		for _, imgExt := range []string{".HEIC", ".JPG", ".JPEG", ".HEIF"} {
			// iPhone uses uppercase by default (but just in case, try lower...)
			if vidExt == vidExtLower {
				imgExt = strings.ToLower(imgExt)
			}
			imageFilename := strings.TrimSuffix(filePath, vidExt) + imgExt
			if timeline.FileExistsFS(fsys, imageFilename) {
				return true
			}
		}
	case ".mp":
		// "PXL_1234.MP" is a sidecar for "PXL_1234.MP.jpg"
		if timeline.FileExistsFS(fsys, filePath+".jpg") || timeline.FileExistsFS(fsys, filePath+".JPG") ||
			timeline.FileExistsFS(fsys, filePath+".jpeg") || timeline.FileExistsFS(fsys, filePath+".JPEG") {
			return true
		}
	}

	return false
}

// ConnectMotionPhoto connects a motion photo for the item represented by ig, at path pictureFile in the fsys,
// if one exists. It looks for sidecars and embedded videos. The attached item has the same owner
// as the ig.Item.
func ConnectMotionPhoto(logger *zap.Logger, fsys fs.FS, pictureFile string, ig *timeline.Graph) {
	// only connect a sidecar/external video if the file does not have an embedded video
	// (no need to store two copies of the video, since we do support playing back embedded videos);
	// this heuristic is very loose currently, could be bolstered by checking EXIF metadata perhaps
	if vid, err := ExtractVideoFromMotionPic(fsys, pictureFile); err == nil && len(vid) > 0 {
		return
	}

	// Apple stores "Live Photos" in a sidecar file with the same name (but different extension)
	// in the same directory. Older Google cameras also used to use a .MP sidecar (while the actual
	// picture has a .MP.jpg extension or similar). Look for a sidecar and draw the relevant relationship.
	ext := path.Ext(pictureFile)
	lowExt := strings.ToLower(ext)
	itemPathNoExt := strings.TrimSuffix(pictureFile, ext)

	switch lowExt {
	case ".jpg", ".jpeg", ".jpe", ".heif", ".heic":
	default:
		return
	}

	var sidecarName string

	// look for sidecar first by checking old style Google Photos / Google cameras .MP.jpg extensions
	if strings.HasSuffix(strings.ToLower(itemPathNoExt), ".mp") {
		if timeline.FileExistsFS(fsys, itemPathNoExt) {
			sidecarName = itemPathNoExt
		}
	}

	// if not that, then try iPhone sidecars (.MOV or .MP4 next to .JPG or .HEIC)
	if sidecarName == "" {
		// try the extensions that match the image file's case; they're usually uppercase
		// but I've seen lowercase after some photo libraries export them; e.g. Google Photos
		// exports as .MP4 files even though they are originally .MOV.
		livePhotoExts := []string{".MOV", ".MP4"}
		if ext == lowExt {
			livePhotoExts = []string{".mov", ".mp4"}
		}
		for _, livePhotoExt := range livePhotoExts {
			trySidecarName := itemPathNoExt + livePhotoExt
			if timeline.FileExistsFS(fsys, trySidecarName) {
				sidecarName = trySidecarName
				break
			}
		}
	}

	if sidecarName != "" {
		sidecarItem := &timeline.Item{
			Classification:       timeline.ClassMedia,
			Owner:                ig.Item.Owner,
			IntermediateLocation: sidecarName,
			Content: timeline.ItemData{
				Filename: path.Base(sidecarName),
				Data: func(_ context.Context) (io.ReadCloser, error) {
					return fsys.Open(sidecarName)
				},
			},
		}

		_, err := ExtractAllMetadata(logger, fsys, sidecarName, sidecarItem, timeline.MetaMergeAppend)
		if err != nil {
			logger.Debug("extracting sidecar metadata",
				zap.String("file", sidecarName),
				zap.Error(err))
		}

		ig.ToItem(RelMotionPhoto, sidecarItem)
	}
}

// ExtractVideoFromMotionPic returns the bytes of a video embedded within filename.
// It is a fairly naive but simple approach that scans the bytes for a video header,
// in particular "ftypisom" (modern) or "ftypmp4" (Google no longer uses this, but some
// other vendors likely do), and then reads from the start of that header to the end.
// I know it works for Google and at least some Samsung motion photos, even though they
// embed their photos a little differently. (This is likely very imperfect and kludgey.)
// With our current naive implementation, files that are over 128 MB will not be processed.
//
// If no (recognized) video is found, a nil slice and nil error is returned.
//
// TODO: Until https://github.com/golang/go/issues/44279 is fixed, fsys can be nil to use the local file systems (os.Open).
func ExtractVideoFromMotionPic(fsys fs.FS, filename string) ([]byte, error) {
	// TODO: Older "MVIMG*" files also had "Micro Video Offset" EXIF data that gave the offset from the *END* of the file (https://medium.com/android-news/working-with-motion-photos-da0aa49b50c)

	filename = filepath.Clean(filename)

	// since our naive implementation reads the whole file into memory, skip if large
	const maxFileSize = 1024 * 1024 * 128

	// TODO: os.DirFS() is currently NOT usable cross-platfom; is problematic on Windows (see linked Go issue above)
	var pictureFileBytes []byte
	if fsys == nil {
		info, err := os.Stat(filename)
		if err != nil {
			return nil, err
		}
		if info.Size() > maxFileSize {
			return nil, nil
		}
		pictureFileBytes, err = os.ReadFile(filename)
		if err != nil {
			return nil, err
		}
	} else {
		info, err := fs.Stat(fsys, filename)
		if err != nil {
			return nil, err
		}
		if info.Size() > maxFileSize {
			return nil, nil
		}
		pictureFileBytes, err = fs.ReadFile(fsys, filename)
		if err != nil {
			return nil, err
		}
	}

	// for *.MP.jpg files (and I think older MVIMG_ files too)
	idx := bytes.Index(pictureFileBytes, []byte("ftypisom")) // current standard for Google photos as of Jan 2023
	const vidStartOffset = 4                                 // video file will start this many bytes before the part of the header we locate
	if idx < vidStartOffset {
		// this value appears to be non-standard (see https://www.ftyps.com)
		// but Google cameras used this for quite some time
		idx = bytes.Index(pictureFileBytes, []byte("ftypmp4"))
	}
	if idx < vidStartOffset {
		// no video found
		return nil, nil
	}

	// video file (or "box") starts 4 bytes before the part of the header we identified
	return pictureFileBytes[idx-vidStartOffset:], nil
}
