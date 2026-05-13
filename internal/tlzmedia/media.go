package tlzmedia

import (
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"

	"github.com/cshum/vipsgen/vips"
	"go.uber.org/zap"
)

// // TODO: Only called in 1 place
// // AssetImage returns the bytes of the image at the given asset path (relative
// // to the repo root). If obfuscate is true, then the image will be downsized and
// // blurred.
// func assetImage(_ context.Context, logger *zap.Logger, imagePath string, obfuscate bool) ([]byte, error) {
// 	size := 480
// 	if obfuscate {
// 		size = 120
// 	}
// 	imageBytes, err := loadAndEncodeImage(logger.Named("assets"), imagePath, nil, ".jpg", size, obfuscate)
// 	if err != nil {
// 		return nil, fmt.Errorf("opening source file %s: %w", imagePath, err)
// 	}
// 	return imageBytes, nil
// }

// LoadAndEncodeImage loads the image at inputFilePath or the bytes contained in inputBuf, (set only one or the other),
// and returns image bytes formatted according to the desired extension to choose the encoding. It resizes the image to
// be within maxDimension on both sides, and optionally obfuscates the image if enabled.
func LoadAndEncodeImage(logger *zap.Logger, inputFilePath string, inputBuf []byte, desiredExtension string, maxDimension int, obfuscate bool) ([]byte, error) {
	inputImage, err := LoadImageVips(logger, inputFilePath, inputBuf)
	if err != nil {
		return nil, fmt.Errorf("loading image: %w", err)
	}
	defer inputImage.Close()

	if err := scaleDownImage(inputImage, maxDimension); err != nil {
		return nil, fmt.Errorf("resizing image to within %d: %w", maxDimension, err)
	}

	if obfuscate {
		// how much blur is needed depends on the size of the image; I have found that the square root
		// of the max dimension, fine-tuned by a coefficient is pretty good: for thumbnails of 120px,
		// this ends up being about 8-10; for larger preview images of 1400px, we get more like 30, which
		// is helpful for obscuring sensitive features like faces (higher sigma = more blur)
		sigma := math.Sqrt(float64(maxDimension) * .9) //nolint:mnd
		if err := inputImage.Gaussblur(sigma, nil); err != nil {
			return nil, fmt.Errorf("applying guassian blur to image for obfuscation: %w", err)
		}
	}

	// apparently Windows does not support 10-bit color depth!?
	bitDepth := 10
	if runtime.GOOS == "windows" {
		bitDepth = 8
	}

	var imageBytes []byte
	switch strings.ToLower(desiredExtension) {
	case ".jpg", ".jpe", ".jpeg", ".jfif":
		imageBytes, err = inputImage.JpegsaveBuffer(&vips.JpegsaveBufferOptions{
			Keep:          vips.KeepNone, // this strips rotation, which is needed if autorotate was not used when loading the image
			Q:             50,
			Interlace:     true,
			SubsampleMode: vips.SubsampleAuto,
			TrellisQuant:  true,
			QuantTable:    3,
		})
	case ".png":
		imageBytes, err = inputImage.PngsaveBuffer(&vips.PngsaveBufferOptions{
			Keep:      vips.KeepNone, // this strips rotation, which is needed if autorotate was not used when loading the image
			Q:         50,
			Interlace: true,
		})
	case ".webp":
		imageBytes, err = inputImage.WebpsaveBuffer(&vips.WebpsaveBufferOptions{
			Keep: vips.KeepNone, // this strips rotation, which is needed if autorotate was not used when loading the image
			Q:    50,
		})
	case ".avif":
		imageBytes, err = inputImage.HeifsaveBuffer(&vips.HeifsaveBufferOptions{
			Keep:        vips.KeepNone, // this strips rotation info, which is needed if autorotate was not used when loading the image
			Compression: vips.HeifCompressionAv1,
			Q:           65,
			Bitdepth:    bitDepth,
			Effort:      1,
		})
	}
	if err != nil {
		// don't attempt a fallback if obfuscation is enabled, so we don't leak the unobfuscated image
		if obfuscate {
			return nil, fmt.Errorf("unable to encode obfuscated preview image: %w -- use thumbhash instead", err)
		}

		// I have seen "VipsJpeg: Corrupt JPEG data: N extraneous bytes before marker 0xdb" for some N,
		// even though my computer can show the image just fine. Not sure how to fix this, other than
		// configuring vips to continue on error (I think -- I know it fixed some errors)
		logger.Error("could not encode preview image, falling back to original image",
			zap.String("filename", inputFilePath),
			zap.String("ext", desiredExtension),
			zap.Error(err))

		// I guess just try returning the full image as-is and hope the browser can handle it - TODO: maybe try a std lib solution, even if slower
		if inputBuf != nil {
			return inputBuf, nil
		}
		return os.ReadFile(inputFilePath)
	}

	return imageBytes, nil
}

