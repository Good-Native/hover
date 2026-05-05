package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/good-native/hover/internal/logging"
)

var authLog = logging.Component("auth")

type AuthClient interface {
	ValidateToken(ctx context.Context, token string) (*UserClaims, error)
	ExtractTokenFromRequest(r *http.Request) (string, error)
	SetUserInContext(r *http.Request, user *UserClaims) *http.Request
}

type SupabaseAuthClient struct{}

func NewSupabaseAuthClient() *SupabaseAuthClient {
	return &SupabaseAuthClient{}
}

func (s *SupabaseAuthClient) ValidateToken(ctx context.Context, token string) (*UserClaims, error) {
	return validateSupabaseToken(ctx, token)
}

func (s *SupabaseAuthClient) ExtractTokenFromRequest(r *http.Request) (string, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		return "", fmt.Errorf("missing or invalid Authorization header")
	}
	return strings.TrimPrefix(authHeader, "Bearer "), nil
}

func (s *SupabaseAuthClient) SetUserInContext(r *http.Request, user *UserClaims) *http.Request {
	ctx := context.WithValue(r.Context(), UserKey, user)
	return r.WithContext(ctx)
}

type UserContextKey string

const (
	UserKey UserContextKey = "user"
)

type UserClaims struct {
	jwt.RegisteredClaims
	UserID       string         `json:"sub"`
	Email        string         `json:"email"`
	AppMetadata  map[string]any `json:"app_metadata"`
	UserMetadata map[string]any `json:"user_metadata"`
	Role         string         `json:"role"`
}

func AuthMiddleware(next http.Handler) http.Handler {
	return AuthMiddlewareWithClient(NewSupabaseAuthClient())(next)
}

func AuthMiddlewareWithClient(authClient AuthClient) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract the JWT from the Authorization header
			tokenString, err := authClient.ExtractTokenFromRequest(r)
			if err != nil {
				writeAuthError(w, r, "Missing or invalid Authorization header", http.StatusUnauthorized)
				return
			}

			claims, err := authClient.ValidateToken(r.Context(), tokenString)
			if err != nil {
				authLog.Warn("JWT validation failed", "error", err, "token_length", len(tokenString))

				// Sentry capture: skip expired (normal); error-log signature failures and JWKS misconfig.
				errorMsg := "Invalid authentication token"
				statusCode := http.StatusUnauthorized

				if strings.Contains(err.Error(), "expired") {
					errorMsg = "Authentication token has expired"
				} else if strings.Contains(err.Error(), "signature") {
					errorMsg = "Invalid token signature"
					authLog.Error("Invalid token signature", "error", err)
				} else if strings.Contains(err.Error(), "JWKS") || strings.Contains(err.Error(), "jwks") || strings.Contains(err.Error(), "certs") || strings.Contains(err.Error(), "keyfunc") {
					errorMsg = "Authentication service misconfigured"
					statusCode = http.StatusInternalServerError
					authLog.Error("Authentication service misconfigured", "error", err)
				}

				writeAuthError(w, r, errorMsg, statusCode)
				return
			}

			r = authClient.SetUserInContext(r, claims)
			next.ServeHTTP(w, r)
		})
	}
}

var (
	jwksOnce    sync.Once
	jwksCache   keyfunc.Keyfunc
	jwksInitErr error
)

func getFallbackAuthURL() string {
	for _, key := range []string{"SUPABASE_FALLBACK_AUTH_URL", "SUPABASE_LEGACY_AUTH_URL"} {
		if value := strings.TrimSuffix(os.Getenv(key), "/"); value != "" {
			return value
		}
	}
	return ""
}

func getJWKS() (keyfunc.Keyfunc, error) {
	jwksOnce.Do(func() {
		authURL := strings.TrimSuffix(os.Getenv("SUPABASE_AUTH_URL"), "/")
		if authURL == "" {
			jwksInitErr = fmt.Errorf("SUPABASE_AUTH_URL environment variable not set")
			return
		}

		jwksURL := fmt.Sprintf("%s/auth/v1/.well-known/jwks.json", authURL)
		jwksURLs := []string{jwksURL}

		// Fallback auth domain during domain migrations.
		if fallbackAuthURL := getFallbackAuthURL(); fallbackAuthURL != "" && fallbackAuthURL != authURL {
			jwksURLs = append(jwksURLs, fmt.Sprintf("%s/auth/v1/.well-known/jwks.json", fallbackAuthURL))
		}

		override := keyfunc.Override{
			Client:          &http.Client{Timeout: 5 * time.Second},
			HTTPTimeout:     5 * time.Second,
			RefreshInterval: 10 * time.Minute,
			RefreshErrorHandlerFunc: func(url string) func(ctx context.Context, err error) {
				return func(ctx context.Context, err error) {
					authLog.Error("JWKS refresh failed", "error", err, "jwks_url", url)
				}
			},
		}

		childCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		jwksCache, jwksInitErr = keyfunc.NewDefaultOverrideCtx(childCtx, jwksURLs, override)
	})

	if jwksInitErr != nil {
		return nil, jwksInitErr
	}
	return jwksCache, nil
}

