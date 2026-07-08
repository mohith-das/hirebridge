package auth

import (
	"database/sql"
	"fmt"
	"time"

	"hirebridge/internal/store/repo"
)

type Service struct {
	DB       *sql.DB
	Mailer   Mailer
	BaseURL  string
	MagicTTL time.Duration
}

func NewService(db *sql.DB, mailer Mailer, baseURL string, magicTTL time.Duration) *Service {
	return &Service{
		DB:       db,
		Mailer:   mailer,
		BaseURL:  baseURL,
		MagicTTL: magicTTL,
	}
}

func (s *Service) RequestMagicLink(email, userCode string) error {
	deviceCodeHash, err := s.resolveDeviceUserCode(userCode)
	if err != nil {
		return fmt.Errorf("resolve device: %w", err)
	}

	userID, err := repo.CreateUser(s.DB, email)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}

	token := repo.GenerateToken()
	if err := repo.InsertMagicToken(s.DB, token, &userID, deviceCodeHash, s.MagicTTL); err != nil {
		return fmt.Errorf("insert magic token: %w", err)
	}

	link := fmt.Sprintf("%s/auth/callback?token=%s", s.BaseURL, token)
	return s.Mailer.SendMagicLink(email, link)
}

func (s *Service) VerifyMagicCallback(token string) (*CallbackResult, error) {
	mt, consumed, err := repo.ConsumeMagicToken(s.DB, token)
	if err != nil {
		return nil, fmt.Errorf("consume token: %w", err)
	}
	if !consumed || mt == nil {
		return nil, nil
	}

	if !mt.UserID.Valid {
		return &CallbackResult{PendingDevice: mt.DeviceCodeHash.Valid}, nil
	}

	userID := mt.UserID.String

	if mt.DeviceCodeHash.Valid {
		return s.approveDevice(mt.DeviceCodeHash.String, userID)
	}

	rawToken, _, err := repo.CreateAPIToken(s.DB, userID, nil, "magic-link", "all")
	if err != nil {
		return nil, fmt.Errorf("create user token: %w", err)
	}

	repo.InsertAuditLog(s.DB, userID, "login", "magic-link")

	return &CallbackResult{AccessToken: rawToken, UserID: userID}, nil
}

func (s *Service) approveDevice(codeHash, userID string) (*CallbackResult, error) {
	if err := repo.ApproveDeviceSession(s.DB, codeHash, userID); err != nil {
		return nil, fmt.Errorf("approve device session: %w", err)
	}

	rawToken, _, err := repo.CreateAPIToken(s.DB, userID, nil, "magic-link", "all")
	if err != nil {
		return nil, fmt.Errorf("create user token: %w", err)
	}

	ds, err := repo.DeviceSessionByCodeHash(s.DB, codeHash)
	if err != nil || ds == nil || !ds.NodeID.Valid {
		return &CallbackResult{AccessToken: rawToken, UserID: userID, DeviceApproved: true}, nil
	}

	rawNodeToken, nt, err := repo.CreateAPIToken(s.DB, userID, &ds.NodeID.String, "device-node", "node:push")
	if err != nil {
		return nil, fmt.Errorf("create node token: %w", err)
	}
	if err := repo.UpdateNodeToken(s.DB, ds.NodeID.String, nt.TokenHash); err != nil {
		return nil, fmt.Errorf("update node token: %w", err)
	}
	if err := repo.SetNodeUser(s.DB, ds.NodeID.String, userID); err != nil {
		return nil, fmt.Errorf("set node user: %w", err)
	}

	repo.InsertAuditLog(s.DB, userID, "device_approved", ds.NodeID.String)

	return &CallbackResult{
		AccessToken:    rawToken,
		NodeToken:      rawNodeToken,
		NodeID:         ds.NodeID.String,
		UserID:         userID,
		DeviceApproved: true,
	}, nil
}

func (s *Service) InitiateDeviceFlow(nodeType, endpointURL string) (*DeviceInitResponse, error) {
	var nt, ep sql.NullString
	if nodeType != "" {
		nt = sql.NullString{String: nodeType, Valid: true}
	}
	if endpointURL != "" {
		ep = sql.NullString{String: endpointURL, Valid: true}
	}

	deviceCode, userCode, err := repo.InsertDeviceSession(s.DB, nt, ep, s.MagicTTL)
	if err != nil {
		return nil, fmt.Errorf("insert device session: %w", err)
	}

	return &DeviceInitResponse{
		DeviceCode:              deviceCode,
		UserCode:                userCode,
		VerificationURI:         s.BaseURL + "/device",
		VerificationURIComplete: s.BaseURL + "/device?uc=" + userCode,
		ExpiresIn:               int(s.MagicTTL.Seconds()),
		Interval:                5,
	}, nil
}

