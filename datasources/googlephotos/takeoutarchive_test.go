package googlephotos

import (
	"testing"
)

func TestDetermineMediaFilenameInArchive(t *testing.T) {
	fimp := &FileImporter{
		truncatedNames: make(map[string]int),
	}

	for i, test := range []struct {
		inputJSONFilePath string
		inputMeta         mediaArchiveMetadata
		expect            string
	}{
		{
			inputJSONFilePath: "15250796_10158125619575157_1421325151866375198.json",
			inputMeta:         mediaArchiveMetadata{Title: "15250796_10158125619575157_1421325151866375198_o.jpg"},
			expect:            "15250796_10158125619575157_1421325151866375198_.jpg",
		},
		{
			inputJSONFilePath: "IMG_20161204_194948.jpg.supplemental-metadata.json",
			inputMeta:         mediaArchiveMetadata{Title: "IMG_20161204_194948.jpg"},
			expect:            "IMG_20161204_194948.jpg",
		},
		{
			inputJSONFilePath: "IMG_20160819_201122-01.jpeg.supplemental-metad.json",
			inputMeta:         mediaArchiveMetadata{Title: "IMG_20160819_201122-01.jpeg"},
			expect:            "IMG_20160819_201122-01.jpeg",
		},
		{
			inputJSONFilePath: "abcdefghijklmnopqrstu-vwxyzabcde-fghijklmnopqr.json",
			inputMeta:         mediaArchiveMetadata{Title: "abcdefghijklmnopqrstu-vwxyzabcde-fghijklmnopqrst-1.jpg"},
			expect:            "abcdefghijklmnopqrstu-vwxyzabcde-fghijklmnopqrs.jpg",
		},
		{
			inputJSONFilePath: "abcdefghijklmnopqrstu-vwxyzabcde-fghijklmnopqr(1).json",
			inputMeta:         mediaArchiveMetadata{Title: "abcdefghijklmnopqrstu-vwxyzabcde-fghijklmnopqrst-2.jpg"},
			expect:            "abcdefghijklmnopqrstu-vwxyzabcde-fghijklmnopqrs(1).jpg",
		},
		{
			inputJSONFilePath: "abcdefghijklmnopqrstu-vwxyzabcde-fghijklmnopqr(2).json",
			inputMeta:         mediaArchiveMetadata{Title: "abcdefghijklmnopqrstu-vwxyzabcde-fghijklmnopqrst-3.jpg"},
			expect:            "abcdefghijklmnopqrstu-vwxyzabcde-fghijklmnopqrs(2).jpg",
		},
	} {
		if actual := fimp.determineMediaFilenameInArchive(test.inputJSONFilePath, test.inputMeta); actual != test.expect {
			t.Errorf("Test %d (json_filename=%q title_from_meta=%q): Expected '%s' but got '%s'",
				i, test.inputJSONFilePath, test.inputMeta.Title, test.expect, actual)
		}
	}
}
