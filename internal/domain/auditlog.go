package domain

import "time"

// AuditEventType names the actions the audit log records — deliberately
// a small, fixed set (not a free-form string any caller can invent) so
// the Settings UI's log viewer can filter/label them meaningfully rather
// than displaying whatever ad-hoc string a call site happened to pass.
type AuditEventType string

const (
	AuditEventUnlock      AuditEventType = "unlock"
	AuditEventRestore     AuditEventType = "restore"
	AuditEventEmailDelete AuditEventType = "email_delete"
	AuditEventRuleCreate  AuditEventType = "rule_create"
	AuditEventRuleUpdate  AuditEventType = "rule_update"
	AuditEventRuleDelete  AuditEventType = "rule_delete"
)

// AuditLogEntry is one row of the audit_log table (FR-... P1 "Аудит-лог").
// IP is empty for CLI-triggered events, which have no request to read one
// from.
type AuditLogEntry struct {
	ID        int64
	CreatedAt time.Time
	EventType AuditEventType
	IP        string
	Summary   string
}
