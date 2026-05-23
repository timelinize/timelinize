package testhelpers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/timelinize/timelinize/timeline"
)

func ValidateItemData(t *testing.T, expectedFilename string, expectedData any, itemData timeline.ItemData, errorMessage string, errorArgs ...any) {
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
