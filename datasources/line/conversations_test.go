package line_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/timelinize/timelinize/timeline"
)

func importLineFixtures(t *testing.T, filenames []string) []*timeline.Conversation {
	t.Helper()

	ctx := context.Background()
	tl, err := timeline.Create(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("creating timeline: %v", err)
	}
	defer tl.Close()

	tl.SetObfuscationFunc(func() (timeline.ObfuscationOptions, bool) {
		return timeline.ObfuscationOptions{}, false
	})

	jobID, err := tl.CreateJob(&timeline.ImportJob{
		Plan: timeline.ImportPlan{
			Files: []timeline.FileImport{{
				DataSourceName: "line",
				Filenames:      filenames,
			}},
		},
		ProcessingOptions: timeline.ProcessingOptions{
			ItemUniqueConstraints: map[string]bool{
				"classification_name": true,
				"data":                true,
				"filename":            true,
				"latlon":              true,
				"timestamp":           true,
			},
		},
	}, time.Time{}, 0, 0, 0)
	if err != nil {
		t.Fatalf("creating import job: %v", err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for {
		jobs, err := tl.GetJobs(ctx, []uint64{jobID}, 0)
		if err != nil {
			t.Fatalf("loading job status: %v", err)
		}
		if len(jobs) != 1 {
			t.Fatalf("expected one job row, got %d", len(jobs))
		}
		switch jobs[0].State {
		case timeline.JobSucceeded:
			convos, err := tl.RecentConversations(ctx, timeline.ItemSearchParams{Limit: 30})
			if err != nil {
				t.Fatalf("loading recent conversations: %v", err)
			}
			return convos
		case timeline.JobFailed, timeline.JobAborted:
			if jobs[0].Message != nil {
				t.Fatalf("import job ended in %s: %s", jobs[0].State, *jobs[0].Message)
			}
			t.Fatalf("import job ended in %s", jobs[0].State)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for import job %d", jobID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func fixturePaths(t *testing.T, names []string) []string {
	t.Helper()
	paths := make([]string, len(names))
	for i, n := range names {
		p := filepath.Join("testdata", "fixtures", n)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("fixture %q not found: %v", p, err)
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			t.Fatalf("resolving fixture path %q: %v", p, err)
		}
		paths[i] = abs
	}
	return paths
}

func namesKey(entities []timeline.Entity) string {
	names := make([]string, 0, len(entities))
	for _, ent := range entities {
		if ent.Name != "" {
			names = append(names, ent.Name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, "|")
}

func TestRecentConversations_OneConversationPerLineExportFile(t *testing.T) {
	files := fixturePaths(t, []string{
		"Chat history with Alice.txt",
		"Chat history in GroupChat.txt",
		"[LINE] Chat in GroupChat.txt",
		"[LINE] Chat mit Solo.txt",
	})

	convos := importLineFixtures(t, files)

	for i, c := range convos {
		t.Logf("convo[%d]: entities=%q messages=%d", i, namesKey(c.Entities), len(c.RecentMessages))
	}

	if len(convos) != 4 {
		t.Fatalf("expected 4 conversations (1 per LINE export file), got %d", len(convos))
	}
}
