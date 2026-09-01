package corvid

import "testing"

// TestErrorCodeTable pins the frozen error-code table (FFI.md §1.3: values
// are never renumbered) — the docs/SURFACE.tsv mapping for the engine's
// corvid::Error rows. The fixtures prove the codes the suite can trigger
// (10/11/12/14/15/17); the redb-internal fault variants have no public
// trigger (the engine's own radar exempts them), so the table itself is the
// proof that every variant maps to its documented code. Code 19 (ErrBusy)
// is FFI-only: compact exclusivity, with no engine Error variant.
func TestErrorCodeTable(t *testing.T) {
	frozen := map[ErrCode]struct{ name string }{
		ErrNone:               {"ErrNone"},
		ErrDatabase:           {"ErrDatabase"},
		ErrTransaction:        {"ErrTransaction"},
		ErrTable:              {"ErrTable"},
		ErrStorage:            {"ErrStorage"},
		ErrCommit:             {"ErrCommit"},
		ErrSetDurability:      {"ErrSetDurability"},
		ErrCompaction:         {"ErrCompaction"},
		ErrDecode:             {"ErrDecode"},
		ErrCorruptIndex:       {"ErrCorruptIndex"},
		ErrReservedCollection: {"ErrReservedCollection"},
		ErrInvalidName:        {"ErrInvalidName"},
		ErrArgument:           {"ErrArgument"},
		ErrIncompatibleFormat: {"ErrIncompatibleFormat"},
		ErrEmptyIndexTraining: {"ErrEmptyIndexTraining"},
		ErrSchemaViolation:    {"ErrSchemaViolation"},
		ErrInvalidDump:        {"ErrInvalidDump"},
		ErrBackupTargetExists: {"ErrBackupTargetExists"},
		ErrIO:                 {"ErrIO"},
		ErrBusy:               {"ErrBusy"},
	}
	want := map[ErrCode]int{
		ErrNone: 0, ErrDatabase: 1, ErrTransaction: 2, ErrTable: 3,
		ErrStorage: 4, ErrCommit: 5, ErrSetDurability: 6, ErrCompaction: 7,
		ErrDecode: 8, ErrCorruptIndex: 9, ErrReservedCollection: 10,
		ErrInvalidName: 11, ErrArgument: 12, ErrIncompatibleFormat: 13,
		ErrEmptyIndexTraining: 14, ErrSchemaViolation: 15, ErrInvalidDump: 16,
		ErrBackupTargetExists: 17, ErrIO: 18, ErrBusy: 19,
	}
	for code, fw := range frozen {
		if int(code) != want[code] {
			t.Errorf("%s = %d, want %d (frozen table drifted)", fw.name, int(code), want[code])
		}
	}
	if len(frozen) != 20 {
		t.Errorf("table has %d entries, want 20 (0..19)", len(frozen))
	}
}
