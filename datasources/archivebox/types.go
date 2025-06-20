package archivebox

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// These are the parts of ArchiveBox's index.json that we're interested in
// Reference: https://github.com/ArchiveBox/ArchiveBox#archive-layout
type metadata struct {
	Timestamp  unixtimestamp        `json:"timestamp"`
	Canonical  canonical            `json:"canonical"`
	Hash       string               `json:"hash"`
	History    map[string][]history `json:"history"`
	IsArchived bool                 `json:"is_archived"`
	Title      string               `json:"title"`
	Domain     string               `json:"domain"`
	URL        string               `json:"url"`
}

type canonical struct {
	ArchiveOrgPath  string `json:"archive_org_path"`
	PDFPath         string `json:"pdf_path"`
	ReadabilityPath string `json:"readability_path"`
	ScreenshotPath  string `json:"screenshot_path"`
	SingleFilePath  string `json:"singlefile_path"`
}

type history struct {
	Output string    `json:"output"`
	Start  timestamp `json:"start_ts"`
	End    timestamp `json:"end_ts"`
	Status string    `json:"status"`
}

var ErrInvalidTimestamp = errors.New("not a valid fractional unix timestamp")

type unixtimestamp struct {
	time.Time
}

const microsecondsInSecond int64 = 1_000_000

func (t *unixtimestamp) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return ErrInvalidTimestamp
	}

	secs, err := strconv.ParseFloat(str, 64)
	if err != nil {
		return ErrInvalidTimestamp
	}

	t.Time = time.UnixMicro(int64(secs) * microsecondsInSecond)
	return nil
}

var _ json.Unmarshaler = (*unixtimestamp)(nil)

type timestamp struct {
	time.Time
}

func (t *timestamp) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return ErrInvalidTimestamp
	}

	parsed, err := time.Parse("2006-01-02T15:04:05.000000-07:00", str)
	if err != nil {
		return err
	}

	t.Time = parsed
	return nil
}

var _ json.Unmarshaler = (*timestamp)(nil)
