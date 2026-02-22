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

package timeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// TODO: INTERACTIVE IMPORTS ARE STILL WIP.

func (p *processor) interactiveGraph(ctx context.Context, root *Graph, opts *InteractiveImport) error {
	p.assignGraphIDs(root)

	if err := p.saveInteractiveGraphFromRootNode(root); err != nil {
		return err
	}

	if err := p.downloadGraphDataFiles(ctx, root, opts); err != nil {
		return err
	}

	p.log.Info("graph ready", zap.String("graph_id", root.ProcessingID))

	opts.Graphs <- &InteractiveGraph{
		Graph:         root,
		DataFileReady: make(chan struct{}),
	}

	return errors.New("TODO: WIP")
}

func (p *processor) saveInteractiveGraphFromRootNode(rootNode *Graph) error {
	graphPath := p.tempGraphFolder()
	if err := os.MkdirAll(graphPath, 0700); err != nil {
		return err
	}
	file, err := os.Create(filepath.Join(graphPath, "root.graph"))
	if err != nil {
		return err
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(rootNode); err != nil {
		return err
	}
	return file.Sync()
}

func (p *processor) assignGraphIDs(g *Graph) {
	if g == nil {
		return
	}
	if g.ProcessingID == "" {
		g.ProcessingID = uuid.New().String()
	}
	for _, edge := range g.Edges {
		p.assignGraphIDs(edge.From)
		p.assignGraphIDs(edge.To)
	}
}

//nolint:unparam // FIXME: linter false positive, bug filed: https://github.com/mvdan/unparam/issues/88
func (p *processor) downloadGraphDataFiles(ctx context.Context, g *Graph, opts *InteractiveImport) error {
	if g == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if (g.Item != nil && g.Item.Content.Data != nil) ||
		(g.Entity != nil && g.Entity.NewPicture != nil) {
		// TODO: Do CoW (write to a .tmp or .dl file first, then rename when finished, so we know by observation if it is complete
		file, err := p.openInteractiveGraphDataFile(g)
		if err != nil {
			return fmt.Errorf("openin graph data file: %w", err)
		}
		// don't defer close, since we need to write to it in a goroutine

		var dataFilename string

		// open the reader for either the item data or the entity picture
		var dataReader io.ReadCloser
		if g.Item != nil && g.Item.Content.Data != nil {
			dataFilename = g.Item.Content.Filename
			dataReader, err = g.Item.Content.Data(ctx)
		} else if g.Entity != nil && g.Entity.NewPicture != nil {
			dataReader, err = g.Entity.NewPicture(ctx)
		}
		// TODO: download the item owner's profile picture too, if available (though I don't know of anywhere this happens yet)
		// if g.Item != nil && g.Item.Owner.NewPicture != nil {
		// }
		if err != nil {
			_ = file.Close()
			return fmt.Errorf("opening data reader from graph: %w", err)
		}
		// don't defer close of the data reader, since we need to read from it in a goroutine

		// read a few bytes off the top to detect the media type, if missing (for item files only)
		dataReader, err = p.fillMediaTypeOfInteractiveGraph(g, dataReader, dataFilename)
		if err != nil {
			_ = dataReader.Close()
			_ = file.Close()

			if rmErr := os.Remove(file.Name()); rmErr != nil {
				p.log.Info("could not clean up data file",
					zap.Error(rmErr),
					zap.String("filename", file.Name()))
			}

			if errors.Is(err, io.EOF) {
				p.log.Info("data file had no content", zap.String("filename", dataFilename))
			} else {
				return fmt.Errorf("trying to determine media type: %w", err)
			}
		} else {
			// only proceed to copy the file contents if we didn't already get an EOF (or any other error)
			go func() {
				defer file.Close()
				defer dataReader.Close()

				if _, err := io.Copy(file, dataReader); err != nil {
					p.log.Error("copying data to temporary file", zap.Error(err))
					return
				}

				if err := file.Sync(); err != nil {
					p.log.Error("syncing data file", zap.Error(err))
				}
			}()
		}
	}

	for _, edge := range g.Edges {
		if err := p.downloadGraphDataFiles(ctx, edge.From, opts); err != nil {
			return err
		}
		if err := p.downloadGraphDataFiles(ctx, edge.To, opts); err != nil {
			return err
		}
	}
	return nil
}

func (p *processor) fillMediaTypeOfInteractiveGraph(g *Graph, dataReader io.ReadCloser, dataFilename string) (io.ReadCloser, error) {
	// if there is no item data file, or it already has a content-type, no-op
	if g.Item == nil || g.Item.Content.MediaType != "" {
		return dataReader, nil
	}

	const peekSize = 1024
	buf := make([]byte, peekSize)

	n, err := io.ReadFull(dataReader, buf)

	// ignore ErrUnexpectedEOF, it just means the content is short;
	// return if there's EOF (caller must handle) -- means no content;
	// obviously return if there's any other error too
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return dataReader, err
	}

	// in case the content was short, use only written portion of buffer
	peekedBytes := buf[:n]

	g.Item.Content.MediaType = detectContentType(peekedBytes, dataFilename)

	// replace the reader with one that re-reads the buffered bytes before resuming reading the source stream
	dataReader = io.NopCloser(io.MultiReader(bytes.NewReader(peekedBytes), dataReader))

	return dataReader, nil
}

func (p *processor) openInteractiveGraphDataFile(g *Graph) (*os.File, error) {
	// We store interactive graph data files, temporarily while the user is
	// interacting with the graph, somewhat deep in the system temp folder.
	// It's in a system temp folder because import jobs are not typically
	// portable; especially starting on one system and continuing on another,
	// though I guess we could simply change the path to be something within
	// the timeline if desired. Still, this seems more proper at least for now.
	tmpFilePath := filepath.Join(p.tempGraphFolder(), g.ProcessingID+".graph.data")

	// ensure folder tree exists or we're gonna have a bad time
	if err := os.MkdirAll(filepath.Dir(tmpFilePath), 0700); err != nil {
		return nil, err
	}

	return os.Create(tmpFilePath)
}

func (p *processor) tempGraphFolder() string {
	return filepath.Join(appTempDir(), fmt.Sprintf("job-%d", p.ij.job.ID()))
}

func appTempDir() string {
	return filepath.Join(os.TempDir(), "timelinize")
}
