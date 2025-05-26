package whatsapp

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
)

func attachmentToItem(attachmentDetail string, timestamp time.Time, owner timeline.Entity, dirEntry timeline.DirEntry) *timeline.Item {
	filename, isAttachment := strings.CutPrefix(attachmentDetail, attachPrefix)
	if !isAttachment {
		return nil
	}

	filename = strings.TrimSuffix(strings.TrimSpace(filename), attachSuffix)

	var fileKey timeline.ItemRetrieval
	fileKey.SetKey(fmt.Sprintf("whatsapp-%s-%s", timestamp.Format(time.DateTime), filename))

	return &timeline.Item{
		Classification: timeline.ClassMessage,
		Timestamp:      timestamp,
		Owner:          owner,
		Retrieval:      fileKey,
		Content: timeline.ItemData{
			Filename: filename,
			Data: func(_ context.Context) (io.ReadCloser, error) {
				return dirEntry.FS.Open(filename)
			},
		},
	}
}
