package googlevoice_test

import (
	"context"
	"io"
	"io/fs"
	"reflect"
	"testing"
	"testing/fstest"
	"time"

	"go.uber.org/zap"

	"github.com/timelinize/timelinize/datasources/googlevoice"
	"github.com/timelinize/timelinize/timeline"
)

var easternStandardTime = time.FixedZone("", -5*60*60)

type testParticipant struct {
	name  string
	phone string
}

type testMessage struct {
	timestamp time.Time
	owner     testParticipant
	recipient testParticipant
	text      string
}

func TestFileImportTextConversation(t *testing.T) {
	mockFS := fstest.MapFS{
		"Voice": &fstest.MapFile{
			Mode: fs.ModeDir | 0o755,
		},
		"Voice/Calls": &fstest.MapFile{
			Mode: fs.ModeDir | 0o755,
		},
		"Voice/Calls/Dummy Dummerson - Text - 2011-12-16T01_08_08Z.html": &fstest.MapFile{
			Data: []byte(`<?xml version="1.0" ?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Strict//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-strict.dtd"><html xmlns="http://www.w3.org/1999/xhtml"><head><meta http-equiv="Content-Type" content="text/html; charset=UTF-8" />
<title>Dummy Dummerson</title>
</head>
<body><div class="hChatLog hfeed">
<div class="message"><abbr class="dt" title="2011-12-15T20:08:08.451-05:00">Dec 15, 2011, 8:08:08&#8239;PM
Eastern Time</abbr>:
<cite class="sender vcard"><a class="tel" href="tel:+12125551234"><span class="fn">Dummy Dummerson</span></a></cite>:
<q>Are you there?</q>
</div> <div class="message"><abbr class="dt" title="2011-12-15T20:14:11.911-05:00">Dec 15, 2011, 8:14:11&#8239;PM
Eastern Time</abbr>:
<cite class="sender vcard"><a class="tel" href="tel:+16465559876"><abbr class="fn" title="">Me</abbr></a></cite>:
<q>Yes, Mr. Dummerson. What do you need?</q>
</div> <div class="message"><abbr class="dt" title="2011-12-15T20:48:12.265-05:00">Dec 15, 2011, 8:48:12&#8239;PM
Eastern Time</abbr>:
<cite class="sender vcard"><a class="tel" href="tel:+12125551234"><span class="fn">Dummy Dummerson</span></a></cite>:
<q>Bring me my slippers at once!</q>
</div></div>
<div class="tags">Labels:
<a rel="tag" href="http://www.google.com/voice#inbox">Inbox</a>, <a rel="tag" href="http://www.google.com/voice#sms">Text</a></div>
<div class="deletedStatusContainer">User Deleted:
False</div></body></html>`),
		},
		"Voice/Phones.vcf": &fstest.MapFile{
			Data: []byte(`BEGIN:VCARD
VERSION:3.0
FN:Test User
item1.TEL:+16465559876
END:VCARD`),
		},
	}

	voiceInfo, err := fs.Stat(mockFS, "Voice")
	if got, want := err, error(nil); got != want {
		t.Fatalf("fs.Stat(..., %q) err=%v, want=%v", "Voice", got, want)
	}

	expectedMessages := []testMessage{
		{
			timestamp: time.Date(2011, time.December, 15, 20, 8, 8, 451_000_000, easternStandardTime),
			owner: testParticipant{
				name:  "Dummy Dummerson",
				phone: "+12125551234",
			},
			recipient: testParticipant{
				name:  "Test User",
				phone: "+16465559876",
			},
			text: "Are you there?",
		},
		{
			timestamp: time.Date(2011, time.December, 15, 20, 14, 11, 911_000_000, easternStandardTime),
			owner: testParticipant{
				name:  "Test User",
				phone: "+16465559876",
			},
			recipient: testParticipant{
				name:  "Dummy Dummerson",
				phone: "+12125551234",
			},
			text: "Yes, Mr. Dummerson. What do you need?",
		},
		{
			timestamp: time.Date(2011, time.December, 15, 20, 48, 12, 265_000_000, easternStandardTime),
			owner: testParticipant{
				name:  "Dummy Dummerson",
				phone: "+12125551234",
			},
			recipient: testParticipant{
				name:  "Test User",
				phone: "+16465559876",
			},
			text: "Bring me my slippers at once!",
		},
	}

	pipeline := make(chan *timeline.Graph, len(expectedMessages))

	err = new(googlevoice.FileImporter).FileImport(
		context.Background(),
		timeline.DirEntry{
			DirEntry: fs.FileInfoToDirEntry(voiceInfo),
			FS:       mockFS,
			Filename: "Voice",
		},
		timeline.ImportParams{
			Pipeline: pipeline,
			Log:      zap.NewNop(),
		},
	)
	if got, want := err, error(nil); got != want {
		t.Fatalf("FileImport(...) err=%v, want=%v", got, want)
	}
	close(pipeline)

	actualMessages := mustReadMessagesFromPipeline(t, pipeline)

	if got, want := len(actualMessages), len(expectedMessages); got != want {
		t.Fatalf("len(actualMessages)=%d, want=%d", got, want)
	}

	if got, want := expectedMessages[0], actualMessages[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("got=%#v, want=%#v", got, want)
	}
	if got, want := expectedMessages[1], actualMessages[1]; !reflect.DeepEqual(got, want) {
		t.Errorf("got=%#v, want=%#v", got, want)
	}
	if got, want := expectedMessages[2], actualMessages[2]; !reflect.DeepEqual(got, want) {
		t.Errorf("got=%#v, want=%#v", got, want)
	}
}

