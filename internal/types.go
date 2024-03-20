package internal

import "fmt"

type ZoneRequest struct {
	Data Zone `json:"data,omitempty"`
}

type APIResponse[T any] struct {
	Meta  *Metadata `json:"meta,omitempty"`
	Data  T         `json:"data,omitempty"`
	Error *Error    `json:"error,omitempty"`
}

type Metadata struct {
	InvocationID string `json:"invocationId,omitempty"`
}

type Zone struct {
	Type       string     `json:"type,omitempty"`
	ID         string     `json:"id,omitempty"`
	Attributes Attributes `json:"attributes,omitempty"`
}

type Attributes struct {
	Records map[string]map[string][]Record `json:"records,omitempty"`
}

type Record struct {
	TTL      int    `json:"ttl,omitempty"`
	Data     string `json:"rdata,omitempty"`
	Comments string `json:"comments,omitempty"`
}

type Error struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("api error: status=%d, message=%s", e.Status, e.Message)
}
