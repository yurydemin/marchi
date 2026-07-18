package sync

// Progress is a snapshot of one folder's in-progress fetch, reported via
// ProgressFunc for a caller that wants live updates (FR-SE-07: "текущий
// UID, всего писем, скорость, ошибки") — e.g. the HTTP layer's WebSocket
// hub. Reported after every message is archived or fails, not on a
// timer; internal/sync has no notion of "the client is slow" or "haven't
// sent an update in 2 seconds" — throttling how often this actually goes
// out over a network connection is the caller's job, not this package's.
type Progress struct {
	AccountID  int64
	FolderName string
	CurrentUID uint32
	Total      int // estimated messages to process in this folder (best-effort, from UIDNEXT at SELECT time — see FetchNewMessages)
	Processed  int
	Archived   int
	Errors     int
}

// ProgressFunc receives Progress updates. A nil ProgressFunc means no
// caller wants updates.
type ProgressFunc func(Progress)

func (f ProgressFunc) report(p Progress) {
	if f != nil {
		f(p)
	}
}
