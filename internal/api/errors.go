package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/Harvey-AU/hover/internal/db"
)

type ErrorResponse struct {
	Status    int    `json:"status"`
	Message   string `json:"message"`
	Code      string `json:"code,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

type ErrorCode string

const (
	ErrCodeBadRequest       ErrorCode = "BAD_REQUEST"
	ErrCodeUnauthorised     ErrorCode = "UNAUTHORISED"
	ErrCodeForbidden        ErrorCode = "FORBIDDEN"
	ErrCodeNotFound         ErrorCode = "NOT_FOUND"
	ErrCodeMethodNotAllowed ErrorCode = "METHOD_NOT_ALLOWED"
	ErrCodeConflict         ErrorCode = "CONFLICT"
	ErrCodeValidation       ErrorCode = "VALIDATION_ERROR"
	ErrCodeRateLimit        ErrorCode = "RATE_LIMIT_EXCEEDED"

	ErrCodeInternal           ErrorCode = "INTERNAL_ERROR"
	ErrCodeServiceUnavailable ErrorCode = "SERVICE_UNAVAILABLE"
	ErrCodeDatabaseError      ErrorCode = "DATABASE_ERROR"
)

func WriteError(w http.ResponseWriter, r *http.Request, err error, status int, code ErrorCode) {
	requestID := GetRequestID(r)

	errResp := ErrorResponse{
		Status:    status,
		Message:   err.Error(),
		Code:      string(code),
		RequestID: requestID,
	}

	// 4xx → debug, 5xx → error.
	logger := loggerWithRequest(r)
	if status >= 500 {
		logger.Error("API error response", "error", err, "status", status, "code", string(code))
	} else {
		logger.Debug("API client error response", "error", err, "status", status, "code", string(code))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errResp); err != nil {
		logger.Error("Failed to encode error response", "error", err)
	}
}

func WriteErrorMessage(w http.ResponseWriter, r *http.Request, message string, status int, code ErrorCode) {
	requestID := GetRequestID(r)

	errResp := ErrorResponse{
		Status:    status,
		Message:   message,
		Code:      string(code),
		RequestID: requestID,
	}

	logger := loggerWithRequest(r)
	if status >= 500 {
		logger.Error("API error response", "status", status, "code", string(code), "message", message)
	} else {
		logger.Debug("API client error response", "status", status, "code", string(code), "message", message)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errResp); err != nil {
		logger.Error("Failed to encode error response", "error", err)
	}
}

func BadRequest(w http.ResponseWriter, r *http.Request, message string) {
	WriteErrorMessage(w, r, message, http.StatusBadRequest, ErrCodeBadRequest)
}

func Unauthorised(w http.ResponseWriter, r *http.Request, message string) {
	WriteErrorMessage(w, r, message, http.StatusUnauthorized, ErrCodeUnauthorised)
}

func Forbidden(w http.ResponseWriter, r *http.Request, message string) {
	WriteErrorMessage(w, r, message, http.StatusForbidden, ErrCodeForbidden)
}

func NotFound(w http.ResponseWriter, r *http.Request, message string) {
	WriteErrorMessage(w, r, message, http.StatusNotFound, ErrCodeNotFound)
}

func Conflict(w http.ResponseWriter, r *http.Request, message string) {
	WriteErrorMessage(w, r, message, http.StatusConflict, ErrCodeConflict)
}

func MethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	WriteErrorMessage(w, r, "Method not allowed", http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed)
}

func InternalError(w http.ResponseWriter, r *http.Request, err error) {
	WriteError(w, r, err, http.StatusInternalServerError, ErrCodeInternal)
}

func DatabaseError(w http.ResponseWriter, r *http.Request, err error) {
	WriteError(w, r, err, http.StatusInternalServerError, ErrCodeDatabaseError)
}

func ServiceUnavailable(w http.ResponseWriter, r *http.Request, message string) {
	WriteErrorMessage(w, r, message, http.StatusServiceUnavailable, ErrCodeServiceUnavailable)
}

// Sets Retry-After header.
func TooManyRequests(w http.ResponseWriter, r *http.Request, message string, retryAfter time.Duration) {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds <= 0 {
		seconds = 3
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	WriteErrorMessage(w, r, message, http.StatusTooManyRequests, ErrCodeRateLimit)
}

// Returns true and writes a 429 when err indicates pool exhaustion.
func HandlePoolSaturation(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, db.ErrPoolSaturated) {
		TooManyRequests(w, r, "Database is busy, please retry shortly", 3*time.Second)
		return true
	}
	return false
}