func (s *Service) PollToken(deviceCode string) (*TokenPollResponse, error) {
	codeHash := repo.HashToken(deviceCode)
	ds, err := repo.DeviceSessionByCodeHash(s.DB, codeHash)
	if err != nil {
		return nil, fmt.Errorf("lookup device session: %w", err)
	}
	if ds == nil {
		return &TokenPollResponse{Error: "expired_token"}, nil
	}

	now := time.Now().Unix()
	switch ds.Status {
	case "pending":
		return s.handlePollPending(ds, codeHash, now)
	case "approved":
		return s.handlePollApproved(ds, codeHash)
	case "denied":
		return &TokenPollResponse{Error: "access_denied"}, nil
	default:
		return &TokenPollResponse{Error: "expired_token"}, nil
	}
}

func (s *Service) handlePollPending(ds *repo.DeviceSession, codeHash string, now int64) (*TokenPollResponse, error) {
	if ds.ExpiresAt < now {
		return &TokenPollResponse{Error: "expired_token"}, nil
	}
	interval := ds.PollInterval
	if ds.LastPollAt.Valid && now-ds.LastPollAt.Int64 < int64(interval) {
		return &TokenPollResponse{Error: "slow_down", Interval: interval + 5}, nil
	}
	if err := repo.UpdateLastPoll(s.DB, codeHash); err != nil {
		return nil, fmt.Errorf("update last poll: %w", err)
	}
	return &TokenPollResponse{Error: "authorization_pending", Interval: interval}, nil
}

func (s *Service) handlePollApproved(ds *repo.DeviceSession, codeHash string) (*TokenPollResponse, error) {
	consumed, err := repo.ConsumeDeviceSession(s.DB, codeHash)
	if err != nil {
		return nil, fmt.Errorf("consume device session: %w", err)
	}
	if consumed == nil || !consumed.UserID.Valid {
		return &TokenPollResponse{Error: "expired_token"}, nil
	}

	userID := consumed.UserID.String

	_, _, err = repo.CreateAPIToken(s.DB, userID, nil, "device-flow", "all")
	if err != nil {
		return nil, fmt.Errorf("create user api token: %w", err)
	}

	var rawNodeToken string
	var nt *repo.APIToken
	if consumed.NodeID.Valid {
		rawNodeToken, nt, err = repo.CreateAPIToken(s.DB, userID, &consumed.NodeID.String, "device-node", "node:push")
		if err != nil {
			return nil, fmt.Errorf("create node token: %w", err)
		}
		if err := repo.UpdateNodeToken(s.DB, consumed.NodeID.String, nt.TokenHash); err != nil {
			return nil, fmt.Errorf("update node token: %w", err)
		}
		if err := repo.SetNodeUser(s.DB, consumed.NodeID.String, userID); err != nil {
			return nil, fmt.Errorf("set node user: %w", err)
		}
	}

	return &TokenPollResponse{
		AccessToken: rawNodeToken,
		NodeID:      consumed.NodeID.String,
		TokenType:   "Bearer",
		Scope:       "node:push",
	}, nil
}

func (s *Service) resolveDeviceUserCode(userCode string) (*string, error) {
	if userCode == "" {
		return nil, nil
	}
	ds, err := repo.DeviceSessionByUserCode(s.DB, userCode)
	if err != nil {
		return nil, err
	}
	if ds == nil {
		return nil, nil
	}
	return &ds.DeviceCodeHash, nil
}

type CallbackResult struct {
	AccessToken    string
	NodeToken      string
	NodeID         string
	UserID         string
	DeviceApproved bool
	PendingDevice  bool
}

type DeviceInitResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type TokenPollResponse struct {
	AccessToken string `json:"access_token,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	TokenType   string `json:"token_type,omitempty"`
	ExpiresIn   int    `json:"expires_in,omitempty"`
	Scope       string `json:"scope,omitempty"`
	Error       string `json:"error,omitempty"`
	Interval    int    `json:"interval,omitempty"`
}
