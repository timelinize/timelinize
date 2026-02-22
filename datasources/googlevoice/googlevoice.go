package googlevoice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/signal-golang/go-vcard"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
	"golang.org/x/net/html"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "google_voice",
		Title:           "Google Voice",
		Icon:            "google_voice.svg",
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter can import the data from a file.
type FileImporter struct {
	owner timeline.Entity
}

// Recognize returns whether this input is supported.
func (FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	// not a match if the file is not a directory
	if !dirEntry.IsDir() {
		return timeline.Recognition{}, nil
	}

	var rec timeline.Recognition
	if dirEntry.FileExists("Phones.vcf") {
		rec.Confidence += .4
	}
	if dirEntry.FileExists("Bills.html") {
		rec.Confidence += .15
	}
	if dirEntry.FileExists("Calls") {
		rec.Confidence += .4
	}
	if dirEntry.Name() == "Voice" {
		rec.Confidence += .05
	}

	return rec, nil
}

// FileImport imports data from the input file.
func (fimp *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	var err error
	fimp.owner, err = fimp.getOwner(dirEntry)
	if err != nil {
		params.Log.Error("failed getting owner information", zap.Error(err))
	}

	err = fs.WalkDir(dirEntry, "Calls", func(fpath string, d fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		err = fimp.processConversationFile(ctx, dirEntry, params, fpath)
		if err != nil {
			params.Log.Error("processing conversation file", zap.String("path", fpath), zap.Error(err))
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("walking call files: %w", err)
	}

	return nil
}

func (*FileImporter) getOwner(dirEntry timeline.DirEntry) (timeline.Entity, error) {
	var owner timeline.Entity

	f, err := dirEntry.Open("Phones.vcf")
	if err != nil {
		return owner, err
	}
	defer f.Close()

	dec := vcard.NewDecoder(f)

	for {
		card, err := dec.Decode()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return owner, err
		}

		// usually empty from what I've seen, but meh, try anyway
		owner.Name = card.PreferredValue(vcard.FieldFormattedName)

		for _, field := range card["TEL"] {
			if field.Group != "" {
				// this is a GVOICE number
				var label string
				if labels, ok := card[field.Group+".X-ABLabel"]; ok && len(labels) > 0 {
					label = labels[0].Value
				}
				owner.Attributes = append(owner.Attributes, timeline.Attribute{
					Name:     timeline.AttributePhoneNumber,
					Value:    field.Value,
					Identity: true,
					Metadata: timeline.Metadata{
						gvoiceGroup: field.Group,
						"Label":     label,
					},
				})
			} else if len(field.Params.Types()) > 0 {
				// this is their regular phone number
				owner.Attributes = append(owner.Attributes, timeline.Attribute{
					Name:        timeline.AttributePhoneNumber,
					Value:       field.Value,
					Identifying: true,
				})
			}
		}
	}

	return owner, nil
}

func (fimp *FileImporter) ownerGVoiceNumber() string {
	for _, attr := range fimp.owner.Attributes {
		if attr.Name == timeline.AttributePhoneNumber && attr.Metadata[gvoiceGroup] != "" {
			return attr.Value.(string)
		}
	}
	return ""
}

func (fimp *FileImporter) processConversationFile(_ context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams, conversationFilePath string) error {
	f, err := dirEntry.Open(conversationFilePath)
	if err != nil {
		return err
	}
	defer f.Close()

	doc, err := goquery.NewDocumentFromReader(f)
	if err != nil {
		return err
	}

	participants := []timeline.Entity{fimp.owner}

	// addUniqueParticipant adds an entity to the participants list only if it's not already there
	// (specifically, if an identifying attribute is the same as one for an entity already in the list)
	addUniqueParticipant := func(entityToAdd timeline.Entity) {
		if len(entityToAdd.Attributes) == 0 {
			return
		}
		// participant data SHOULD all be the same within the file, so just do a simple check if we've already got this person
		var isDuplicate bool
	outer:
		for _, attr := range entityToAdd.Attributes {
			for _, existing := range participants {
				for _, existingAttr := range existing.Attributes {
					if (attr.Identity || attr.Identifying) && existingAttr.Name == attr.Name && existingAttr.Value == attr.Value {
						isDuplicate = true
						break outer
					}
				}
			}
		}
		if !isDuplicate {
			participants = append(participants, entityToAdd)
		}
	}

	// group conversation files list the participants explicitly
	doc.Find(".participants .sender").Each(func(_ int, s *goquery.Selection) {
		participant := entityFromSenderCiteTag(s)
		addUniqueParticipant(participant)
	})

	// when the participants aren't listed, we can still figure it out from scanning the log,
	// but only if the other participant(s) sent a message
	if len(participants) == 1 {
		doc.Find(".hChatLog .message .sender").Each(func(_ int, s *goquery.Selection) {
			participant := entityFromSenderCiteTag(s)
			addUniqueParticipant(participant)
		})
	}

	// last resort, we can get something from the file name if it's a one-on-one conversation file
	if len(participants) == 1 {
		// get other entity info from file title; it could be name or phone number
		const expectedParts = 2
		var other timeline.Entity
		parts := strings.SplitN(path.Base(conversationFilePath), " - Text - ", expectedParts)
		if len(parts) == expectedParts {
			var name, phone string
			nameOrPhone := parts[0]
			if strings.HasPrefix(nameOrPhone, "+") || isShortPhoneNum(nameOrPhone) {
				phone = nameOrPhone
			} else {
				name = nameOrPhone
			}
			other = timeline.Entity{
				Name: name,
				Attributes: []timeline.Attribute{
					{
						Name:     timeline.AttributePhoneNumber,
						Value:    phone,
						Identity: true,
					},
					{
						// regrettably, this is required, since sometimes we only have their name
						Name:        googleVoiceNameAttr,
						Value:       name,
						Identifying: true,
					},
				},
			}
			addUniqueParticipant(other)
		}
	}

	// now iterate each message and create the item graphs
	doc.Find(".hChatLog .message").Each(func(_ int, s *goquery.Selection) {
		when := s.Find(".dt").AttrOr("title", "")
		from := s.Find(".sender .tel").AttrOr("href", "")
		msg := extractTextWithNewlines(s.Find("q"))

		ts, err := time.Parse(time.RFC3339, when)
		if err != nil {
			// TODO: could try parsing it from the text content with HTML entities, but the time zone isn't standard... example input: "Jul 17, 2021, 12:16:16&#8239;PM Mountain Time"
			params.Log.Error("message has invalid or missing RFC 3339 timestamp", zap.String("input", when))
		}

		sender := entityFromSenderCiteTag(s.Find(".sender"))
		senderPhoneNum := strings.TrimPrefix(from, telPrefix)

		// owner entity may have more info associated with it, so use that instead of just the phone number given here
		if senderPhoneNum == fimp.ownerGVoiceNumber() {
			sender = fimp.owner
		}

		item := &timeline.Item{
			Classification: timeline.ClassMessage,
			Timestamp:      ts,
			Owner:          sender,
		}

		textData := strings.TrimSpace(msg)
		if textData != "MMS Received" {
			item.Content = timeline.ItemData{
				MediaType: "text/plain",
				Data:      timeline.StringData(textData),
			}
		}

		g := &timeline.Graph{Item: item}

		// connect all participants who are not the sender as recipients of the message
	nextParticipant:
		for _, participant := range participants {
			for _, partAttr := range participant.Attributes {
				if partAttr.Name == timeline.AttributePhoneNumber && partAttr.Value == senderPhoneNum {
					continue nextParticipant
				}
			}
			g.ToEntity(timeline.RelSent, &participant)
		}

		// media attachments
		s.Find("img").Each(func(_ int, s *goquery.Selection) {
			imgSrc := s.AttrOr("src", "")
			if imgSrc == "" {
				return
			}

			// believe it or not, the <img src> is BROKEN since it doesn't contain the file extension -- we have to try a few common ones!
			var filename string
			for _, tryExt := range []string{
				".jpg",
				".gif",
			} {
				tryFilename := path.Join(path.Dir(conversationFilePath), imgSrc+tryExt)
				if dirEntry.FileExists(tryFilename) {
					filename = tryFilename
					break
				}
			}
			if filename == "" {
				params.Log.Warn("unable to determine actual filename of attachment", zap.String("filename_stub", imgSrc))
				return
			}

			attachmentData := func(_ context.Context) (io.ReadCloser, error) {
				return dirEntry.Open(filename)
			}

			// if the message has no text content, make the first attachment the content
			if item.Content.Data == nil {
				item.IntermediateLocation = filename
				item.Content.MediaType = "" // just let the processor sniff it out
				item.Content.Filename = path.Base(filename)
				item.Content.Data = attachmentData
			} else {
				attachment := &timeline.Item{
					Classification:       timeline.ClassMessage,
					Timestamp:            ts,
					Owner:                sender,
					IntermediateLocation: filename,
					Content: timeline.ItemData{
						Filename: path.Base(filename),
						Data:     attachmentData,
					},
				}
				g.ToItem(timeline.RelAttachment, attachment)
			}
		})

		params.Pipeline <- g
	})

	return nil
}

// entityFromSenderCiteTag returns the entity described by the given selection, which should be
// a <cite class="sender"> tag.
func entityFromSenderCiteTag(s *goquery.Selection) timeline.Entity {
	phoneNum := strings.TrimPrefix(s.Find(".tel").AttrOr("href", ""), telPrefix)
	formattedName := s.Find(".fn").Text() // this will actually be the phone number if name is not known / not in contacts
	if formattedName == phoneNum {
		formattedName = ""
	}
	entity := timeline.Entity{
		Name: formattedName,
	}
	if phoneNum != "" {
		entity.Attributes = append(entity.Attributes, timeline.Attribute{
			Name:     timeline.AttributePhoneNumber,
			Value:    phoneNum,
			Identity: true,
		})
	}
	if formattedName != "" {
		// this is, regrettably, required to be an attribute and an identifying one at that, since we don't always have the person's
		// phone number when given their name, since in one-on-one conversation files, the data source only gives us either their name
		// or their phone number in the filename, and we only get both if they sent a message in the conversation
		// (without this, duplicate entities end up getting created, since we can't link them just by the name; it has to be an identifying attribute)
		entity.Attributes = append(entity.Attributes, timeline.Attribute{
			Name:        googleVoiceNameAttr,
			Value:       formattedName,
			Identifying: true,
		})
	}
	return entity
}

func isShortPhoneNum(s string) bool {
	if s == "" {
		return false
	}
	const maxLen = 6
	if len(s) > maxLen {
		return false
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// extractTextWithNewlines is like calling .Text(), but it
// inserts '\n' where <br> are, or "\n\n" where </p> tags are.
func extractTextWithNewlines(sel *goquery.Selection) string {
	var sb strings.Builder

	sel.Contents().Each(func(_ int, s *goquery.Selection) {
		node := s.Get(0)
		switch node.Type {
		case html.TextNode:
			sb.WriteString(strings.TrimSpace(node.Data))
		case html.ElementNode:
			tag := node.Data
			switch tag {
			case "br":
				sb.WriteString("\n")
			case "p":
				// Recurse for children
				sb.WriteString(extractTextWithNewlines(s))
				sb.WriteString("\n\n") // paragraph break
			default:
				sb.WriteString(extractTextWithNewlines(s))
			}
		}
	})

	return sb.String()
}

// Phone numbers are prefixed with this in the data
const telPrefix = "tel:"

// This metadata key appears on Google Voice number entity attributes
const gvoiceGroup = "Google Voice Group"

// The name of the attribute that we use to specify the person's name
// as it appears in Google Voice (typically from their Google Contact list,
// though it could also be a public business or something) -- this is
// unfortunately needed since sometimes all we have about a participant
// is their name, if they didn't send any messages in the conversation
const googleVoiceNameAttr = "gvoice_name"
