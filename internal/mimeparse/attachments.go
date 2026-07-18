package mimeparse

import (
	"bytes"
	"io"
	"strconv"
	"strings"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
)

// Attachment describes one non-body MIME part (FR-SE-05: names, types,
// sizes are indexed; content is not).
type Attachment struct {
	Filename  string
	MIMEType  string
	Size      int64
	ContentID string
}

// ParseAttachments walks raw's MIME tree and returns metadata for every
// part go-message classifies as an attachment rather than message body text
// (mail.Reader's own Content-Disposition/Content-Type based split: a part
// is body text if it's explicitly "inline", or isn't marked "attachment"
// and is text/*; everything else is an attachment).
//
// Content bytes are read (to measure decoded size) and discarded — FR-SE-05
// says index names/types/sizes, not content, and the original bytes stay
// exactly where they already are, inside the archived .eml. Nothing here
// gets separately stored.
func ParseAttachments(raw []byte) []Attachment {
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if r == nil {
		return nil
	}
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return nil
	}
	defer r.Close()

	var attachments []Attachment
	n := 0
	for {
		part, partErr := r.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil && !message.IsUnknownCharset(partErr) && !message.IsUnknownEncoding(partErr) {
			break // malformed beyond what's recoverable — stop, don't loop forever
		}

		ah, ok := part.Header.(*mail.AttachmentHeader)
		if !ok {
			continue // body text, not an attachment
		}
		n++

		filename, _ := ah.Filename()
		if filename == "" {
			filename = "attachment-" + strconv.Itoa(n)
		}
		mimeType, _, _ := ah.ContentType()
		size, _ := io.Copy(io.Discard, part.Body)
		contentID := strings.Trim(ah.Get("Content-Id"), "<>")

		attachments = append(attachments, Attachment{
			Filename:  filename,
			MIMEType:  mimeType,
			Size:      size,
			ContentID: contentID,
		})
	}
	return attachments
}

// ExtractAttachmentAt returns the decoded content of the attachment at
// position index (0-based, in the same document order ParseAttachments
// enumerates them). Since attachment content is never stored separately
// (see ParseAttachments' doc comment), this is how a download endpoint
// recovers an attachment's bytes: re-parse the parent .eml and read only
// the target part, discarding every other attachment's body without
// buffering it (same io.Copy-to-discard pattern ParseAttachments itself
// uses for parts it's only measuring).
func ExtractAttachmentAt(raw []byte, index int) (content []byte, ok bool) {
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if r == nil {
		return nil, false
	}
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return nil, false
	}
	defer r.Close()

	n := -1
	for {
		part, partErr := r.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil && !message.IsUnknownCharset(partErr) && !message.IsUnknownEncoding(partErr) {
			break
		}

		if _, ok := part.Header.(*mail.AttachmentHeader); !ok {
			continue
		}
		n++
		if n != index {
			_, _ = io.Copy(io.Discard, part.Body)
			continue
		}
		data, err := io.ReadAll(part.Body)
		if err != nil {
			return nil, false
		}
		return data, true
	}
	return nil, false
}
