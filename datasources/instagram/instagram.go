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

// Package instagram implements a data source for importing data from Instagram archive files.
package instagram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"time"

	"github.com/mholt/archiver/v4"
	"github.com/timelinize/timelinize/datasources/facebook"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "instagram",
		Title:           "Instagram",
		Icon:            "instagram.svg",
		NewFileImporter: func() timeline.FileImporter { return new(Client) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// Client implements the timeline.Client interface.
type Client struct{}

// Recognize returns whether the file or folder is recognized.
func (Client) Recognize(ctx context.Context, filenames []string) (timeline.Recognition, error) {
	for _, filename := range filenames {
		fsys, err := archiver.FileSystem(ctx, filename)
		if err != nil {
			return timeline.Recognition{}, err
		}
		_, err = fs.Stat(fsys, personalInformationPath)
		if err != nil {
			return timeline.Recognition{}, nil
		}
	}
	return timeline.Recognition{Confidence: 1}, nil
}

// FileImport imports data from the file or folder.
func (c *Client) FileImport(ctx context.Context, filenames []string, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {
	for _, filename := range filenames {
		fsys, err := archiver.FileSystem(ctx, filename)
		if err != nil {
			return err
		}

		// first, load the profile information
		pi, err := c.getPersonalInfo(fsys)
		if err != nil {
			return fmt.Errorf("loading profile: %w", err)
		}
		if len(pi.ProfileUser) == 0 {
			return errors.New("no profile information found: missing profile user")
		}
		personalInfo := pi.ProfileUser[0].StringMapData

		owner := timeline.Entity{
			Name: personalInfo.Name.Value,
			Attributes: []timeline.Attribute{
				{
					Name:     "instagram_username",
					Value:    personalInfo.Username.Value,
					Identity: true,
				},
				{
					Name:  timeline.AttributeGender,
					Value: personalInfo.Gender.Value,
				},
				{
					Name:        timeline.AttributePhoneNumber,
					Value:       personalInfo.PhoneNumber.Value,
					Identifying: true,
				},
				{
					Name:        timeline.AttributeEmail,
					Value:       personalInfo.Email.Value,
					Identifying: true,
				},
				{
					Name:  "instagram_bio",
					Value: personalInfo.Bio.Value,
				},
				{
					Name:  "website",
					Value: personalInfo.Website.Value,
				},
			},
		}
		if picFilename := pi.ProfileUser[0].MediaMapData.ProfilePhoto.URI; picFilename != "" {
			owner.NewPicture = func(_ context.Context) (io.ReadCloser, error) {
				return fsys.Open(picFilename)
			}
		}
		if personalInfo.DateOfBirth.Value != "" {
			bd, err := time.Parse("2006-01-02", personalInfo.DateOfBirth.Value)
			if err == nil {
				owner.Attributes = append(owner.Attributes, timeline.Attribute{
					Name:  "birth_date",
					Value: bd,
				})
			}
		}

		// then, load the posts index
		postIdx, err := c.getPostsIndex(fsys)
		if err != nil {
			return fmt.Errorf("loading index: %w", err)
		}
		for _, post := range postIdx {
			// a post may have multiple media items, we'll treat them as attachments

			var ig *timeline.Graph
			var firstMedia int

			// if there is text, use that as the "main" item
			if postText := post.allText(); postText != "" {
				ig = &timeline.Graph{
					Item: &timeline.Item{
						Classification: timeline.ClassSocial,
						Timestamp:      post.timestamp(),
						Owner:          owner,
						Content: timeline.ItemData{
							Data: timeline.StringData(postText),
						},
						IntermediateLocation: post.filename,
					},
				}
			} else if len(post.Media) > 0 {
				item := post.Media[0].timelineItem(fsys, owner)
				ig = &timeline.Graph{Item: item}
				firstMedia = 1 // the 0th media was used as the root of the graph
			}

			// add remaining media to graph
			for i := firstMedia; i < len(post.Media); i++ {
				ig.ToItem(timeline.RelAttachment, post.Media[i].timelineItem(fsys, owner))
			}

			itemChan <- ig
		}

		// stories
		// TODO: Maybe stories should go into a collection
		storyIdx, err := c.getStoryIndex(fsys)
		if err != nil {
			return err
		}
		for _, story := range storyIdx.IgStories {
			itemChan <- &timeline.Graph{
				Item: &timeline.Item{
					Timestamp:            time.Unix(story.CreationTimestamp, 0),
					Owner:                owner,
					IntermediateLocation: story.URI,
					Content: timeline.ItemData{
						Filename: path.Base(story.URI),
						Data: func(_ context.Context) (io.ReadCloser, error) {
							return fsys.Open(story.URI)
						},
					},
					Metadata: timeline.Metadata{
						"Caption": facebook.FixString(story.Title),
					},
				},
			}
		}

		// messages
		err = facebook.GetMessages(fsys, itemChan, "instagram", opt.Log)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *Client) getPersonalInfo(fsys fs.FS) (instaPersonalInformation, error) {
	var pi instaPersonalInformation

	file, err := fsys.Open(personalInformationPath)
	if err != nil {
		return pi, err
	}
	defer file.Close()

	err = json.NewDecoder(file).Decode(&pi)
	if err != nil {
		return pi, fmt.Errorf("decoding personal information file: %w", err)
	}
	return pi, nil
}

func (c *Client) getPostsIndex(fsys fs.FS) (instaPostsIndex, error) {
	var all instaPostsIndex

	for i := 1; i < 10000; i++ {
		postsFilename := fmt.Sprintf("%s%d.json", instaPostsIndexPrefix, i)

		file, err := fsys.Open(postsFilename)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return nil, err
		}

		var idx instaPostsIndex
		err = json.NewDecoder(file).Decode(&idx)
		file.Close()
		if err != nil {
			return nil, fmt.Errorf("decoding posts index file %s: %w", postsFilename, err)
		}

		for i := range len(idx) {
			idx[i].filename = postsFilename
		}

		all = append(all, idx...)
	}

	return all, nil
}

func (c *Client) getStoryIndex(fsys fs.FS) (instaStories, error) {
	file, err := fsys.Open(instaStoryIndex)
	if err != nil {
		return instaStories{}, err
	}
	defer file.Close()

	var idx instaStories
	err = json.NewDecoder(file).Decode(&idx)
	if err != nil {
		return idx, fmt.Errorf("decoding stories index file: %w", err)
	}

	return idx, nil
}

const (
	personalInformationPath = "personal_information/personal_information.json"
	instaPostsIndexPrefix   = "content/posts_"
	instaStoryIndex         = "content/stories.json"
)