func validateSupabaseToken(ctx context.Context, tokenString string) (*UserClaims, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("request context cancelled: %w", ctx.Err())
	default:
	}

	jwks, err := getJWKS()
	if err != nil {
		return nil, fmt.Errorf("failed to initialise JWKS: %w", err)
	}

	authURL := strings.TrimSuffix(os.Getenv("SUPABASE_AUTH_URL"), "/")
	if authURL == "" {
		return nil, fmt.Errorf("SUPABASE_AUTH_URL environment variable not set")
	}

	// Issuer validated below — parser only checks signature/expiry here.
	token, err := jwt.ParseWithClaims(
		tokenString,
		&UserClaims{},
		jwks.Keyfunc,
		jwt.WithValidMethods([]string{
			jwt.SigningMethodRS256.Name,
			jwt.SigningMethodES256.Name,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	claims, ok := token.Claims.(*UserClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	// Allow legacy issuer during auth-domain migrations.
	validIssuers := []string{
		fmt.Sprintf("%s/auth/v1", authURL),
	}
	if fallbackAuthURL := getFallbackAuthURL(); fallbackAuthURL != "" && fallbackAuthURL != authURL {
		validIssuers = append(validIssuers, fmt.Sprintf("%s/auth/v1", fallbackAuthURL))
	}

	issuer, err := claims.GetIssuer()
	if err != nil {
		return nil, fmt.Errorf("failed to read issuer: %w", err)
	}

	validIssuer := slices.Contains(validIssuers, issuer)
	if !validIssuer {
		return nil, fmt.Errorf("token has unexpected issuer: %s", issuer)
	}

	audiences, err := claims.GetAudience()
	if err != nil {
		return nil, fmt.Errorf("failed to read audience: %w", err)
	}
	if len(audiences) == 0 {
		return nil, fmt.Errorf("token missing audience")
	}

	validAudience := false
	for _, aud := range audiences {
		if aud == "authenticated" || aud == "service_role" {
			validAudience = true
			break
		}
	}
	if !validAudience {
		return nil, fmt.Errorf("token has unexpected audience: %v", audiences)
	}

	return claims, nil
}

func resetJWKSForTest() {
	jwksOnce = sync.Once{}
	jwksCache = nil
	jwksInitErr = nil
}

func GetUserFromContext(ctx context.Context) (*UserClaims, bool) {
	user, ok := ctx.Value(UserKey).(*UserClaims)
	return user, ok
}

// Validates JWT if present; doesn't require one.
func OptionalAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
			tokenString := strings.TrimPrefix(authHeader, "Bearer ")

			// Try to validate the token
			claims, err := validateSupabaseToken(r.Context(), tokenString)
			if err == nil {
				// Token is valid, add to context
				ctx := context.WithValue(r.Context(), UserKey, claims)
				r = r.WithContext(ctx)
			} else {
				// Token is invalid but we continue without auth
				authLog.Warn("Invalid JWT token in optional auth", "error", err)
			}
		}

		next.ServeHTTP(w, r)
	})
}

func writeAuthError(w http.ResponseWriter, r *http.Request, message string, statusCode int) {
	var requestID string
	if r != nil && r.Context() != nil {
		if rid := r.Context().Value("request_id"); rid != nil {
			if ridStr, ok := rid.(string); ok {
				requestID = ridStr
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	response := map[string]any{
		"status":     statusCode,
		"message":    message,
		"code":       "UNAUTHORISED",
		"request_id": requestID,
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		authLog.Error("Failed to encode unauthorised response", "error", err)
	}
}

type SessionInfo struct {
	IsValid       bool   `json:"is_valid"`
	ExpiresAt     int64  `json:"expires_at,omitempty"`
	RefreshNeeded bool   `json:"refresh_needed"`
	UserID        string `json:"user_id,omitempty"`
	Email         string `json:"email,omitempty"`
}

func ValidateSession(tokenString string) *SessionInfo {
	claims, err := validateSupabaseToken(context.Background(), tokenString)
	if err != nil {
		return &SessionInfo{
			IsValid:       false,
			RefreshNeeded: strings.Contains(err.Error(), "expired"),
		}
	}

	// Refresh threshold: 5 min before expiry.
	refreshNeeded := false
	if claims.ExpiresAt != nil {
		timeUntilExpiry := claims.ExpiresAt.Unix() - time.Now().Unix()
		refreshNeeded = timeUntilExpiry < 300
	}

	return &SessionInfo{
		IsValid:       true,
		ExpiresAt:     claims.ExpiresAt.Unix(),
		RefreshNeeded: refreshNeeded,
		UserID:        claims.UserID,
		Email:         claims.Email,
	}
}
