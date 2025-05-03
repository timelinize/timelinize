package whatsapp_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/timelinize/timelinize/datasources/whatsapp"
	"github.com/timelinize/timelinize/timeline"
)

func TestFileImport(t *testing.T) {
	// Setup
	fixtures := os.DirFS("testdata/fixtures")
	dirEntry := timeline.DirEntry{
		FS: fixtures,
	}

	// The buffer for messages is 100 here; significantly more lines than there are in the chat log
	pipeline := make(chan *timeline.Graph, 100)
	params := timeline.ImportParams{Pipeline: pipeline}

	// Run the import
	runErr := new(whatsapp.Importer).FileImport(context.Background(), dirEntry, params)
	if runErr != nil {
		t.Errorf("unable to import file: %v", runErr)
	}
	close(params.Pipeline)

	// Expectations
	expected := []struct {
		owner       string
		index       int
		text        string
		attachments []string
		metadata    map[string]any
	}{
		// Messages and calls are end-to-end encrypted
		// Person 2 changed their phone number
		{owner: "Person 2", index: 3, text: "A message"},
		{owner: "Person 1", index: 4, text: "A reply with _italic_ and *bold* and ~strikethrough~ and `monospace` and emoji ☺️"},
		{owner: "Person 3", index: 5, text: "Someone else\r\nwith a lot to say\r\nover multiple lines!"},
		{owner: "Person 1", index: 6, text: "", attachments: []string{"some-image.jpg"}},
		{owner: "Person 2", index: 7, text: "A retort"},
		// You deleted this message
		{owner: "Person 3", index: 9, text: "How rude"},
		{owner: "Person 1", index: 10, text: "", attachments: []string{"some-doc.pdf"}},
		{owner: "Person 1", index: 11, text: "A question\r\n- Option A (☑︎ 1)\r\n- Option B (☑︎ 2)",
			metadata: map[string]any{
				"Poll Question": "A question",
				"Poll Option 1": "Option A",
				"Poll Votes 1":  "1",
				"Poll Option 2": "Option B",
				"Poll Votes 2":  "2",
			}},
		{owner: "Person 2", index: 12, text: "British Library (96 Euston Rd, London, Greater London NW1 2DB): https://foursquare.com/v/4ac518cef964a52019a620e3",
			metadata: map[string]any{
				"Location Foursquare ID": "4ac518cef964a52019a620e3",
			}},
		{owner: "Person 3", index: 13, text: "Location: https://maps.google.com/?q=51.513767,-0.098266",
			metadata: map[string]any{
				"Location Latitude":  51.513767,
				"Location Longitude": -0.098266,
			}},
		// Missing image means message is ignored
		// Missed voice call omitted
		// Taken video call omitted
		// Deleted message omitted
		{owner: "Person 2", index: 18, text: "An edited message",
			metadata: map[string]any{
				"Edited": true,
			}},
		{owner: "Persona español", index: 19, text: "Una pregunta\r\n- Opción A (☑︎ 0)\r\n- Opción B (☑︎ 1)\r\n- Opción C (☑︎ 2)",
			metadata: map[string]any{
				"Poll Question": "Una pregunta",
				"Poll Option 1": "Opción A",
				"Poll Votes 1":  "0",
				"Poll Option 2": "Opción B",
				"Poll Votes 2":  "1",
				"Poll Option 3": "Opción C",
				"Poll Votes 3":  "2",
			}},
	}

	i := 0
	for message := range pipeline {
		if i >= len(expected) {
			i++
			continue
		}

		if message.Item.Owner.Name != expected[i].owner {
			t.Fatalf("incorrect owner for message %d, wanted %s but was %s", i, expected[i].owner, message.Item.Owner.Name)
		}

		expectedMonth := ((expected[i].index - 1) % 12) + 1
		if int(message.Item.Timestamp.Month()) != expectedMonth {
			t.Fatalf("incorrect month for message %d, wanted %d but was %d", i, expected[i].index, int(message.Item.Timestamp.Month()))
		}
		// TODO: Timestamp timezones need to all be interpreted as "local time, at the time"
		// ie. 2020-01-01 00:00:00 should be interpreted as midnight GMT, but 2020-06-01 00:00:00 should be interpreted as midnight BST
		if message.Item.Timestamp.Hour() != 12 {
			t.Fatalf("message %d should have been near midday, but was %d", i, message.Item.Timestamp.Hour())
		}
		if message.Item.Timestamp.Second() != expected[i].index {
			t.Fatalf("incorrect second for message %d, wanted %d but was %d", i, expected[i].index, message.Item.Timestamp.Second())
		}

		validateItemData(t, "", expected[i].text, message.Item.Content, "incorrect text for message %d", i)

		if len(expected[i].attachments) > 0 {
			var attachedItems []*timeline.Item
			for _, edge := range message.Edges {
				if edge.Relation == timeline.RelAttachment {
					attachedItems = append(attachedItems, edge.To.Item)
				}
			}
			if len(attachedItems) != len(expected[i].attachments) {
				t.Fatalf("incorrect number of attachments for message %d", i)
			}

			for j, filename := range expected[i].attachments {
				expFile, err := fixtures.Open(filename)
				if err != nil {
					t.Errorf("unable to open fixture file (%s) as test data: %v", filename, err)
				}

				validateItemData(t, filename, expFile, attachedItems[j].Content, "incorrect %dth attachment for message %d", j, i)
			}
		}

		i++
	}

	if i != len(expected) {
		t.Fatalf("received %d messages instead of %d", i, len(expected))
	}
}

func validateItemData(t *testing.T, expectedFilename string, expectedData any, itemData timeline.ItemData, errorMessage string, errorArgs ...any) {
	errMsg := fmt.Sprintf(errorMessage, errorArgs...)

	expectedStr, isStr := expectedData.(string)

	var expectedBytes []byte
	if expectedData == nil || (isStr && expectedStr == "") {
		if itemData.Data != nil {
			t.Fatalf("%s; should not have had a DataFunc", errMsg)
		}
		return
	} else if exp, ok := expectedData.(io.Reader); ok {
		var err error
		expectedBytes, err = io.ReadAll(exp)
		if err != nil {
			t.Errorf("%s; couldn't read expected data: %v", errMsg, err)
			return
		}
	} else if isStr {
		expectedBytes = []byte(expectedStr)
	} else if exp, ok := expectedData.([]byte); ok {
		expectedBytes = exp
	} else {
		t.Errorf("%s; unable to check content with expected data type: %T", errMsg, expectedData)
		return
	}

	if itemData.Data == nil {
		t.Fatalf("%s; DataFunc should be non-nil", errMsg)
		return
	}

	actualR, err := itemData.Data(context.Background())
	if err != nil {
		t.Fatalf("%s; unable to retrieve actual data from dataFunc", errMsg)
		return
	}

	actualBytes, err := io.ReadAll(actualR)
	if err != nil {
		t.Fatalf("%s; unable read data from dataFunc reader", errMsg)
		return
	}

	if expectedFilename != "" && itemData.Filename != expectedFilename {
		t.Fatalf("%s; filename incorrect, wanted %s but was %s", errMsg, expectedFilename, itemData.Filename)
	}
	if !bytes.Equal(actualBytes, expectedBytes) {
		t.Fatalf("%s; item data incorrect, wanted:\n %v\n %s\nbut got:\n %v\n %s", errMsg, expectedBytes, string(expectedBytes), actualBytes, string(actualBytes))
	}
}
