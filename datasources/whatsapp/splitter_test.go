package whatsapp

import (
	"bufio"
	"bytes"
	"testing"
)

func TestParseMessageHeader(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantOK   bool
		wantDate string
		wantTime string
		wantName string
		wantLen  int
		wantLRO  bool
	}{
		{
			name:     "bracket YYYY/MM/DD HH:MM",
			line:     "[2024/12/31, 12:34] Alice: hello",
			wantOK:   true,
			wantDate: "2024/12/31",
			wantTime: "12:34",
			wantName: "Alice",
			wantLen:  len("[2024/12/31, 12:34] Alice: "),
		},
		{
			name:     "bracket DD/MM/YYYY HH:MM:SS",
			line:     "[31/12/2024, 12:34:56] Bob: hey",
			wantOK:   true,
			wantDate: "31/12/2024",
			wantTime: "12:34:56",
			wantName: "Bob",
			wantLen:  len("[31/12/2024, 12:34:56] Bob: "),
		},
		{
			name:     "dash YYYY-MM-DD HH:MM",
			line:     "2024-12-31, 12:34 - Carol: hi",
			wantOK:   true,
			wantDate: "2024-12-31",
			wantTime: "12:34",
			wantName: "Carol",
			wantLen:  len("2024-12-31, 12:34 - Carol: "),
		},
		{
			name:     "dash DD.MM.YYYY HH:MM:SS",
			line:     "31.12.2024, 12:34:56 - Dave: yo",
			wantOK:   true,
			wantDate: "31.12.2024",
			wantTime: "12:34:56",
			wantName: "Dave",
			wantLen:  len("31.12.2024, 12:34:56 - Dave: "),
		},
		{
			name:     "LRO bracket",
			line:     "\u200E[2024/12/31, 12:34] Eve: hi",
			wantOK:   true,
			wantDate: "2024/12/31",
			wantTime: "12:34",
			wantName: "Eve",
			wantLen:  len("\u200E") + len("[2024/12/31, 12:34] Eve: "),
			wantLRO:  true,
		},
		{
			name:     "LRO dash",
			line:     "\u200E2024-12-31, 12:34 - Frank: hi",
			wantOK:   true,
			wantDate: "2024-12-31",
			wantTime: "12:34",
			wantName: "Frank",
			wantLen:  len("\u200E") + len("2024-12-31, 12:34 - Frank: "),
			wantLRO:  true,
		},
		{
			name:   "invalid date format",
			line:   "[2024_12_31, 12:34] Bad: nope",
			wantOK: false,
		},
		{
			name:   "invalid date length",
			line:   "[2024/12/3, 12:34] Bad: nope",
			wantOK: false,
		},
		{
			name:   "invalid time format",
			line:   "[2024/12/31, 2:34] Bad: nope",
			wantOK: false,
		},
		{
			name:   "missing colon separator",
			line:   "[2024/12/31, 12:34] Alice nope",
			wantOK: false,
		},
		{
			name:   "no name",
			line:   "[2024/12/31, 12:34] : msg",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, ok := parseMessageHeader([]byte(tt.line))
			if ok != tt.wantOK {
				t.Fatalf("ok mismatch: got %v want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if h.Date != tt.wantDate || h.Time != tt.wantTime || h.Name != tt.wantName {
				t.Fatalf("header mismatch: got (%s,%s,%s) want (%s,%s,%s)", h.Date, h.Time, h.Name, tt.wantDate, tt.wantTime, tt.wantName)
			}
			if h.HeaderLen != tt.wantLen {
				t.Fatalf("header length mismatch: got %d want %d", h.HeaderLen, tt.wantLen)
			}
			if h.HasLRO != tt.wantLRO {
				t.Fatalf("HasLRO mismatch: got %v want %v", h.HasLRO, tt.wantLRO)
			}
		})
	}
}

func TestChatSplitTwoMessages(t *testing.T) {
	tokens := scanTokens(t, "[2024/12/31, 12:34] Alice: hello\n[2024/12/31, 12:35] Bob: hi there\n")
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}
	if tokens[0] != "[2024/12/31, 12:34] Alice: hello\n" {
		t.Fatalf("first token mismatch: got %q", tokens[0])
	}
	if tokens[1] != "[2024/12/31, 12:35] Bob: hi there\n" {
		t.Fatalf("second token mismatch: got %q", tokens[1])
	}
}

func TestChatSplitTwoMessagesDash(t *testing.T) {
	tokens := scanTokens(t, "2024-12-31, 12:34 - Alice: hello\n2024-12-31, 12:35 - Bob: hi there\n")
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}
	if tokens[0] != "2024-12-31, 12:34 - Alice: hello\n" {
		t.Fatalf("first token mismatch: got %q", tokens[0])
	}
	if tokens[1] != "2024-12-31, 12:35 - Bob: hi there\n" {
		t.Fatalf("second token mismatch: got %q", tokens[1])
	}
}

func TestChatSplitTwoMessagesLRO(t *testing.T) {
	tokens := scanTokens(t, "\u200E[2024/12/31, 12:34] Alice: hello\n\u200E[2024/12/31, 12:35] Bob: hi there\n")
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}
	if tokens[0] != "\u200E[2024/12/31, 12:34] Alice: hello\n" {
		t.Fatalf("first token mismatch: got %q", tokens[0])
	}
	if tokens[1] != "\u200E[2024/12/31, 12:35] Bob: hi there\n" {
		t.Fatalf("second token mismatch: got %q", tokens[1])
	}
}

func TestChatSplitMultilineMessage(t *testing.T) {
	tokens := scanTokens(t, "[2024/12/31, 12:34] Alice: hello\ncontinued line\n[2024/12/31, 12:35] Bob: hi\n")
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}
	if tokens[0] != "[2024/12/31, 12:34] Alice: hello\ncontinued line\n" {
		t.Fatalf("first token should contain multiline content, got %q", tokens[0])
	}
	if tokens[1] != "[2024/12/31, 12:35] Bob: hi\n" {
		t.Fatalf("second token mismatch: got %q", tokens[1])
	}
}

func TestChatSplitSingleMessage(t *testing.T) {
	tokens := scanTokens(t, "[2024/12/31, 12:34] Alice: hello\n")
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}
	if tokens[0] == "" {
		t.Fatalf("token should not be empty")
	}
}

func scanTokens(t *testing.T, input string) []string {
	t.Helper()

	scanner := bufio.NewScanner(bytes.NewReader([]byte(input)))
	scanner.Split(chatSplit)

	var tokens []string
	for scanner.Scan() {
		// scanner.Bytes() is reused; copy and keep exact bytes including newlines
		b := make([]byte, len(scanner.Bytes()))
		copy(b, scanner.Bytes())
		tokens = append(tokens, string(b))
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}
	return tokens
}
