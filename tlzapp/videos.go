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

package tlzapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"time"

	"go.uber.org/zap"
)

// Transcode transcodes the file at inputPath, or inputStream (but not both), to contentType, and writes it to output.
// inputStream can only be used on video files that can be streamed (MP4, for example, requires seeking and cannot be streamed in).
func (app *App) Transcode(ctx context.Context, inputPath string, inputStream io.Reader, contentType string, output io.Writer, blur bool) error {
	if inputPath != "" && inputStream != nil {
		return errors.New("cannot specify both an input path and an input stream")
	}

	if errFfmpegInstalled != nil {
		return fmt.Errorf("transcoding unavailable: %w", errFfmpegInstalled)
	}

	// some Pixel/Google motion photos have multiple video streams where stream index 1 (second stream) is higher resolution,
	// but only has 1 frame -- essentially a still/preview; ffmpeg's default selection behavior is to prefer the highest-res
	// stream, but that is incorrect in the case of Google Motion Pictures. So this function is used to select the best stream.
	videoStreamMapping, err := determineVideoStream(ctx, inputPath)
	if err != nil {
		app.log.Error("unable to determine best video stream; using ffmpeg default stream selection", zap.Error(err))
	}

	input := inputPath
	if inputStream != nil {
		input = "pipe:" // could also use "-" apparently; stdin is assumed in this position
	}

	args := []string{"-i", input}
	if videoStreamMapping != "" {
		args = append(args, "-map", videoStreamMapping)
		args = append(args, "-map", "0:a?") // include audio, if present
	}
	if blur {
		// majorly scale down the video - we don't need quality when obfuscating it, but we do need speed;
		// limit video to 10 seconds (it's blurry so probably just a demo anyway), and a fast box blur
		// (2:1 means radius of 2 pixels, 1 iteration; lower numbers are faster) (could also add a filter
		// like "setpts=2.0*PTS" to slow it down by half if we want to cut no. of frames to process but keep duration)
		args = append(args,
			"-ss", "0",
			"-t", "10",
			"-vf", "scale=w=150:h=200:force_original_aspect_ratio=decrease, boxblur=5:2",
		)
	}

	switch contentType {
	case "video/webm":
		// The webm encoder is NOT fast on Apple Silicon as of Q1 2024
		// but it seems that matroska goes quite fast using hevc...
		// apparently webm is a subset of matroska anyway!?
		if runtime.GOOS == "darwin" {
			args = append(args,
				"-c:v", "hevc_videotoolbox",
				"-b:v", "512k",
				"-maxrate", "2000k",
				"-bufsize", "4000k",
				"-f", "matroska",
			)
		} else {
			// using libvpx and libvorbis greatly accelerates transcoding and reduces output size
			args = append(args,
				"-vcodec", "libvpx",
				"-acodec", "libvorbis",
				"-f", "webm",
			)
		}
	default:
		return fmt.Errorf("unsupported transcode media format (content-type): %s", contentType)
	}

	// finally, finish with the output ("-" is apparently equivalent to "pipe:" -- stdout is assumed in this position)
	args = append(args, "-")

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if inputStream != nil {
		cmd.Stdin = inputStream
	}
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	app.log.Debug("exec " + cmd.String())

	if err := cmd.Start(); err != nil {
		return err
	}

	app.log.Debug("ffmpeg command started", zap.Int("pid", cmd.Process.Pid))

	n, err := io.Copy(output, stdout)
	if err != nil {
		return fmt.Errorf("copy error: %w", err)
	}

	app.log.Debug("finished streaming transcoded video", zap.Int64("bytes", n))

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("waiting: %w", err)
	}

	app.log.Debug("ffmpeg command completed", zap.Int("pid", cmd.Process.Pid))

	return nil
}

