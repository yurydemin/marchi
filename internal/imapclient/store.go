package imapclient

import (
	"fmt"

	"github.com/emersion/go-imap"
	uidplus "github.com/emersion/go-imap-uidplus"
	"github.com/emersion/go-imap/client"
)

// MarkSeen sets the \Seen flag on uid in the currently SELECTed mailbox
// (FR-RE-03's archive_and_mark_read). The mailbox must have been SELECTed
// read-write — see internal/sync.FetchNewMessages, which opens it that
// way specifically so this and DeleteMessage can issue STORE/EXPUNGE.
func MarkSeen(c *client.Client, uid uint32) error {
	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	item := imap.FormatFlagsOp(imap.AddFlags, true) // +FLAGS.SILENT: no untagged FETCH response needed
	if err := c.UidStore(seqset, item, []interface{}{imap.SeenFlag}, nil); err != nil {
		return fmt.Errorf("imapclient: marking UID %d seen: %w", uid, err)
	}
	return nil
}

// DeleteMessage flags uid \Deleted and expunges it from the currently
// SELECTed mailbox (FR-RE-03's archive_and_delete — the message has
// already been archived by the time this is called, so this only removes
// it from the source server).
//
// Expunging is scoped to just this UID via RFC 4315's UID EXPUNGE when
// the server advertises UIDPLUS. Servers that don't get a plain EXPUNGE
// instead, which — a known, deliberately accepted limitation, since
// IMAP4rev1 has no narrower primitive — also removes any other message a
// different client already flagged \Deleted in this mailbox.
func DeleteMessage(c *client.Client, uid uint32) error {
	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	item := imap.FormatFlagsOp(imap.AddFlags, true)
	if err := c.UidStore(seqset, item, []interface{}{imap.DeletedFlag}, nil); err != nil {
		return fmt.Errorf("imapclient: flagging UID %d deleted: %w", uid, err)
	}

	up := uidplus.NewClient(c)
	if ok, _ := up.SupportUidPlus(); ok {
		if err := up.UidExpunge(seqset, nil); err != nil {
			return fmt.Errorf("imapclient: UID EXPUNGE %d: %w", uid, err)
		}
		return nil
	}
	if err := c.Expunge(nil); err != nil {
		return fmt.Errorf("imapclient: EXPUNGE (no UIDPLUS support): %w", err)
	}
	return nil
}
