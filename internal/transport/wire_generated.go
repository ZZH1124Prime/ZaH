// Code generated from schemas/rpc.json; DO NOT EDIT.
package transport

import (
	"encoding/json"
	"time"
)

const ProtocolVersion = 1
const MaxFrameBytes = 1048576
const MaxConnections = 32
const DefaultTimeout = 30 * time.Second

// ApplicationsStopAllInput is a generated protocol envelope.
type ApplicationsStopAllInput struct {
	Confirmed bool `json:"confirmed"`
}

// ApplicationsStopAllPlanParams is a generated protocol envelope.
type ApplicationsStopAllPlanParams struct {
	Input     ApplicationsStopAllInput `json:"input"`
	Operation string                   `json:"operation"`
}

// CatalogRecord is a generated protocol envelope.
type CatalogRecord struct {
	CreatedAt string          `json:"created_at"`
	Data      json.RawMessage `json:"data"`
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	UpdatedAt string          `json:"updated_at"`
}

// Error is a generated protocol envelope.
type Error struct {
	Code    int    `json:"code"`
	Data    any    `json:"data,omitempty"`
	Message string `json:"message"`
}

// Event is a generated protocol envelope.
type Event struct {
	CreatedAt string          `json:"created_at"`
	Data      json.RawMessage `json:"data"`
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
}

// EventPage is a generated protocol envelope.
type EventPage struct {
	Events     []Event                    `json:"events"`
	HasMore    bool                       `json:"has_more"`
	NextCursor int64                      `json:"next_cursor"`
	Snapshot   map[string][]CatalogRecord `json:"snapshot,omitempty"`
}

// HelloParams is a generated protocol envelope.
type HelloParams struct {
	Version int `json:"version"`
}

// HelloResult is a generated protocol envelope.
type HelloResult struct {
	MaxConnections int `json:"max_connections"`
	MaxFrameBytes  int `json:"max_frame_bytes"`
	Version        int `json:"version"`
}

// InstanceStopInput is a generated protocol envelope.
type InstanceStopInput struct {
	Id string `json:"id"`
}

// InstanceStopPlanParams is a generated protocol envelope.
type InstanceStopPlanParams struct {
	Input     InstanceStopInput `json:"input"`
	Operation string            `json:"operation"`
}

// ReleaseIconSetParams is a generated protocol envelope.
type ReleaseIconSetParams struct {
	Id   string `json:"id"`
	Path string `json:"path"`
}

// Request is a generated protocol envelope.
type Request struct {
	ID      json.RawMessage `json:"id,omitempty"`
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a generated protocol envelope.
type Response struct {
	Error   *Error          `json:"error,omitempty"`
	ID      json.RawMessage `json:"id"`
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
}

// ResumeParams is a generated protocol envelope.
type ResumeParams struct {
	After *int64 `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}
