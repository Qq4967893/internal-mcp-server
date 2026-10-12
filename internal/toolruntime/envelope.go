package toolruntime

import "time"

type Status string

const (
	StatusSuccess Status = "SUCCESS"
	StatusError   Status = "ERROR"
)

type Source struct {
	Service    string    `json:"service"`
	Resource   string    `json:"resource"`
	ObservedAt time.Time `json:"observed_at"`
}

type Pagination struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	Total  int `json:"total_count"`
}

type Envelope[T any] struct {
	Status     Status       `json:"status"`
	Data       *T           `json:"data"`
	Source     *Source      `json:"source"`
	Pagination *Pagination  `json:"pagination"`
	Warnings   []string     `json:"warnings"`
	Error      *ErrorDetail `json:"error,omitempty"`
}

type ErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func Success[T any](data T, source Source) Envelope[T] {
	return Envelope[T]{
		Status:     StatusSuccess,
		Data:       &data,
		Source:     &source,
		Pagination: nil,
		Warnings:   []string{},
	}
}

func Failure[T any](code, message string, retryable bool) Envelope[T] {
	return Envelope[T]{
		Status:   StatusError,
		Warnings: []string{},
		Error: &ErrorDetail{
			Code:      code,
			Message:   message,
			Retryable: retryable,
		},
	}
}
