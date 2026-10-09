package domain

import "time"

// SignedRequest содержит адрес и параметры HTTP-запроса к приватному объекту.
type SignedRequest struct {
	URL       string
	Method    string
	Headers   map[string]string
	ExpiresAt time.Time
}

type UploadInput struct {
	Bucket      string
	Key         string
	ContentType string
	SizeBytes   int64
	TTL         time.Duration
}

type DownloadInput struct {
	Bucket       string
	Key          string
	OriginalName string
	TTL          time.Duration
}

type Upload struct {
	File    File
	Request SignedRequest
}
