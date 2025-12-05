package config

import "testing"

func TestParseSize(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		// IEC units (base 1024)
		{"100", 100, false},
		{"100B", 100, false},
		{"1KiB", 1024, false},
		{"1MiB", 1024 * 1024, false},
		{"1GiB", 1024 * 1024 * 1024, false},
		{"200GiB", 200 * 1024 * 1024 * 1024, false},
		{"1TiB", 1024 * 1024 * 1024 * 1024, false},

		// SI units (base 1000)
		{"1KB", 1000, false},
		{"1MB", 1000 * 1000, false},
		{"1GB", 1000 * 1000 * 1000, false},
		{"200GB", 200 * 1000 * 1000 * 1000, false},
		{"1TB", 1000 * 1000 * 1000 * 1000, false},

		// Shorthand (treated as IEC)
		{"1K", 1024, false},
		{"1M", 1024 * 1024, false},
		{"1G", 1024 * 1024 * 1024, false},
		{"1T", 1024 * 1024 * 1024 * 1024, false},

		// With spaces
		{"100 GiB", 100 * 1024 * 1024 * 1024, false},
		{" 50 GB ", 50 * 1000 * 1000 * 1000, false},

		// Decimals
		{"1.5GiB", int64(1.5 * 1024 * 1024 * 1024), false},
		{"2.5GB", int64(2.5 * 1000 * 1000 * 1000), false},

		// Case insensitive
		{"100gib", 100 * 1024 * 1024 * 1024, false},
		{"100gb", 100 * 1000 * 1000 * 1000, false},

		// Errors
		{"", 0, true},
		{"abc", 0, true},
		{"100XB", 0, true},
		{"-100GB", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseSize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSize(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.expected {
				t.Errorf("ParseSize(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseSizeSpec(t *testing.T) {
	tests := []struct {
		input       string
		wantBytes   int64
		wantPercent float64
		wantErr     bool
	}{
		// Absolute sizes
		{"100GiB", 100 * 1024 * 1024 * 1024, 0, false},
		{"200GB", 200 * 1000 * 1000 * 1000, 0, false},
		{"500MB", 500 * 1000 * 1000, 0, false},

		// Percentages
		{"80%", 0, 80, false},
		{"50%", 0, 50, false},
		{"100%", 0, 100, false},
		{" 75 %", 0, 75, false},
		{"33.5%", 0, 33.5, false},

		// Errors
		{"", 0, 0, true},
		{"0%", 0, 0, true},   // 0% is invalid
		{"101%", 0, 0, true}, // >100% is invalid
		{"-50%", 0, 0, true}, // negative is invalid
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseSizeSpec(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSizeSpec(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if tt.wantPercent > 0 {
				if !got.IsPercent() {
					t.Errorf("ParseSizeSpec(%q) expected percentage, got absolute", tt.input)
				}
				if got.Percent != tt.wantPercent {
					t.Errorf("ParseSizeSpec(%q) percent = %f, want %f", tt.input, got.Percent, tt.wantPercent)
				}
			} else {
				if got.IsPercent() {
					t.Errorf("ParseSizeSpec(%q) expected absolute, got percentage", tt.input)
				}
				if got.Bytes != tt.wantBytes {
					t.Errorf("ParseSizeSpec(%q) bytes = %d, want %d", tt.input, got.Bytes, tt.wantBytes)
				}
			}
		})
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
		{200 * 1024 * 1024 * 1024, "200.0 GiB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TiB"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := FormatSize(tt.input)
			if got != tt.expected {
				t.Errorf("FormatSize(%d) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
