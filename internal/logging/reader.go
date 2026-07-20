package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dateLayout is the date format used both in log filenames and in the
// --date flag CLI callers pass to LogFilePath.
const dateLayout = "2006-01-02"

// LogFilePath resolves the daily log file for date (format "2006-01-02")
// under dir, or today's file if date is empty. It does not chase same-day
// overflow parts (marchi-{date}.2.log, ...) — those only exist once a
// single day's log passes 100MB (NFR-RL-04), an edge case this CLI viewer
// doesn't need to handle.
func LogFilePath(dir, date string) (string, error) {
	if date == "" {
		date = time.Now().Format(dateLayout)
	} else if _, err := time.Parse(dateLayout, date); err != nil {
		return "", fmt.Errorf("logging: invalid date %q, want YYYY-MM-DD: %w", date, err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.log", filePrefix, date))
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("logging: no log file for %s", date)
		}
		return "", fmt.Errorf("logging: stat %s: %w", path, err)
	}
	return path, nil
}

// TailLines returns up to the last n non-empty lines of dir's log file for
// date (today's, if date is empty).
func TailLines(dir, date string, n int) ([]string, error) {
	path, err := LogFilePath(dir, date)
	if err != nil {
		return nil, err
	}
	return tailFile(path, n)
}

// tailChunkSize is how much we read backward from the end of the file at a
// time — big enough that most tail requests resolve in one read, small
// enough not to pull a 100MB log file fully into memory just to show the
// last 50 lines.
const tailChunkSize = 64 * 1024

func tailFile(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("logging: opening %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("logging: stat %s: %w", path, err)
	}

	pos := info.Size()
	var buf []byte
	for pos > 0 && bytes.Count(buf, []byte("\n")) <= n {
		readSize := int64(tailChunkSize)
		if readSize > pos {
			readSize = pos
		}
		pos -= readSize

		chunk := make([]byte, readSize)
		if _, err := f.ReadAt(chunk, pos); err != nil {
			return nil, fmt.Errorf("logging: reading %s: %w", path, err)
		}
		buf = append(chunk, buf...)
	}

	trimmed := strings.TrimRight(string(buf), "\n")
	if trimmed == "" {
		return nil, nil
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// LogEntry is one parsed JSON log line.
type LogEntry struct {
	Timestamp string
	Level     string
	Message   string
	Fields    map[string]any // every field besides ts/level/msg
}

// ParseLine parses one JSON log line as produced by New's JSON encoder.
func ParseLine(line string) (LogEntry, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return LogEntry{}, fmt.Errorf("logging: parsing log line: %w", err)
	}
	e := LogEntry{Fields: make(map[string]any, len(raw))}
	for k, v := range raw {
		switch k {
		case "ts":
			e.Timestamp, _ = v.(string)
		case "level":
			e.Level, _ = v.(string)
		case "msg":
			e.Message, _ = v.(string)
		default:
			e.Fields[k] = v
		}
	}
	return e, nil
}

// levelSeverity orders zap's level names for --level filtering. Unknown
// levels sort as the most severe, so they're never accidentally hidden by
// a filter.
var levelSeverity = map[string]int{
	"debug": 0,
	"info":  1,
	"warn":  2,
	"error": 3,
}

// LevelSeverity returns level's numeric severity, or the highest known
// severity + 1 for an unrecognized level.
func LevelSeverity(level string) int {
	if s, ok := levelSeverity[strings.ToLower(level)]; ok {
		return s
	}
	return len(levelSeverity)
}
