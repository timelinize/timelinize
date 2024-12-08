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

// Package email implements a data source for emails (mbox and eml files).
package email

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/mail"
	"path"
	"strings"
	"time"

	"github.com/jhillyerd/enmime"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "email",
		Title:           "Email",
		Icon:            "email.png",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter can import the data from a file.
type FileImporter struct{}

// Recognize returns whether the file is recognized for this data source.
func (fi FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	rec := timeline.Recognition{DirThreshold: 0.9}

	// special case: Google Takeout archive with mail folder
	if timeline.FileExistsFS(dirEntry.FS, googleTakeoutMailFolder) {
		rec.Confidence = 1
		return rec, nil
	}

	// TODO: proper detection, not just filename
	ext := strings.ToLower(path.Ext(dirEntry.Filename))
	if ext == extMbox || ext == extEml {
		rec.Confidence = 1
	}

	return rec, nil
}

// Options configures the data source.
type Options struct {
	// Gmail labels to skip
	GmailSkipLabels []string `json:"gmail_skip_labels"`
}

// FileImport imports data from a file.
func (fi FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	dsOpt := params.DataSourceOptions.(*Options)

	// as a special case, support Google Takeout's "Mail" folder
	walkDir := dirEntry.Filename
	if timeline.FileExistsFS(dirEntry.FS, googleTakeoutMailFolder) {
		walkDir = googleTakeoutMailFolder
	}

	err := fs.WalkDir(dirEntry.FS, walkDir, func(fpath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") {
			// skip hidden files & folders
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil // traverse into subdirectories
		}

		// skip unsupported file types
		ext := path.Ext(strings.ToLower(d.Name()))
		if ext != extEml && ext != extMbox {
			return nil
		}

		file, err := dirEntry.FS.Open(fpath)
		if err != nil {
			return err
		}
		defer file.Close()

		// .eml files are easy: should be just a single message in them
		if ext == extEml {
			msg := message{mboxName: d.Name()}
			fi.processMessage(file, msg, params, dsOpt)
			return nil
		}

		// .mbox files contain multiple messages
		bufr := bufio.NewReader(file)

		// we gradually fill buf with every line we read,
		// and we'll keep current message state in msg
		buf := new(bytes.Buffer)
		msg := message{mboxName: d.Name()}

		// read each line of the mbox file, looking for boundary/separator
		// lines that start with "From ", and fill the buffer up to each one
		for {
			// check for context cancellation
			if err := ctx.Err(); err != nil {
				return err
			}

			line, err := bufr.ReadBytes('\n')
			if errors.Is(err, io.EOF) {
				// don't forget to process last message in file
				fi.processMessage(buf, msg, params, dsOpt)
				break
			}
			if err != nil {
				return err
			}

			// if not at a message boundary, append to buffer and continue
			if !isBoundary(line, buf) {
				buf.Write(line)
				continue
			}

			// reached message boundary

			// process buffered message and reset for next one
			if buf.Len() > 0 {
				fi.processMessage(buf, msg, params, dsOpt)
				buf.Reset()
				msg = message{mboxName: d.Name(), index: msg.index + 1}
			}

			// boundary lines are anything goes, but generally we see a gibberish email address followed by a timestamp
			if err = parseFromLine(&msg, line); err != nil {
				params.Log.Warn("invalid or unrecognized 'From ' boundary line fields", zap.Error(err))
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

func (fi FileImporter) processMessage(r io.Reader, msg message, params timeline.ImportParams, dsOpt *Options) {
	graph, err := fi.messageToGraph(r, msg, params, dsOpt)
	if err != nil {
		params.Log.Error("building item graph from envelope",
			zap.Error(err),
			zap.Int("message_index", msg.index))
	}
	if graph != nil {
		params.Pipeline <- graph
	}
}

func (FileImporter) messageToGraph(r io.Reader, msg message, opt timeline.ImportParams, dsOpt *Options) (*timeline.Graph, error) {
	// parse message
	env, err := enmime.ReadEnvelope(r)
	if err != nil {
		return nil, fmt.Errorf("reading envelope: %w (message_index=%d)", err, msg.index)
	}
	msg.Envelope = env

	// process the result
	ig, err := itemGraphFromEnvelope(msg, opt, dsOpt)
	if err != nil {
		return nil, fmt.Errorf("building item graph from envelope: %w (message_index=%d)", err, msg.index)
	}

	return ig, nil
}

// parseFromLine tries to parse the 'From ' boundary line of a mbox file.
// Boundary lines are "anything goes", but generally we see a gibberish
// email address followed by a timestamp.
func parseFromLine(msg *message, line []byte) error {
	fields := bytes.Fields(line)
	if len(fields) > 1 {
		msg.FromLineEmail = string(fields[1])
	}
	const minFieldsRequired = 8
	if len(fields) >= minFieldsRequired {
		tsStr := string(bytes.Join(fields[2:minFieldsRequired], spaceBytes))
		ts, err := time.Parse("Mon Jan 02 15:04:05 -0700 2006", tsStr)
		if err != nil {
			return fmt.Errorf("parsing timestamp: %w", err)
		}
		msg.FromLineTimestamp = ts
	}
	return nil
}

// itemGraphFromEnvelope builds the message's item graph. It may return nil and nil if the message
// is to be skipped, either because of a severe error that was logged, or configuration options.
func itemGraphFromEnvelope(m message, opt timeline.ImportParams, dsOpt *Options) (*timeline.Graph, error) {
	// checkErrors returns the first severe error, and logs all others.
	checkErrors := func(part *enmime.Part) error {
		for _, err := range part.Errors {
			if err.Severe {
				return err
			}
			opt.Log.Warn(err.Name, zap.String("detail", err.Detail))
		}
		return nil
	}

	// skip message if there is a severe error at the root
	if err := checkErrors(m.Root); err != nil {
		return nil, err
	}

	// skip desired labels
	labels := strings.Split(m.GetHeader("X-Gmail-Labels"), ",")
	for _, skipLabel := range dsOpt.GmailSkipLabels {
		for _, label := range labels {
			if strings.EqualFold(label, skipLabel) {
				return nil, nil
			}
		}
	}

	// TODO: make this configurable if user wants to prefer HTML...
	rootDataText := m.Text
	rootMediaType := "text/plain"
	if rootDataText == "" {
		rootDataText = m.HTML
		rootMediaType = "text/html"
	}

	item := &timeline.Item{
		Classification: timeline.ClassEmail,
		Timestamp:      m.timestamp(),
		Owner:          m.firstFrom(),
		Content: timeline.ItemData{
			Filename:  "", // TODO:...?
			MediaType: rootMediaType,
			Data:      timeline.StringData(rootDataText),
		},
		Metadata: timeline.Metadata{}, // TODO: lots of metadata in headers, probably!
	}

	// create graph and relate recipients to it
	ig := &timeline.Graph{Item: item}
	for _, recipient := range m.to("To") {
		recipCopy := recipient
		ig.ToEntity(timeline.RelSent, &recipCopy)
	}
	for _, cc := range m.to("Cc") {
		ccCopy := cc
		ig.ToEntity(timeline.RelCCed, &ccCopy)
	}

	// add attachments to graph
	for i, attach := range m.Attachments {
		// skip part if there are any severe errors
		if err := checkErrors(attach); err != nil {
			opt.Log.Error("parsing attachment",
				zap.Error(err),
				zap.Int("message_index", m.index),
				zap.Int("attachment_index", i))
			continue
		}

		item := &timeline.Item{
			Classification: timeline.ClassEmail,
			Timestamp:      m.timestamp(), // TODO: if this is an image, could we try to get TS from exif?
			Owner:          m.firstFrom(),
			Content: timeline.ItemData{
				Filename:  attach.FileName,
				MediaType: attach.ContentType,
				Data:      timeline.ByteData(attach.Content),
			},
			Metadata: timeline.Metadata{}, // TODO: lots of metadata in headers, probably!
		}

		ig.ToItem(timeline.RelAttachment, item)
	}

	return ig, nil
}

// isBoundary returns true if line is a boundary/separator line
// starting with "From " and is preceded by an empty line at the
// tail end of buf (or is at the beginning of the file).
func isBoundary(line []byte, buf *bytes.Buffer) bool {
	return bytes.HasPrefix(line, nextMailboxMessage) &&
		(buf.Len() == 0 || // beginning of file
			bytes.HasSuffix(buf.Bytes(), doubleLFbytes) ||
			bytes.HasSuffix(buf.Bytes(), doubleCRLFbytes))
}

// message holds information about a single message/entry in a mailbox (.mbox) file.
type message struct {
	mboxName string // the name of the mbox file
	index    int    // the position of the message in the mbox file (starting at 0)

	FromLineEmail     string    // first field of the "From " separator line
	FromLineTimestamp time.Time // timestamp following the email on the separator line
	*enmime.Envelope            // parsed message contents
}

// timestamp returns the best known timestamp for the message.
func (m message) timestamp() time.Time {
	// prefer Date header
	ts, err := mail.ParseDate(m.Root.Header.Get("Date"))
	if err == nil {
		return ts
	}

	// next, try Received headers... (there's also X-Received; not sure which to use...)
	if recvHeaders := m.Root.Header["Received"]; len(recvHeaders) > 0 {
		// prefer last Received header; these aren't great to rely on, but maybe better than nothing
		for i := len(recvHeaders) - 1; i >= 0; i-- {
			recvHeader := recvHeaders[len(recvHeaders)-1]

			// date usually appears at the end, after a semicolon
			semiColonPos := strings.LastIndex(recvHeader, "; ")
			if semiColonPos > -1 {
				end := strings.TrimSpace(recvHeader[semiColonPos+2:])
				ts, err := time.Parse("Mon, 02 Jan 2006 15:04:05 -0700 (MST)", end)
				if err == nil {
					continue
				}
				return ts
			}
		}
	}

	// last resort, maybe we can use the date in the starting
	// line of this mailbox database entry
	if !m.FromLineTimestamp.IsZero() {
		return m.FromLineTimestamp
	}

	return time.Time{}
}

// firstFrom returns the first person in the "From" header.
func (m message) firstFrom() timeline.Entity {
	froms, err := m.AddressList("From")
	if err == nil && len(froms) > 0 {
		name := froms[0].Name
		if name == froms[0].Address {
			// very common for email to be repeated; leave this empty so a potential
			// future import can fill in this information automatically
			name = ""
		}
		return timeline.Entity{
			Name: name,
			Attributes: []timeline.Attribute{
				{
					Name:     timeline.AttributeEmail,
					Value:    froms[0].Address,
					Identity: true,
				},
			},
		}
	}
	return timeline.Entity{}
}

// to returns all the recipients in the "To" header.
func (m message) to(fieldName string) []timeline.Entity {
	tos, err := m.AddressList(fieldName)
	if err != nil {
		return nil
	}
	persons := make([]timeline.Entity, len(tos))
	for i, to := range tos {
		if to.Name == to.Address {
			// very common for email to be repeated; leave this empty so a potential
			// future import can fill in this information automatically
			to.Name = ""
		}
		persons[i] = timeline.Entity{
			Name: to.Name,
			Attributes: []timeline.Attribute{
				{
					Name:     timeline.AttributeEmail,
					Value:    to.Address,
					Identity: true,
				},
			},
		}
	}
	return persons
}

var (
	nextMailboxMessage = []byte("From ") // prefix of line that separates messages in mailbox files
	spaceBytes         = []byte{' '}
	doubleLFbytes      = []byte("\n\n")
	doubleCRLFbytes    = []byte("\r\n\r\n")
)

const (
	extMbox = ".mbox"
	extEml  = ".eml"
)

const googleTakeoutMailFolder = "Takeout/Mail"
