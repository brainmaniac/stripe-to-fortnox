package fortnox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"stripe-fortnox-sync/internal/db"
)

const (
	tokenURL     = "https://apps.fortnox.se/oauth-v1/token"
	authorizeURL = "https://apps.fortnox.se/oauth-v1/auth"
)

// OAuthClient manages Fortnox OAuth2 tokens.
type OAuthClient struct {
	clientID     string
	clientSecret string
	redirectURI  string
	queries      *db.Queries
}

func NewOAuthClient(clientID, clientSecret, baseURL string, queries *db.Queries) *OAuthClient {
	return &OAuthClient{
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURI:  baseURL + "/auth/fortnox/callback",
		queries:      queries,
	}
}

// AuthorizeURL returns the URL to redirect the user to for Fortnox OAuth2 authorization.
func (o *OAuthClient) AuthorizeURL(state string) string {
	params := url.Values{
		"response_type": {"code"},
		"client_id":     {o.clientID},
		"redirect_uri":  {o.redirectURI},
		"scope":         {"bookkeeping invoice customer companyinformation payment costcenter supplier supplierinvoice"},
		"state":         {state},
	}
	return authorizeURL + "?" + params.Encode()
}

// TokenResponse holds the token payload from Fortnox.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// ExchangeCode exchanges an authorization code for access/refresh tokens.
func (o *OAuthClient) ExchangeCode(ctx context.Context, code string) (*TokenResponse, error) {
	data := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {o.redirectURI},
	}
	return o.postToken(ctx, data)
}

// RefreshAccessToken uses a refresh token to obtain new access/refresh tokens.
func (o *OAuthClient) RefreshAccessToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	return o.postToken(ctx, data)
}

func (o *OAuthClient) postToken(ctx context.Context, data url.Values) (*TokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	auth := base64.StdEncoding.EncodeToString([]byte(o.clientID + ":" + o.clientSecret))
	req.Header.Set("Authorization", "Basic "+auth)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token request failed %d: %s", resp.StatusCode, string(body))
	}

	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	return &tr, nil
}

// SaveTokens persists access/refresh tokens and their expiry to settings.
func (o *OAuthClient) SaveTokens(ctx context.Context, accessToken, refreshToken string, expiresIn int) error {
	expiry := time.Now().Add(time.Duration(expiresIn) * time.Second).Unix()
	if err := o.queries.UpsertSetting(ctx, "fortnox_access_token", accessToken, 1); err != nil {
		return err
	}
	if err := o.queries.UpsertSetting(ctx, "fortnox_refresh_token", refreshToken, 1); err != nil {
		return err
	}
	return o.queries.UpsertSetting(ctx, "fortnox_token_expiry", fmt.Sprintf("%d", expiry), 0)
}

// GetValidAccessToken returns a valid access token, refreshing it if necessary.
func (o *OAuthClient) GetValidAccessToken(ctx context.Context) (string, error) {
	accessSetting, err := o.queries.GetSetting(ctx, "fortnox_access_token")
	if err != nil || accessSetting == nil {
		return "", fmt.Errorf("no fortnox access token stored")
	}
	refreshSetting, err := o.queries.GetSetting(ctx, "fortnox_refresh_token")
	if err != nil || refreshSetting == nil {
		return "", fmt.Errorf("no fortnox refresh token stored")
	}
	expirySetting, _ := o.queries.GetSetting(ctx, "fortnox_token_expiry")

	if expirySetting != nil && expirySetting.Value != "" {
		var expiry int64
		fmt.Sscanf(expirySetting.Value, "%d", &expiry)
		if time.Now().Unix() < expiry-60 {
			return accessSetting.Value, nil
		}
	}

	tr, err := o.RefreshAccessToken(ctx, refreshSetting.Value)
	if err != nil {
		return "", fmt.Errorf("refresh fortnox token: %w", err)
	}
	if err := o.SaveTokens(ctx, tr.AccessToken, tr.RefreshToken, tr.ExpiresIn); err != nil {
		return "", fmt.Errorf("save refreshed tokens: %w", err)
	}
	return tr.AccessToken, nil
}

// StartTokenRefresher launches a background goroutine that proactively refreshes
// the Fortnox access token every 50 minutes. This keeps the refresh token alive
// (Fortnox refresh tokens expire after 30 days of non-use) even when there is
// nothing to sync.
func (o *OAuthClient) StartTokenRefresher(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(50 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := o.GetValidAccessToken(ctx); err != nil {
					log.Printf("fortnox token refresh: %v", err)
				} else {
					log.Printf("fortnox token refreshed proactively")
				}
			}
		}
	}()
}

// IsConnected returns true if Fortnox tokens are stored.
func (o *OAuthClient) IsConnected(ctx context.Context) bool {
	setting, err := o.queries.GetSetting(ctx, "fortnox_access_token")
	return err == nil && setting != nil && setting.Value != ""
}
