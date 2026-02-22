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

package twitter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/url"
	"path"
	"strings"

	"github.com/timelinize/timelinize/timeline"
)

// Recognize returns whether the input is supported.
func (Client) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	manifestFile, err := dirEntry.Open(manifestFile)
	if errors.Is(err, fs.ErrNotExist) {
		return timeline.Recognition{}, nil
	} else if err != nil {
		return timeline.Recognition{}, nil
	}
	defer manifestFile.Close()

	if err := stripPreface(manifestFile); err != nil {
		return timeline.Recognition{}, err
	}

	var manifest archiveManifest
	if err = json.NewDecoder(manifestFile).Decode(&manifest); err != nil {
		return timeline.Recognition{}, err
	}

	return timeline.Recognition{
		Confidence:   1,
		SnapshotDate: &manifest.ArchiveInfo.GenerationDate,
	}, nil
}

// FileImport imports data from the input.
func (c *Client) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	dsOpt := *params.DataSourceOptions.(*Options)

	// load the owner info
	acc, err := c.loadOwnerAccountFromArchive(dirEntry.FS)
	if err != nil {
		return fmt.Errorf("unable to get owner account: %w", err)
	}
	c.owner = acc.entity(ctx, dirEntry.FS)

	// first pass - add tweets to timeline
	err = c.processArchive(ctx, dirEntry.FS, params, c.makeItemGraphFromTweet, dsOpt)
	if err != nil {
		return fmt.Errorf("processing tweets: %w", err)
	}

	// TODO: not ready yet
	// // second pass - add tweet relationships to timeline
	// err = c.processArchive(ctx, fsys, itemChan, c.processReplyRelationFromArchive, dsOpt)
	// if err != nil {
	// 	return fmt.Errorf("processing tweets round 2: %v", err)
	// }

	err = c.processDirectMessages(ctx, dirEntry.FS, params, dsOpt)
	if err != nil {
		return fmt.Errorf("processing direct messages: %w", err)
	}

	return nil
}

func (c *Client) processArchive(ctx context.Context, fsys fs.FS, params timeline.ImportParams, processFunc archiveProcessFn, opt Options) error {
	file, err := fsys.Open(tweetsFile)
	if errors.Is(err, fs.ErrNotExist) {
		file, err = fsys.Open(tweetsFile2022)
	}
	if err != nil {
		return err
	}
	defer file.Close()

	// consume non-JSON preface (JavaScript variable definition)
	err = stripPreface(file)
	if err != nil {
		return fmt.Errorf("reading tweet file preface: %w", err)
	}

	err = c.processTweetsFromArchive(ctx, params, file, fsys, processFunc, opt)
	if err != nil {
		return fmt.Errorf("processing tweet file: %w", err)
	}

	return nil
}

func (c *Client) processDirectMessages(ctx context.Context, fsys fs.FS, params timeline.ImportParams, _ Options) error {
	file, err := fsys.Open(dmsFile)
	if err != nil {
		return err
	}
	defer file.Close()

	// consume non-JSON preface (JavaScript variable definition)
	err = stripPreface(file)
	if err != nil {
		return fmt.Errorf("reading direct messages file preface: %w", err)
	}

	dec := json.NewDecoder(file)

	// read array opening bracket '['
	_, err = dec.Token()
	if err != nil {
		return fmt.Errorf("decoding opening token: %w", err)
	}

	for dec.More() {
		if err := ctx.Err(); err != nil {
			return err
		}

		var convo directMessages
		err := dec.Decode(&convo)
		if err != nil {
			return fmt.Errorf("decoding conversation element: %w", err)
		}

		for _, msg := range convo.DMConversation.Messages {
			// replace the annoying t.co links with the real links
			text := msg.MessageCreate.Text
			for _, u := range msg.MessageCreate.URLs {
				text = strings.ReplaceAll(text, u.URL, u.Expanded)
			}
			text = html.UnescapeString(text)

			ig := &timeline.Graph{
				Item: &timeline.Item{
					ID:             msg.MessageCreate.ID,
					Classification: timeline.ClassMessage,
					Timestamp:      msg.MessageCreate.CreatedAt,
					Owner: timeline.Entity{
						Attributes: []timeline.Attribute{
							{
								Name:     identityAttribute,
								Value:    msg.MessageCreate.SenderID,
								Identity: true,
							},
						},
					},
					Content: timeline.ItemData{
						Data: timeline.StringData(text),
					},
				},
			}

			// relate recipient
			ig.ToEntity(timeline.RelSent, &timeline.Entity{
				Attributes: []timeline.Attribute{
					{
						Name:     identityAttribute,
						Value:    msg.MessageCreate.RecipientID,
						Identity: true,
					},
				},
			})

			// attach any media
			for _, m := range msg.MessageCreate.MediaURLs {
				u, err := url.Parse(m)
				if err != nil {
					continue
				}

				parts := strings.Split(u.Path, "/")

				filename := path.Join("data", "direct_messages_media")
				switch len(parts) {
				case 4:
					// /dm_gif/... paths
					filename = path.Join(filename, msg.MessageCreate.ID+"-"+path.Base(u.Path))
				case 5:
					// /dm/... paths
					filename = path.Join(filename, parts[2]+"-"+parts[4])
				}

				ig.ToItem(timeline.RelAttachment, &timeline.Item{
					Classification: timeline.ClassMessage,
					Timestamp:      msg.MessageCreate.CreatedAt,
					Owner: timeline.Entity{
						Attributes: []timeline.Attribute{
							{
								Name:     identityAttribute,
								Value:    msg.MessageCreate.SenderID,
								Identity: true,
							},
						},
					},
					Content: timeline.ItemData{
						Filename: parts[len(parts)-1],
						Data: func(_ context.Context) (io.ReadCloser, error) {
							return fsys.Open(filename)
						},
					},
				})
			}

			params.Pipeline <- ig
		}
	}

	return nil
}

