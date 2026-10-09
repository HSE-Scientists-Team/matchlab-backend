package domain

import "time"

type FileStatus string

const (
	FileStatusPending  FileStatus = "pending"
	FileStatusReady    FileStatus = "ready"
	FileStatusDeleting FileStatus = "deleting"
	FileStatusDeleted  FileStatus = "deleted"
	FileStatusFailed   FileStatus = "failed"
)

// File соответствует существующей таблице media.file.
type File struct {
	ID             string
	OwnerUserID    string
	IsPublic       bool
	BucketName     string
	ObjectKey      string
	OriginalName   string
	ContentType    string
	SizeBytes      *int64
	ETag           *string
	ChecksumSHA256 *string
	Status         FileStatus
	CreatedAt      time.Time
	UpdatedAt      time.Time
	UploadedAt     *time.Time
	DeletedAt      *time.Time
}

// NewFile содержит расположение объекта, выбранное Media, и метаданные загрузки.
// ID создаётся вызывающим кодом до формирования object_key.
type NewFile struct {
	ID           string
	OwnerUserID  string
	IsPublic     bool
	BucketName   string
	ObjectKey    string
	OriginalName string
	ContentType  string
}

// ObjectInfo содержит фактические метаданные объекта из хранилища.
// ETag не считается контрольной суммой SHA-256.
type ObjectInfo struct {
	SizeBytes      int64
	ContentType    string
	ETag           *string
	ChecksumSHA256 *string
}