// determineVideoStream returns the value to use for the "-map" option of ffmpeg.
// Note that an empty string and nil error may be returned if no specific stream
// mapping/selection could be determined.
func determineVideoStream(ctx context.Context, inputPath string) (string, error) {
	if inputPath == "" {
		return "", nil
	}

	probe := exec.CommandContext(ctx, "ffprobe",
		"-output_format", "json",
		"-show_streams",
		"-i", inputPath,
	)
	stdout, err := probe.StdoutPipe()
	if err != nil {
		return "", err
	}

	if err := probe.Start(); err != nil {
		return "", err
	}

	var result ffprobeJSONOutput
	if err := json.NewDecoder(stdout).Decode(&result); err != nil {
		return "", fmt.Errorf("%s: failed to probe video input for transcoding: %w", inputPath, err)
	}

	if err := probe.Wait(); err != nil {
		return "", err
	}

	// remove streams that aren't video
	for i := 0; i < len(result.Streams); i++ {
		if result.Streams[i].CodecType != "video" || result.Streams[i].NbFrames == "1" {
			result.Streams = append(result.Streams[:i], result.Streams[i+1:]...)
			i--
		}
	}

	// prefer stream with most number of frames
	// (if this turns out to be naive/faulty, another option could be highest bitrate)
	sort.SliceStable(result.Streams, func(i, j int) bool {
		iNumber, _ := strconv.Atoi(result.Streams[i].NbFrames)
		jNumber, _ := strconv.Atoi(result.Streams[j].NbFrames)
		return iNumber > jNumber
	})

	return fmt.Sprintf("0:%d", result.Streams[0].Index), nil
}

// ffprobeJSONOutput is the structure of the ffprobe command when outputting JSON
// showing stream information (-show_streams), as of March 2024.
type ffprobeJSONOutput struct {
	Streams []struct {
		Index              int    `json:"index"`
		CodecName          string `json:"codec_name,omitempty"`
		CodecLongName      string `json:"codec_long_name,omitempty"`
		Profile            string `json:"profile,omitempty"`
		CodecType          string `json:"codec_type"`
		CodecTagString     string `json:"codec_tag_string"`
		CodecTag           string `json:"codec_tag"`
		Width              int    `json:"width,omitempty"`
		Height             int    `json:"height,omitempty"`
		CodedWidth         int    `json:"coded_width,omitempty"`
		CodedHeight        int    `json:"coded_height,omitempty"`
		ClosedCaptions     int    `json:"closed_captions,omitempty"`
		FilmGrain          int    `json:"film_grain,omitempty"`
		HasBFrames         int    `json:"has_b_frames,omitempty"`
		SampleAspectRatio  string `json:"sample_aspect_ratio,omitempty"`
		DisplayAspectRatio string `json:"display_aspect_ratio,omitempty"`
		PixFmt             string `json:"pix_fmt,omitempty"`
		Level              int    `json:"level,omitempty"`
		ColorRange         string `json:"color_range,omitempty"`
		ColorSpace         string `json:"color_space,omitempty"`
		ColorTransfer      string `json:"color_transfer,omitempty"`
		ColorPrimaries     string `json:"color_primaries,omitempty"`
		ChromaLocation     string `json:"chroma_location,omitempty"`
		Refs               int    `json:"refs,omitempty"`
		ID                 string `json:"id"`
		RFrameRate         string `json:"r_frame_rate"`
		AvgFrameRate       string `json:"avg_frame_rate"`
		TimeBase           string `json:"time_base"`
		StartPts           int    `json:"start_pts"`
		StartTime          string `json:"start_time"`
		DurationTS         int    `json:"duration_ts"`
		Duration           string `json:"duration"`
		BitRate            string `json:"bit_rate"`
		NbFrames           string `json:"nb_frames"` // number of frames
		ExtradataSize      int    `json:"extradata_size,omitempty"`
		Disposition        struct {
			Default         int `json:"default"`
			Dub             int `json:"dub"`
			Original        int `json:"original"`
			Comment         int `json:"comment"`
			Lyrics          int `json:"lyrics"`
			Karaoke         int `json:"karaoke"`
			Forced          int `json:"forced"`
			HearingImpaired int `json:"hearing_impaired"`
			VisualImpaired  int `json:"visual_impaired"`
			CleanEffects    int `json:"clean_effects"`
			AttachedPic     int `json:"attached_pic"`
			TimedThumbnails int `json:"timed_thumbnails"`
			NonDiegetic     int `json:"non_diegetic"`
			Captions        int `json:"captions"`
			Descriptions    int `json:"descriptions"`
			Metadata        int `json:"metadata"`
			Dependent       int `json:"dependent"`
			StillImage      int `json:"still_image"`
		} `json:"disposition"`
		Tags struct {
			CreationTime time.Time `json:"creation_time"`
			Language     string    `json:"language"`
			HandlerName  string    `json:"handler_name"`
			VendorID     string    `json:"vendor_id"`
		} `json:"tags,omitempty"`
	} `json:"streams"`
}

var errFfmpegInstalled = func() error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg not found in PATH: %w", err)
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return fmt.Errorf("ffprobe not found in PATH: %w", err)
	}
	return nil
}()
