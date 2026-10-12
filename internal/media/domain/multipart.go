package domain

import "time"

const (
	MinPartSize         int64 = 5 * 1024 * 1024
	MaxPartSize         int64 = 5 * 1024 * 1024 * 1024
	MaxParts                  = 10000
	MultipartUploading        = "uploading"
	MultipartCompleting       = "completing"
	MultipartCompleted        = "completed"
	MultipartAborted          = "aborted"
)

type Part struct {
	Number    int32  `json:"part_number"`
	ETag      string `json:"etag"`
	SizeBytes int64  `json:"size_bytes"`
}

type Multipart struct {
	FileID       string
	UploadID     string
	ExpectedSize int64
	PartSize     int64
	Status       string
	ExpiresAt    time.Time
	// Manifest is persisted before CompleteMultipartUpload for crash recovery.
	Manifest []Part
}

func (m Multipart) PartCount() int32 { return int32((m.ExpectedSize-1)/m.PartSize + 1) }
func (m Multipart) SizeOfPart(number int32) int64 {
	if number == m.PartCount() {
		return m.ExpectedSize - int64(number-1)*m.PartSize
	}
	return m.PartSize
}

type MultipartState struct {
	File          File
	Upload        Multipart
	Parts         []Part
	UploadedBytes int64
}

type PartURL struct {
	Number    int32
	SizeBytes int64
	Request   SignedRequest
}
