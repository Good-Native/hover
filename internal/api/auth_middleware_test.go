package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/good-native/hover/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockAuthClient is a mock implementation for testing auth middleware
type MockAuthClient struct {
	mock.Mock
}

type authErrorResponse struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
}

func (m *MockAuthClient) ValidateToken(ctx context.Context, token string) (*auth.UserClaims, error) {
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*auth.UserClaims), args.Error(1)
}

func (m *MockAuthClient) ExtractTokenFromRequest(r *http.Request) (string, error) {
	args := m.Called(r)
	return args.String(0), args.Error(1)
}

func (m *MockAuthClient) SetUserInContext(r *http.Request, user *auth.UserClaims) *http.Request {
	args := m.Called(r, user)
	if req := args.Get(0); req != nil {
		if httpReq, ok := req.(*http.Request); ok {
			return httpReq
		}
	}
	// Return the original request if mock doesn't specify otherwise
	return r
}

// TestAuthMiddleware tests the core authentication middleware logic
func TestAuthMiddleware(t *testing.T) {
	tests := []struct {
		name              string
		setupMocks        func(*MockAuthClient)
		requestHeaders    map[string]string
		expectedStatus    int
		expectUserContext bool
		checkResponse     func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name: "successful_authentication",
			setupMocks: func(mac *MockAuthClient) {
				// Mock successful token extraction
				mac.On("ExtractTokenFromRequest", mock.AnythingOfType("*http.Request")).Return("valid-jwt-token", nil)

				// Mock successful token validation
				userClaims := &auth.UserClaims{
					UserID: "user-123",
					Email:  "test@example.com",
				}
				mac.On("ValidateToken", mock.Anything, "valid-jwt-token").Return(userClaims, nil)

				// Mock context setting - return the modified request
				mac.On("SetUserInContext", mock.AnythingOfType("*http.Request"), userClaims).Return((*http.Request)(nil)).Run(func(args mock.Arguments) {
					// Mock will return the original request from the fallback logic
				})
			},
			requestHeaders: map[string]string{
				"Authorization": "Bearer valid-jwt-token",
			},
			expectedStatus:    http.StatusOK,
			expectUserContext: true,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				assert.Equal(t, "authenticated", rec.Body.String())
			},
		},
		{
			name: "missing_authorization_header",
			setupMocks: func(mac *MockAuthClient) {
				// Mock token extraction returning empty token
				mac.On("ExtractTokenFromRequest", mock.AnythingOfType("*http.Request")).Return("", assert.AnError)
			},
			requestHeaders:    map[string]string{},
			expectedStatus:    http.StatusUnauthorized,
			expectUserContext: false,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var response authErrorResponse
				err := json.Unmarshal(rec.Body.Bytes(), &response)
				require.NoError(t, err)

				assert.Equal(t, 401, response.Status)
				assert.Equal(t, "UNAUTHORISED", response.Code)
			},
		},
		{
			name: "invalid_token",
			setupMocks: func(mac *MockAuthClient) {
				// Mock successful token extraction
				mac.On("ExtractTokenFromRequest", mock.AnythingOfType("*http.Request")).Return("invalid-token", nil)

				// Mock failed token validation
				mac.On("ValidateToken", mock.Anything, "invalid-token").Return(nil, assert.AnError)
			},
			requestHeaders: map[string]string{
				"Authorization": "Bearer invalid-token",
			},
			expectedStatus:    http.StatusUnauthorized,
			expectUserContext: false,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var response authErrorResponse
				err := json.Unmarshal(rec.Body.Bytes(), &response)
				require.NoError(t, err)

				assert.Equal(t, 401, response.Status)
				assert.Equal(t, "UNAUTHORISED", response.Code)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock auth client and set up expectations
			mockAuthClient := new(MockAuthClient)
			tt.setupMocks(mockAuthClient)

			// Create test handler that returns success
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("authenticated"))
			})

			// Create middleware with mock client
			middleware := auth.AuthMiddlewareWithClient(mockAuthClient)
			handler := middleware(testHandler)

			// Create test request
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			for key, value := range tt.requestHeaders {
				req.Header.Set(key, value)
			}
			rec := httptest.NewRecorder()

			// Execute request
			handler.ServeHTTP(rec, req)

			// Check status code
			assert.Equal(t, tt.expectedStatus, rec.Code)

			// Run custom response checks if provided
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}

			// Verify all mock expectations were met
			mockAuthClient.AssertExpectations(t)
		})
	}
}

// TestGetUserFromContext tests the utility function for extracting user from context
func TestGetUserFromContext(t *testing.T) {
	tests := []struct {
		name           string
		setupContext   func() context.Context
		expectedUser   *auth.UserClaims
		expectedExists bool
	}{
		{
			name: "user_present_in_context",
			setupContext: func() context.Context {
				user := &auth.UserClaims{
					UserID: "test-user",
					Email:  "test@example.com",
				}
				return context.WithValue(context.Background(), auth.UserKey, user)
			},
			expectedUser: &auth.UserClaims{
				UserID: "test-user",
				Email:  "test@example.com",
			},
			expectedExists: true,
		},
		{
			name: "user_not_in_context",
			setupContext: func() context.Context {
				return context.Background()
			},
			expectedUser:   nil,
			expectedExists: false,
		},
		{
			name: "wrong_type_in_context",
			setupContext: func() context.Context {
				return context.WithValue(context.Background(), auth.UserKey, "not-a-user")
			},
			expectedUser:   nil,
			expectedExists: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupContext()

			user, exists := auth.GetUserFromContext(ctx)

			assert.Equal(t, tt.expectedExists, exists)
			if tt.expectedExists {
				require.NotNil(t, user)
				assert.Equal(t, tt.expectedUser.UserID, user.UserID)
				assert.Equal(t, tt.expectedUser.Email, user.Email)
			} else {
				assert.Nil(t, user)
			}
		})
	}
}