func mustReadMessagesFromPipeline(
	t *testing.T,
	pipeline <-chan *timeline.Graph,
) []testMessage {
	t.Helper()

	var actualMessages []testMessage
	for graph := range pipeline {
		recipient := mustReadRecipient(t, graph)

		actualMessages = append(actualMessages, testMessage{
			timestamp: graph.Item.Timestamp,
			owner: testParticipant{
				name:  graph.Item.Owner.Name,
				phone: mustReadPhoneNumber(t, graph.Item.Owner),
			},
			recipient: testParticipant{
				name:  recipient.Name,
				phone: mustReadPhoneNumber(t, *recipient),
			},
			text: mustReadString(t, graph.Item.Content),
		})
	}

	return actualMessages
}

func mustReadRecipient(t *testing.T, graph *timeline.Graph) *timeline.Entity {
	t.Helper()

	if graph.Item.Classification.Name != timeline.ClassMessage.Name {
		t.Fatalf(
			"graph item classification=%q, want=%q",
			graph.Item.Classification.Name,
			timeline.ClassMessage.Name,
		)
	}
	if len(graph.Edges) != 1 {
		t.Fatalf("len(graph.Edges)=%d, want=%d", len(graph.Edges), 1)
	}

	edge := graph.Edges[0]
	if edge.Relation != timeline.RelSent {
		t.Fatalf(
			"graph edge relation=%v, want=%v",
			edge.Relation,
			timeline.RelSent,
		)
	}

	return edge.To.Entity
}

func mustReadPhoneNumber(t *testing.T, entity timeline.Entity) string {
	t.Helper()

	for _, attr := range entity.Attributes {
		if attr.Name != timeline.AttributePhoneNumber {
			continue
		}

		phone, ok := attr.Value.(string)
		if !ok {
			t.Fatalf("phone attribute has type %T, want string", attr.Value)
		}

		return phone
	}

	t.Fatalf("entity %q is missing a phone attribute", entity.Name)
	return ""
}

func mustReadString(t *testing.T, content timeline.ItemData) string {
	t.Helper()

	contentReader, err := content.Data(context.Background())
	if err != nil {
		t.Fatalf("graph content reader err=%v", err)
	}

	contentBytes, err := io.ReadAll(contentReader)
	closeErr := contentReader.Close()
	if err != nil {
		t.Fatalf("graph content read err=%v", err)
	}
	if closeErr != nil {
		t.Fatalf("graph content close err=%v", closeErr)
	}

	return string(contentBytes)
}
