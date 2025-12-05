package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// SizeSpec represents either an absolute size in bytes or a percentage of available disk.
type SizeSpec struct {
	Bytes   int64   // Absolute size in bytes (used if Percent == 0)
	Percent float64 // Percentage of available disk (0-100, used if > 0)
}

// IsPercent returns true if this spec represents a percentage.
func (s SizeSpec) IsPercent() bool {
	return s.Percent > 0
}

// IsZero returns true if no size was specified.
func (s SizeSpec) IsZero() bool {
	return s.Bytes == 0 && s.Percent == 0
}

// ParseSizeSpec parses a size string that can be either:
// - Absolute: "200GiB", "200GB", "500MB", etc.
// - Percentage: "80%", "50%"
func ParseSizeSpec(s string) (SizeSpec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return SizeSpec{}, fmt.Errorf("empty size string")
	}

	// Check for percentage
	if strings.HasSuffix(s, "%") {
		numStr := strings.TrimSuffix(s, "%")
		numStr = strings.TrimSpace(numStr)
		pct, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return SizeSpec{}, fmt.Errorf("invalid percentage: %s", s)
		}
		if pct <= 0 || pct > 100 {
			return SizeSpec{}, fmt.Errorf("percentage must be between 0 and 100: %s", s)
		}
		return SizeSpec{Percent: pct}, nil
	}

	// Parse as absolute size
	bytes, err := ParseSize(s)
	if err != nil {
		return SizeSpec{}, err
	}
	return SizeSpec{Bytes: bytes}, nil
}

// ParseSize parses human-readable size strings like "200GiB", "200GB", "500MB", etc.
// Supports both SI (KB, MB, GB, TB) and IEC (KiB, MiB, GiB, TiB) units.
// Returns size in bytes.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size string")
	}

	// Match number (with optional decimal) and unit
	re := regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([A-Za-z]*)$`)
	matches := re.FindStringSubmatch(s)
	if matches == nil {
		return 0, fmt.Errorf("invalid size format: %s", s)
	}

	numStr := matches[1]
	unit := strings.ToUpper(matches[2])

	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number: %s", numStr)
	}

	var multiplier float64
	switch unit {
	case "", "B":
		multiplier = 1
	// IEC units (base 1024)
	case "KIB":
		multiplier = 1024
	case "MIB":
		multiplier = 1024 * 1024
	case "GIB":
		multiplier = 1024 * 1024 * 1024
	case "TIB":
		multiplier = 1024 * 1024 * 1024 * 1024
	// SI units (base 1000)
	case "KB":
		multiplier = 1000
	case "MB":
		multiplier = 1000 * 1000
	case "GB":
		multiplier = 1000 * 1000 * 1000
	case "TB":
		multiplier = 1000 * 1000 * 1000 * 1000
	// Common shorthand (treat as IEC)
	case "K":
		multiplier = 1024
	case "M":
		multiplier = 1024 * 1024
	case "G":
		multiplier = 1024 * 1024 * 1024
	case "T":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown unit: %s", unit)
	}

	return int64(num * multiplier), nil
}

// FormatSize formats bytes into human-readable string using IEC units.
func FormatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
