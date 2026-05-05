package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/good-native/hover/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthRegister(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		body           any
		expectedStatus int
		expectedError  string
		skipDBCheck    bool // Skip tests that require DB
	}{
		{
			name:   "valid_registration",
			method: http.MethodPost,
			body: AuthRegisterRequest{
				UserID: "user-123",
				Email:  "test@example.com",
			},
			expectedStatus: http.StatusNotImplemented,
			skipDBCheck:    true, // Requires DB
		},
		{
			name:           "wrong_method",
			method:         http.MethodGet,
			body:           nil,
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "invalid_json",
			method:         http.MethodPost,
			body:           "invalid json",
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Invalid JSON request body",
		},
		{
			name:   "missing_user_id",
			method: http.MethodPost,
			body: AuthRegisterRequest{
				Email: "test@example.com",
			},
			expectedStatus: http.StatusBadRequest,
			expectedError:  "user_id and email are required",
		},
		{
			name:   "missing_email",
			method: http.MethodPost,
			body: AuthRegisterRequest{
				UserID: "user-123",
			},
			expectedStatus: http.StatusBadRequest,
			expectedError:  "user_id and email are required",
		},
		{
			name:   "with_full_name",
			method: http.MethodPost,
			body: AuthRegisterRequest{
				UserID:   "user-123",
				Email:    "test@example.com",
				FullName: new("John Doe"),
			},
			expectedStatus: http.StatusNotImplemented,
			skipDBCheck:    true, // Requires DB
		},
		{
			name:   "with_org_name",
			method: http.MethodPost,
			body: AuthRegisterRequest{
				UserID:  "user-123",
				Email:   "test@company.com",
				OrgName: new("My Company"),
			},
			expectedStatus: http.StatusNotImplemented,
			skipDBCheck:    true, // Requires DB
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skipDBCheck {
				t.Skip("Skipping test that requires database")
			}

			h := &Handler{}

			var body []byte
			if tt.body != nil {
				if str, ok := tt.body.(string); ok {
					body = []byte(str)
				} else {
					var err error
					body, err = json.Marshal(tt.body)
					require.NoError(t, err)
				}
			}

			req := httptest.NewRequest(tt.method, "/v1/auth/register", bytes.NewReader(body))
			rec := httptest.NewRecorder()

			h.AuthRegister(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)

			if tt.expectedError != "" && rec.Code >= 400 {
				var errResp map[string]any
				err := json.Unmarshal(rec.Body.Bytes(), &errResp)
				if err == nil && errResp != nil {
					if msg, ok := errResp["message"].(string); ok {
						assert.Contains(t, msg, tt.expectedError)
					}
				}
			}
		})
	}
}

func TestAuthSession(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		body           any
		hasAuth        bool
		expectedStatus int
		expectedError  string
	}{
		{
			name:   "valid_session_request",
			method: http.MethodPost,
			body: AuthSessionRequest{
				Token: "valid-token",
			},
			hasAuth:        false,
			expectedStatus: http.StatusOK, // Session endpoint returns OK
		},
		{
			name:           "wrong_method",
			method:         http.MethodGet,
			body:           nil,
			hasAuth:        false,
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "invalid_json",
			method:         http.MethodPost,
			body:           "invalid json",
			hasAuth:        false,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Invalid JSON request body",
		},
		{
			name:   "missing_token",
			method: http.MethodPost,
			body: AuthSessionRequest{
				Token: "",
			},
			hasAuth:        false,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "token is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{}

			var body []byte
			if tt.body != nil {
				if str, ok := tt.body.(string); ok {
					body = []byte(str)
				} else {
					var err error
					body, err = json.Marshal(tt.body)
					require.NoError(t, err)
				}
			}

			req := httptest.NewRequest(tt.method, "/v1/auth/session", bytes.NewReader(body))

			if tt.hasAuth {
				ctx := context.WithValue(req.Context(), auth.UserKey, &auth.UserClaims{
					UserID: "test-user",
					Email:  "test@example.com",
				})
				req = req.WithContext(ctx)
			}

			rec := httptest.NewRecorder()
			h.AuthSession(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)

			if tt.expectedError != "" && rec.Code >= 400 {
				var errResp map[string]any
				err := json.Unmarshal(rec.Body.Bytes(), &errResp)
				if err == nil && errResp != nil {
					if msg, ok := errResp["message"].(string); ok {
						assert.Contains(t, msg, tt.expectedError)
					}
				}
			}
		})
	}
}

func TestAuthProfile(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		hasAuth        bool
		expectedStatus int
		skipDBCheck    bool
	}{
		{
			name:           "authenticated_user",
			method:         http.MethodGet,
			hasAuth:        true,
			expectedStatus: http.StatusNotImplemented,
			skipDBCheck:    true, // Requires DB
		},
		{
			name:           "unauthenticated_user",
			method:         http.MethodGet,
			hasAuth:        false,
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "wrong_method",
			method:         http.MethodPost,
			hasAuth:        true,
			expectedStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skipDBCheck {
				t.Skip("Skipping test that requires database")
			}

			h := &Handler{}

			req := httptest.NewRequest(tt.method, "/v1/auth/profile", nil)

			if tt.hasAuth {
				ctx := context.WithValue(req.Context(), auth.UserKey, &auth.UserClaims{
					UserID: "test-user",
					Email:  "test@example.com",
				})
				req = req.WithContext(ctx)
			}

			rec := httptest.NewRecorder()
			h.AuthProfile(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
		})
	}
}

func TestUserResponse(t *testing.T) {
	user := UserResponse{
		ID:             "user-123",
		Email:          "test@example.com",
		FullName:       new("John Doe"),
		OrganisationID: new("org-456"),
		CreatedAt:      "2024-01-01T12:00:00Z",
		UpdatedAt:      "2024-01-02T12:00:00Z",
	}

	data, err := json.Marshal(user)
	require.NoError(t, err)

	var decoded UserResponse
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, user.ID, decoded.ID)
	assert.Equal(t, user.Email, decoded.Email)
	assert.Equal(t, *user.FullName, *decoded.FullName)
	assert.Equal(t, *user.OrganisationID, *decoded.OrganisationID)
	assert.Equal(t, user.CreatedAt, decoded.CreatedAt)
	assert.Equal(t, user.UpdatedAt, decoded.UpdatedAt)
}

func TestOrganisationResponse(t *testing.T) {
	org := OrganisationResponse{
		ID:        "org-123",
		Name:      "My Company",
		CreatedAt: "2024-01-01T12:00:00Z",
		UpdatedAt: "2024-01-02T12:00:00Z",
	}

	data, err := json.Marshal(org)
	require.NoError(t, err)

	var decoded OrganisationResponse
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, org.ID, decoded.ID)
	assert.Equal(t, org.Name, decoded.Name)
	assert.Equal(t, org.CreatedAt, decoded.CreatedAt)
	assert.Equal(t, org.UpdatedAt, decoded.UpdatedAt)
}

// Helper function
//
//go:fix inline
func strPtr(s string) *string {
	return new(s)
}
