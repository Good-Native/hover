package mocks

import (
	"context"
	"net/http"

	"github.com/good-native/hover/internal/auth"
	"github.com/stretchr/testify/mock"
)

// MockAuthClient is a mock implementation of an authentication client
type MockAuthClient struct {
	mock.Mock
}

// ValidateToken mocks JWT token validation
func (m *MockAuthClient) ValidateToken(ctx context.Context, token string) (*AuthUser, error) {
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*AuthUser), args.Error(1)
}

// RefreshToken mocks token refresh functionality
func (m *MockAuthClient) RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	args := m.Called(ctx, refreshToken)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*TokenResponse), args.Error(1)
}

// GetUserClaims mocks extracting user claims from JWT token
func (m *MockAuthClient) GetUserClaims(ctx context.Context, token string) (*UserClaims, error) {
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*UserClaims), args.Error(1)
}

// CheckPermissions mocks organisation role permission checks
func (m *MockAuthClient) CheckPermissions(ctx context.Context, userID string, orgID string, permission string) (bool, error) {
	args := m.Called(ctx, userID, orgID, permission)
	return args.Bool(0), args.Error(1)
}

// CreateUser mocks user registration functionality
func (m *MockAuthClient) CreateUser(ctx context.Context, email string, password string, metadata map[string]any) (*AuthUser, error) {
	args := m.Called(ctx, email, password, metadata)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*AuthUser), args.Error(1)
}

// SignIn mocks user sign-in functionality
func (m *MockAuthClient) SignIn(ctx context.Context, email string, password string) (*TokenResponse, error) {
	args := m.Called(ctx, email, password)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*TokenResponse), args.Error(1)
}

// SignOut mocks user sign-out functionality
func (m *MockAuthClient) SignOut(ctx context.Context, token string) error {
	args := m.Called(ctx, token)
	return args.Error(0)
}

// AuthUser represents an authenticated user for testing
type AuthUser struct {
	ID           string         `json:"id"`
	Email        string         `json:"email"`
	FullName     string         `json:"full_name,omitempty"`
	Metadata     map[string]any `json:"user_metadata,omitempty"`
	AppMetadata  map[string]any `json:"app_metadata,omitempty"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
	LastSignInAt string         `json:"last_sign_in_at,omitempty"`
}

// TokenResponse represents authentication tokens for testing
type TokenResponse struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int       `json:"expires_in"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	User         *AuthUser `json:"user,omitempty"`
}

// UserClaims represents JWT token claims for testing
type UserClaims struct {
	Sub           string         `json:"sub"`   // User ID
	Email         string         `json:"email"` // User email
	EmailVerified bool           `json:"email_verified"`
	Role          string         `json:"role,omitempty"`
	UserMetadata  map[string]any `json:"user_metadata,omitempty"`
	AppMetadata   map[string]any `json:"app_metadata,omitempty"`
	Aud           string         `json:"aud"` // Audience
	Iss           string         `json:"iss"` // Issuer
	Iat           int64          `json:"iat"` // Issued at
	Exp           int64          `json:"exp"` // Expires at
}

// MockSupabaseClient is a mock for Supabase authentication client
type MockSupabaseClient struct {
	mock.Mock
}

// Auth mocks the Auth method to return auth client
func (m *MockSupabaseClient) Auth() *MockAuthClient {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*MockAuthClient)
}

// MockAuthMiddleware is a mock for authentication middleware testing
type MockAuthMiddleware struct {
	mock.Mock
}

// Middleware mocks the authentication middleware handler
func (m *MockAuthMiddleware) Middleware(next http.Handler) http.Handler {
	args := m.Called(next)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(http.Handler)
}

// ExtractTokenFromRequest mocks token extraction from HTTP request
func (m *MockAuthMiddleware) ExtractTokenFromRequest(r *http.Request) (string, error) {
	args := m.Called(r)
	return args.String(0), args.Error(1)
}

// SetUserInContext mocks setting user context in HTTP request
func (m *MockAuthMiddleware) SetUserInContext(r *http.Request, user *AuthUser) *http.Request {
	args := m.Called(r, user)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*http.Request)
}

// AuthClientInterface defines the interface for authentication client operations
type AuthClientInterface interface {
	ValidateToken(ctx context.Context, token string) (*AuthUser, error)
	RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error)
	GetUserClaims(ctx context.Context, token string) (*UserClaims, error)
	CheckPermissions(ctx context.Context, userID string, orgID string, permission string) (bool, error)
	CreateUser(ctx context.Context, email string, password string, metadata map[string]any) (*AuthUser, error)
	SignIn(ctx context.Context, email string, password string) (*TokenResponse, error)
	SignOut(ctx context.Context, token string) error
}

// NewMockAuthConfig creates a mock auth configuration for testing
func NewMockAuthConfig() *auth.Config {
	return &auth.Config{
		AuthURL:        "https://test.supabase.co",
		PublishableKey: "sb_publishable_test_key",
	}
}
