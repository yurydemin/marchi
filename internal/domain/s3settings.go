package domain

import "time"

// S3Settings is the singleton S3 connection configuration (FR-S3-02),
// backed by the s3_config table's single row. Unlike per-account IMAP
// credentials, this is one connection shared by the whole instance —
// see internal/config's S3Config doc comment for why it lives here
// (Master-Key-encrypted, API-editable) rather than in config.yaml.
type S3Settings struct {
	Enabled            bool
	Endpoint           string
	Region             string
	Bucket             string
	AccessKeyEncrypted []byte
	SecretKeyEncrypted []byte
	PathStyle          bool
	StorageClass       string
	TLSSkipVerify      bool
	UpdatedAt          time.Time
}