func (c *Client) processTweetsFromArchive(ctx context.Context, params timeline.ImportParams, file io.Reader, fsys fs.FS, processFunc archiveProcessFn, opt Options) error {
	dec := json.NewDecoder(file)

	// read array opening bracket '['
	_, err := dec.Token()
	if err != nil {
		return fmt.Errorf("decoding opening token: %w", err)
	}

	for dec.More() {
		var container struct {
			Tweet tweet `json:"tweet"`
		}
		err := dec.Decode(&container)
		if err != nil {
			return fmt.Errorf("decoding tweet element: %w", err)
		}
		t := container.Tweet

		skip, err := c.prepareTweet(ctx, &t, "archive", opt)
		if err != nil {
			return fmt.Errorf("preparing tweet: %w", err)
		}
		if skip {
			continue
		}

		ig, err := processFunc(ctx, params, t, fsys, opt)
		if err != nil {
			return fmt.Errorf("processing tweet: %w", err)
		}

		// send the tweet(s) for processing
		if ig != nil {
			params.Pipeline <- ig
		}
	}

	return nil
}

// func (c *Client) processReplyRelationFromArchive(_ context.Context, t tweet, _ fs.FS, _ Options) (*timeline.Graph, error) {
// 	if t.InReplyToStatusIDStr == "" {
// 		// current tweet is not a reply, so no relationship to add
// 		return nil, nil
// 	}
// 	if t.InReplyToUserIDStr != "" && t.InReplyToUserIDStr != c.owner.AttributeValue(identityAttribute) {
// 		// from archives, we only support storing replies to self... (TODO:)
// 		return nil, nil
// 	}

// 	// TODO: with the new Relationship struct, we'll have to get the actual items, not just their IDs...
// 	// ig := &timeline.ItemGraph{
// 	// 	Relationships: []timeline.Relationship{
// 	// 		{
// 	// 			FromItemID: t.TweetIDStr,
// 	// 			ToItemID:   t.InReplyToStatusIDStr,
// 	// 			Relation:   timeline.RelReplyTo,
// 	// 		},
// 	// 	},
// 	// }

// 	return nil, errors.New("TODO: not implemented")
// }

func (c *Client) loadOwnerAccountFromArchive(fsys fs.FS) (twitterAccount, error) {
	acc, err := c.getAccountInfoFromArchive(fsys)
	if err != nil {
		return twitterAccount{}, err
	}

	err = c.addPhoneNumbersFromArchive(fsys, &acc)
	if err != nil {
		return twitterAccount{}, err
	}

	err = c.addProfileFromArchive(fsys, &acc)
	if err != nil {
		return twitterAccount{}, err
	}

	return acc, nil
}

func (c *Client) getAccountInfoFromArchive(fsys fs.FS) (twitterAccount, error) {
	file, err := fsys.Open("data/account.js")
	if err != nil {
		return twitterAccount{}, err
	}
	defer file.Close()

	// consume non-JSON preface (JavaScript variable definition)
	err = stripPreface(file)
	if err != nil {
		return twitterAccount{}, fmt.Errorf("reading account file preface: %w", err)
	}

	var accFile twitterAccountFile
	err = json.NewDecoder(file).Decode(&accFile)
	if err != nil {
		return twitterAccount{}, fmt.Errorf("decoding account file: %w", err)
	}
	if len(accFile) == 0 {
		return twitterAccount{}, errors.New("account file was empty")
	}

	return accFile[0].Account, nil
}

func (c *Client) addPhoneNumbersFromArchive(fsys fs.FS, acc *twitterAccount) error {
	file, err := fsys.Open("data/phone-number.js")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	// consume non-JSON preface (JavaScript variable definition)
	err = stripPreface(file)
	if err != nil {
		return fmt.Errorf("reading preface: %w", err)
	}

	return json.NewDecoder(file).Decode(&acc.PhoneNumbers)
}

func (c *Client) addProfileFromArchive(fsys fs.FS, acc *twitterAccount) error {
	file, err := fsys.Open("data/profile.js")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	// consume non-JSON preface (JavaScript variable definition)
	err = stripPreface(file)
	if err != nil {
		return fmt.Errorf("reading preface: %w", err)
	}

	return json.NewDecoder(file).Decode(&acc.Profile)
}

// stripPrefaces reads from f until a '=' is encountered.
// (Each .js file starts with a variable definition at the beginning.)
func stripPreface(f io.Reader) error {
	b := make([]byte, 1)
	for {
		_, err := io.ReadFull(f, b)
		if err != nil {
			return err
		}
		if b[0] == '=' {
			return nil
		}
	}
}

// archiveProcessFn is a function that processes a
// tweet from a Twitter export archive and returns
// an ItemGraph created from t.
type archiveProcessFn func(ctx context.Context, params timeline.ImportParams, t tweet, fsys fs.FS, opt Options) (*timeline.Graph, error)

const (
	manifestFile   = "data/manifest.js"
	dmsFile        = "data/direct-messages.js"
	tweetsFile2022 = "data/tweet.js" // seen in an archive from 2022
	tweetsFile     = "data/tweets.js"
)
