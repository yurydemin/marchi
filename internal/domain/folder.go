package domain

// Folder is an IMAP mailbox tracked for one account (FR-ST-03). UIDValidity
// and LastUID drive incremental sync (FR-SE-01): if the server's
// UIDVALIDITY no longer matches what's stored, every previously-recorded
// UID is meaningless and the folder needs a full resync (FR-SE-02).
type Folder struct {
	ID          int64
	AccountID   int64
	FolderName  string // UTF-7 decoded
	UIDValidity uint32
	LastUID     uint32
	SyncEnabled bool
	// MSGraphDeltaLink is ConnectorMSGraph's incremental-sync cursor for
	// this folder (Microsoft Graph's delta query — see
	// internal/sync.SyncAccountMSGraph). Empty means no successful sync
	// of this folder has completed yet. Meaningless for any other
	// connector type.
	MSGraphDeltaLink string
}
