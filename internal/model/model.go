// Package model contains the durable domain objects shared by the relay.
package model

import "time"

// State describes an asset's durable lifecycle. An asset is never considered
// backed up merely because it reached the Android device.
type State string

const (
	StateDiscovered                 State = "DISCOVERED"
	StateQueued                     State = "QUEUED"
	StateTransferring               State = "TRANSFERRING"
	StateTransferred                State = "TRANSFERRED"
	StateAwaitingBackupConfirmation State = "AWAITING_BACKUP_CONFIRMATION"
	StateBackupConfirmed            State = "BACKUP_CONFIRMED"
	StateLocalRemoved               State = "LOCAL_REMOVED"
	StateFailed                     State = "FAILED"
	StateIgnored                    State = "IGNORED"
)

// Asset is the immutable metadata and mutable relay state for an Immich item.
type Asset struct {
	ID              string
	OriginalName    string
	MIMEType        string
	Checksum        string
	ByteSize        int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	State           State
	DestinationPath string
	LocalPath       string
	TransferSHA256  string
	Attempts        int
	NextRetryAt     *time.Time
	LastError       string
	BatchID         string
}

// Batch groups files delivered in one sync pass for explicit confirmation.
type Batch struct {
	ID          string
	CreatedAt   time.Time
	ConfirmedAt *time.Time
	CleanedAt   *time.Time
	Notes       string
}

// StorageInfo describes the usable Android filesystem capacity in bytes.
type StorageInfo struct {
	AvailableBytes int64
	TotalBytes     int64
}
