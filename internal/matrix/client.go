// Package matrix provides a Matrix client wrapper using mautrix-go.
// It handles connection, authentication, and session management.
package matrix

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"gopkg.in/yaml.v3"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/config"
)

// MatrixClient wraps mautrix.Client with session management capabilities.
// It stores configuration and provides methods for login, session persistence,
// and connection verification.
type MatrixClient struct {
	client *mautrix.Client
	config *config.MatrixConfig
}

// Session represents the persisted authentication state.
// WARNING: Never commit session files containing access tokens to version control.
type Session struct {
	UserID      string `yaml:"user_id"`
	DeviceID    string `yaml:"device_id"`
	AccessToken string `yaml:"access_token"`
	Homeserver  string `yaml:"homeserver"`
}

// NewMatrixClient creates a new Matrix client wrapper.
// It validates the configuration and initializes the client with either
// an existing access token or prepares for password-based login.
//
// If AccessToken is provided in config, it creates a client with that token.
// If only Password is provided, the client is created but requires Login() to be called.
func NewMatrixClient(cfg *config.MatrixConfig) (*MatrixClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("matrix config cannot be nil")
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid matrix configuration: %w", err)
	}

	userID := id.UserID(cfg.UserID)
	homeserver := cfg.Homeserver

	slog.Info("Creating Matrix client",
		"homeserver", homeserver,
		"user_id", userID.String(),
		"token_auth", cfg.UseTokenAuth())

	var client *mautrix.Client
	var err error

	if cfg.UseTokenAuth() {
		client, err = mautrix.NewClient(homeserver, userID, cfg.AccessToken)
		if err != nil {
			return nil, fmt.Errorf("failed to create Matrix client with token: %w", err)
		}

		if cfg.DeviceID != "" {
			client.DeviceID = id.DeviceID(cfg.DeviceID)
		}

		slog.Info("Matrix client created with access token",
			"user_id", userID.String(),
			"device_id", cfg.DeviceID)
	} else {
		client, err = mautrix.NewClient(homeserver, userID, "")
		if err != nil {
			return nil, fmt.Errorf("failed to create Matrix client: %w", err)
		}

		if cfg.DeviceID != "" {
			client.DeviceID = id.DeviceID(cfg.DeviceID)
		}

		slog.Info("Matrix client created, ready for password login",
			"user_id", userID.String(),
			"device_id", cfg.DeviceID)
	}

	return &MatrixClient{
		client: client,
		config: cfg,
	}, nil
}

// Login performs password-based authentication against the Matrix homeserver.
// This method should only be called when UsePasswordAuth() returns true.
// On successful login, the access token and device ID are stored in the client.
func (m *MatrixClient) Login(ctx context.Context) error {
	if !m.config.UsePasswordAuth() {
		return fmt.Errorf("password authentication not configured: either access token is set or password is empty")
	}

	userID := id.UserID(m.config.UserID)
	deviceName := m.config.DeviceName
	if deviceName == "" {
		deviceName = "Saber Bot"
	}

	slog.Info("Attempting Matrix password login",
		"user_id", userID.String(),
		"device_name", deviceName)

	loginReq := &mautrix.ReqLogin{
		Type: mautrix.AuthTypePassword,
		Identifier: mautrix.UserIdentifier{
			Type: mautrix.IdentifierTypeUser,
			User: string(userID),
		},
		Password:                 m.config.Password,
		DeviceID:                 id.DeviceID(m.config.DeviceID),
		InitialDeviceDisplayName: deviceName,
	}

	resp, err := m.client.Login(ctx, loginReq)
	if err != nil {
		return fmt.Errorf("failed to login to Matrix server: %w", err)
	}

	m.client.AccessToken = resp.AccessToken
	m.client.DeviceID = resp.DeviceID

	slog.Info("Matrix login successful",
		"user_id", resp.UserID.String(),
		"device_id", resp.DeviceID.String())

	return nil
}

// SaveSession persists the current session credentials to a YAML file.
// The session includes user ID, device ID, access token, and homeserver.
//
// WARNING: The session file contains sensitive credentials.
// Never commit session files to version control.
func (m *MatrixClient) SaveSession(path string) error {
	if m.client.AccessToken == "" {
		return fmt.Errorf("cannot save session: no access token available")
	}

	session := Session{
		UserID:      m.client.UserID.String(),
		DeviceID:    m.client.DeviceID.String(),
		AccessToken: m.client.AccessToken,
		Homeserver:  m.client.HomeserverURL.String(),
	}

	data, err := yaml.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to marshal session data: %w", err)
	}

	// Write with restricted permissions (0600 = owner read/write only)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("failed to write session file: %w", err)
	}

	slog.Info("Session saved successfully",
		"path", path,
		"user_id", session.UserID)

	return nil
}

// LoadSession restores session credentials from a YAML file.
// This allows reusing an existing authenticated session without re-login.
func (m *MatrixClient) LoadSession(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("session file not found: %s", path)
		}
		return fmt.Errorf("failed to read session file: %w", err)
	}

	var session Session
	if err := yaml.Unmarshal(data, &session); err != nil {
		return fmt.Errorf("failed to parse session file: %w", err)
	}

	if session.AccessToken == "" {
		return fmt.Errorf("session file contains no access token")
	}
	if session.UserID == "" {
		return fmt.Errorf("session file contains no user ID")
	}

	m.client.AccessToken = session.AccessToken
	m.client.DeviceID = id.DeviceID(session.DeviceID)
	m.client.UserID = id.UserID(session.UserID)

	slog.Info("Session loaded successfully",
		"path", path,
		"user_id", session.UserID,
		"device_id", session.DeviceID)

	return nil
}

// VerifyLogin checks if the current session is valid by querying the homeserver.
// This uses the Whoami API endpoint to verify the access token is still valid.
func (m *MatrixClient) VerifyLogin(ctx context.Context) error {
	if m.client.AccessToken == "" {
		return fmt.Errorf("cannot verify login: no access token set")
	}

	slog.Info("Verifying Matrix login status",
		"user_id", m.client.UserID.String())

	resp, err := m.client.Whoami(ctx)
	if err != nil {
		return fmt.Errorf("login verification failed: %w", err)
	}

	slog.Info("Matrix login verified successfully",
		"user_id", resp.UserID.String(),
		"device_id", resp.DeviceID.String())

	return nil
}

// GetClient returns the underlying mautrix.Client for advanced operations.
// Use this to access room operations, message sending, and other Matrix APIs.
func (m *MatrixClient) GetClient() *mautrix.Client {
	return m.client
}

// GetConfig returns the current configuration.
func (m *MatrixClient) GetConfig() *config.MatrixConfig {
	return m.config
}

// GetUserID returns the current authenticated user ID.
func (m *MatrixClient) GetUserID() id.UserID {
	return m.client.UserID
}

// GetDeviceID returns the current device ID.
func (m *MatrixClient) GetDeviceID() id.DeviceID {
	return m.client.DeviceID
}

// IsLoggedIn returns true if an access token is set.
func (m *MatrixClient) IsLoggedIn() bool {
	return m.client.AccessToken != ""
}
