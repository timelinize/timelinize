package timeline

import "testing"

func TestCouldBeMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "Empty string",
			input:    "",
			expected: false,
		},
		{
			name:     "Plain text",
			input:    "Hello, world!",
			expected: false,
		},
		{
			name:     "Markdown headers",
			input:    "# Header 1\n## Header 2",
			expected: true,
		},
		{
			name:     "Markdown list",
			input:    "- Item 1\n- Item 2\n- Item 3",
			expected: true,
		},
		{
			name:     "Markdown link",
			input:    "[Link text](https://example.com)",
			expected: true,
		},
		{
			name:     "Markdown code block",
			input:    "```\ncode block\n```",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := couldBeMarkdown([]byte(tt.input))
			if result != tt.expected {
				t.Errorf("couldBeMarkdown(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}
