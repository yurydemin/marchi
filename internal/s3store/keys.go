// Package s3store implements the S3 mirror/disaster-recovery layer
// (FR-S3-01 through FR-S3-09): a client wrapping aws-sdk-go-v2 for
// MinIO-compatible endpoints, the FR-S3-04 object key layout, and (in
// later Phase 3 steps) client-side encryption and an upload queue worker
// pool.
package s3store

import (
	"fmt"
	"path"
	"time"
)

// EmailKey returns the FR-S3-04 object key for an archived email:
//
//	mailvault/v1/accounts/{account_id}/emails/{yyyy}/{mm}/{dd}/{sha256[:2]}/{sha256}.eml
//
// date is the email's own date (used for the yyyy/mm/dd partition), not
// the upload time. sha256Hex must be the lowercase hex SHA-256 of the
// raw .eml content — its first two characters fan out the partition to
// avoid a single directory-equivalent prefix holding every object.
func EmailKey(accountID int64, date time.Time, sha256Hex string) string {
	return path.Join(
		"mailvault", "v1",
		"accounts", fmt.Sprint(accountID),
		"emails",
		fmt.Sprintf("%04d", date.Year()),
		fmt.Sprintf("%02d", date.Month()),
		fmt.Sprintf("%02d", date.Day()),
		sha256Hex[:2],
		sha256Hex+".eml",
	)
}

// AttachmentKey returns the FR-S3-04 object key for an email's attachment:
//
//	mailvault/v1/accounts/{account_id}/attachments/{email_sha256}/{filename}
//
// emailSHA256Hex ties the attachment back to its parent email's content
// hash (not the attachment's own hash) — the layout groups an email's
// attachments under one prefix.
func AttachmentKey(accountID int64, emailSHA256Hex, filename string) string {
	return path.Join(
		"mailvault", "v1",
		"accounts", fmt.Sprint(accountID),
		"attachments",
		emailSHA256Hex,
		filename,
	)
}
