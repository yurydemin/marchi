package logging

import "testing"

func TestParseLine_ValidLine(t *testing.T) {
	line := `{"level":"info","ts":"2026-07-17T22:33:38.123+0000","msg":"command started","command":"marchi sync"}`
	e, err := ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if e.Level != "info" {
		t.Errorf("Level = %q", e.Level)
	}
	if e.Timestamp != "2026-07-17T22:33:38.123+0000" {
		t.Errorf("Timestamp = %q", e.Timestamp)
	}
	if e.Message != "command started" {
		t.Errorf("Message = %q", e.Message)
	}
	if e.Fields["command"] != "marchi sync" {
		t.Errorf("Fields[command] = %v", e.Fields["command"])
	}
	if _, ok := e.Fields["ts"]; ok {
		t.Error("ts should be extracted into Timestamp, not left in Fields")
	}
}

func TestParseLine_MalformedJSON(t *testing.T) {
	_, err := ParseLine("this is not json")
	if err == nil {
		t.Fatal("expected an error for malformed JSON, got nil")
	}
}

func TestParseLine_NoExtraFields(t *testing.T) {
	e, err := ParseLine(`{"level":"warn","ts":"2026-07-17T00:00:00Z","msg":"hello"}`)
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if len(e.Fields) != 0 {
		t.Errorf("Fields = %v, want empty", e.Fields)
	}
}

func TestLevelSeverity_Ordering(t *testing.T) {
	if LevelSeverity("debug") >= LevelSeverity("info") {
		t.Error("debug should be less severe than info")
	}
	if LevelSeverity("info") >= LevelSeverity("warn") {
		t.Error("info should be less severe than warn")
	}
	if LevelSeverity("warn") >= LevelSeverity("error") {
		t.Error("warn should be less severe than error")
	}
}

func TestLevelSeverity_CaseInsensitive(t *testing.T) {
	if LevelSeverity("WARN") != LevelSeverity("warn") {
		t.Error("LevelSeverity should be case-insensitive")
	}
}

func TestLevelSeverity_UnknownSortsHighest(t *testing.T) {
	if LevelSeverity("totally-unknown") <= LevelSeverity("error") {
		t.Error("an unrecognized level should sort as more severe than error, so filters never hide it")
	}
}
