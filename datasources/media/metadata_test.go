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

package media

import "testing"

func TestSplitCamelCaseIntoWords(t *testing.T) {
	for input, expect := range map[string]string{
		"ImageWidth":                       "Image Width",
		"ImageLength":                      "Image Length",
		"BitsPerSample":                    "Bits Per Sample",
		"Compression":                      "Compression",
		"PhotometricInterpretation":        "Photometric Interpretation",
		"Orientation":                      "Orientation",
		"SamplesPerPixel":                  "Samples Per Pixel",
		"PlanarConfiguration":              "Planar Configuration",
		"YCbCrSubSampling":                 "Y Cb Cr Sub Sampling",
		"YCbCrPositioning":                 "Y Cb Cr Positioning",
		"XResolution":                      "X Resolution",
		"YResolution":                      "Y Resolution",
		"ResolutionUnit":                   "Resolution Unit",
		"DateTime":                         "Date Time",
		"ImageDescription":                 "Image Description",
		"Make":                             "Make",
		"Model":                            "Model",
		"Software":                         "Software",
		"Artist":                           "Artist",
		"Copyright":                        "Copyright",
		"ExifIFDPointer":                   "Exif IFD Pointer",
		"GPSInfoIFDPointer":                "GPS Info IFD Pointer",
		"InteroperabilityIFDPointer":       "Interoperability IFD Pointer",
		"ExifVersion":                      "Exif Version",
		"FlashpixVersion":                  "Flashpix Version",
		"ColorSpace":                       "Color Space",
		"ComponentsConfiguration":          "Components Configuration",
		"CompressedBitsPerPixel":           "Compressed Bits Per Pixel",
		"PixelXDimension":                  "Pixel X Dimension",
		"PixelYDimension":                  "Pixel Y Dimension",
		"MakerNote":                        "Maker Note",
		"UserComment":                      "User Comment",
		"RelatedSoundFile":                 "Related Sound File",
		"DateTimeOriginal":                 "Date Time Original",
		"DateTimeDigitized":                "Date Time Digitized",
		"SubSecTime":                       "Sub Sec Time",
		"SubSecTimeOriginal":               "Sub Sec Time Original",
		"SubSecTimeDigitized":              "Sub Sec Time Digitized",
		"ImageUniqueID":                    "Image Unique ID",
		"ExposureTime":                     "Exposure Time",
		"FNumber":                          "F Number",
		"ExposureProgram":                  "Exposure Program",
		"SpectralSensitivity":              "Spectral Sensitivity",
		"ISOSpeedRatings":                  "ISO Speed Ratings",
		"OECF":                             "OECF",
		"ShutterSpeedValue":                "Shutter Speed Value",
		"ApertureValue":                    "Aperture Value",
		"BrightnessValue":                  "Brightness Value",
		"ExposureBiasValue":                "Exposure Bias Value",
		"MaxApertureValue":                 "Max Aperture Value",
		"SubjectDistance":                  "Subject Distance",
		"MeteringMode":                     "Metering Mode",
		"LightSource":                      "Light Source",
		"Flash":                            "Flash",
		"FocalLength":                      "Focal Length",
		"SubjectArea":                      "Subject Area",
		"FlashEnergy":                      "Flash Energy",
		"SpatialFrequencyResponse":         "Spatial Frequency Response",
		"FocalPlaneXResolution":            "Focal Plane X Resolution",
		"FocalPlaneYResolution":            "Focal Plane Y Resolution",
		"FocalPlaneResolutionUnit":         "Focal Plane Resolution Unit",
		"SubjectLocation":                  "Subject Location",
		"ExposureIndex":                    "Exposure Index",
		"SensingMethod":                    "Sensing Method",
		"FileSource":                       "File Source",
		"SceneType":                        "Scene Type",
		"CFAPattern":                       "CFA Pattern",
		"CustomRendered":                   "Custom Rendered",
		"ExposureMode":                     "Exposure Mode",
		"WhiteBalance":                     "White Balance",
		"DigitalZoomRatio":                 "Digital Zoom Ratio",
		"FocalLengthIn35mmFilm":            "Focal Length In 35mm Film",
		"SceneCaptureType":                 "Scene Capture Type",
		"GainControl":                      "Gain Control",
		"Contrast":                         "Contrast",
		"Saturation":                       "Saturation",
		"Sharpness":                        "Sharpness",
		"DeviceSettingDescription":         "Device Setting Description",
		"SubjectDistanceRange":             "Subject Distance Range",
		"LensMake":                         "Lens Make",
		"LensModel":                        "Lens Model",
		"XPTitle":                          "XP Title",
		"XPComment":                        "XP Comment",
		"XPAuthor":                         "XP Author",
		"XPKeywords":                       "XP Keywords",
		"XPSubject":                        "XP Subject",
		"ThumbJPEGInterchangeFormat":       "Thumb JPEG Interchange Format",
		"ThumbJPEGInterchangeFormatLength": "Thumb JPEG Interchange Format Length",
		"GPSVersionID":                     "GPS Version ID",
		"GPSLatitudeRef":                   "GPS Latitude Ref",
		"GPSLatitude":                      "GPS Latitude",
		"GPSLongitudeRef":                  "GPS Longitude Ref",
		"GPSLongitude":                     "GPS Longitude",
		"GPSAltitudeRef":                   "GPS Altitude Ref",
		"GPSAltitude":                      "GPS Altitude",
		"GPSTimeStamp":                     "GPS Time Stamp",
		"GPSSatelites":                     "GPS Satelites",
		"GPSStatus":                        "GPS Status",
		"GPSMeasureMode":                   "GPS Measure Mode",
		"GPSDOP":                           "GPSDOP",
		"GPSSpeedRef":                      "GPS Speed Ref",
		"GPSSpeed":                         "GPS Speed",
		"GPSTrackRef":                      "GPS Track Ref",
		"GPSTrack":                         "GPS Track",
		"GPSImgDirectionRef":               "GPS Img Direction Ref",
		"GPSImgDirection":                  "GPS Img Direction",
		"GPSMapDatum":                      "GPS Map Datum",
		"GPSDestLatitudeRef":               "GPS Dest Latitude Ref",
		"GPSDestLatitude":                  "GPS Dest Latitude",
		"GPSDestLongitudeRef":              "GPS Dest Longitude Ref",
		"GPSDestLongitude":                 "GPS Dest Longitude",
		"GPSDestBearingRef":                "GPS Dest Bearing Ref",
		"GPSDestBearing":                   "GPS Dest Bearing",
		"GPSDestDistanceRef":               "GPS Dest Distance Ref",
		"GPSDestDistance":                  "GPS Dest Distance",
		"GPSProcessingMethod":              "GPS Processing Method",
		"GPSAreaInformation":               "GPS Area Information",
		"GPSDateStamp":                     "GPS Date Stamp",
		"GPSDifferential":                  "GPS Differential",
		"InteroperabilityIndex":            "Interoperability Index",
	} {
		actual := splitCamelCaseIntoWords(input)
		if actual != expect {
			t.Errorf("'%s': Expected '%s' but got '%s'", input, expect, actual)
		}
	}
}
