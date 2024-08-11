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

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"

	"github.com/timelinize/timelinize/datasources/imessage"
	"github.com/timelinize/timelinize/timeline"
)

// (requires iOS 11 and higher)
func (fimp *FileImporter) messages(ctx context.Context) error {
	messagesDBFileID, err := fimp.findFileID(ctx, homeDomain, relativePathMessagesDB)
	if err != nil {
		return err
	}

	db, err := sql.Open("sqlite3", fimp.fileIDToPath(messagesDBFileID)+"?mode=ro")
	if err != nil {
		return fmt.Errorf("opening messages DB: %v", err)
	}
	defer db.Close()

	im := imessage.Importer{
		DB: db,
		FillOutAttachmentItem: func(ctx context.Context, filename string, attachment *timeline.Item) {
			relativeFilename := iMessageAttachmentRelativeFilename(filename)
			attachment.OriginalLocation = fmt.Sprintf("%s:%s", mediaDomain, relativeFilename)
			attachment.IntermediateLocation = iMessageAttachmentFileIDPath(ctx, fimp, relativeFilename)
			attachment.Content.Data = func(ctx context.Context) (io.ReadCloser, error) {
				// TODO: We see this happen sometimes, but I'm not sure why or what the right
				// thing to do is. For example, sometimes the file has path
				// "/var/tmp/com.apple.messages/F1F57D20-CBC4-4780-8991-F28A7BA47B7E/IMG_6262.jpeg"
				// instead of the correct
				// "~/Library/SMS/Attachments/32/02/5439F23C-751C-4B41-89E0-8F8AF11D5DB8/IMG_6262.jpeg".
				// The correct one is in another row in the attachments table, as far as I've seen,
				// but the row is not referenced by any other row or table that I can find. I've
				// noticed the correct row has a start_date and transfer_state > 0, and a larger
				// total_bytes. I don't know if that is generally true, though. My best guess is
				// to try to query for a row where the path has the "~/Library" root and the
				// filename suffix, sort by total_bytes DESC, and if there's only 1, then use it?
				// I have not implemented this kind of logic because I'm not sure if we can query
				// the attachments table while we're scanning the messages table, and we'd surely
				// need some locking -- plus I don't even know if the logic is right. I actually
				// looked up the sample above on the original iPhone and sure enough, the image
				// was not found (the message was there, an attachment with that filename showed,
				// but it had no content -- some sort of error or missing data), even though it's
				// clearly referenced in the DB *and* the photo file itself is in the iPhone backup!
				return fimp.openFile(ctx, mediaDomain, relativeFilename)
			}
		},
		DeviceID: fimp.devicePhoneNumber,
	}

	return im.ImportMessages(ctx, fimp.itemChan, fimp.opt)
}

func iMessageAttachmentRelativeFilename(filename string) string {
	return strings.TrimPrefix(filename, "~/")
}

func iMessageAttachmentFileIDPath(ctx context.Context, fimp *FileImporter, relativeFilename string) string {
	fileID, err := fimp.findFileID(ctx, mediaDomain, relativeFilename)
	if err != nil {
		return ""
	}
	return fimp.fileIDToRelativePath(fileID)
}