// LoadImageVips loads an image for vips to work with from either a file path or
// a buffer of bytes directly; precisely one must be non-nil.
func LoadImageVips(logger *zap.Logger, inputFilePath string, inputBytes []byte) (*vips.Image, error) {
	if inputFilePath != "" && inputBytes != nil {
		panic("load image with vips: input cannot be both a filename and a buffer")
	}
	if inputFilePath == "" && inputBytes == nil {
		panic("load image with vips: input must be either a filename or a buffer")
	}

	// load the image; right now I don't have any reason to set load parameters; Autorotate: true is
	// the only thing I think we need, but setting it there causes errors for loaders that don't
	// support it, like PNG images, so we do that separately after loading it, and the only other
	// thing would be FailOnError, but I have seen "VipsJpeg: Corrupt JPEG data: N extraneous bytes
	// before marker 0xdb" for some N, even though my computer can show the image just fine with
	// other tools, so I've found that just leaving it false works for the million images I've tested...
	var img *vips.Image
	var err error
	if inputFilePath != "" {
		img, err = vips.NewImageFromFile(inputFilePath, nil)
		if err != nil {
			return nil, fmt.Errorf("new image from file: %s: %w", inputFilePath, err)
		}
	} else if len(inputBytes) > 0 {
		img, err = vips.NewImageFromBuffer(inputBytes, nil)
		if err != nil {
			return nil, fmt.Errorf("new image from buffer, size %d: %w", len(inputBytes), err)
		}
	}
	if img == nil {
		return nil, errors.New("no image loaded")
	}

	// If rotation info is encoded into EXIF metadata, this can orient the image properly.
	// We do autorotate this way, rather than setting it in the LoadOptions parameter, because
	// that returns errors for loaders that don't support it (like pngload), even with
	// FailOnError set to false; but this apparently works anyway, I suppose since it bypasses
	// the loader.
	if err := img.Autorot(nil); err != nil {
		logger.Warn("autorotate failed",
			zap.String("input_file_path", inputFilePath),
			zap.Int("input_buffer_len", len(inputBytes)),
			zap.Error(err))
	}
	return img, nil
}

// scaleDownImage resizes the image to fit within the maxDimension
// if either side is larger than maxDimension; otherwise it does
// nothing to the image.
func scaleDownImage(inputImage *vips.Image, maxDimension int) error {
	width, height := inputImage.Width(), inputImage.Height()

	// if image is already within constraints, no-op
	if width < maxDimension && height < maxDimension {
		return nil
	}

	var scale float64
	if width > height {
		scale = float64(maxDimension) / float64(width)
	} else {
		scale = float64(maxDimension) / float64(height)
	}

	err := inputImage.Resize(scale, vips.DefaultResizeOptions()) // Nearest is fast, but Auto looks slightly better
	if err != nil {
		return fmt.Errorf("scaling image: %w", err)
	}

	// if AutoRotate was not set when loading the image, you could call AutoRotate
	// here to apply rotation info before EXIF metadata is stripped
	// if err := inputImage.AutoRotate(); err != nil {
	// 	return fmt.Errorf("rotating image: %v", err)
	// }

	return nil
}
