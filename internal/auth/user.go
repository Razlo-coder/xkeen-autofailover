package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
	"xkeen-panel/internal/models"

	"golang.org/x/crypto/bcrypt"
)

type UserManager struct {
	dataDir string
	user    *models.User
	mu      sync.RWMutex
}

func NewUserManager(dataDir string) *UserManager {
	return &UserManager{dataDir: dataDir}
}

func (um *UserManager) userFilePath() string {
	return filepath.Join(um.dataDir, "user.json")
}

// Load reads the stored user.
func (um *UserManager) Load() error {
	um.mu.Lock()
	defer um.mu.Unlock()

	data, err := os.ReadFile(um.userFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var user models.User
	if err := json.Unmarshal(data, &user); err != nil {
		return err
	}
	// Older installations stored a TOTP secret in user.json. The account,
	// password hash, JWT key and passkeys stay intact while the unused secret
	// is removed from disk on the first start after upgrading.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, legacyTOTP := fields["totp_secret"]; legacyTOTP {
		delete(fields, "totp_secret")
		cleaned, err := json.MarshalIndent(fields, "", "  ")
		if err != nil {
			return err
		}
		if err := um.writeUserFile(cleaned); err != nil {
			return err
		}
	}
	um.user = &user
	return nil
}

// SetupRequired reports whether no account exists yet.
func (um *UserManager) SetupRequired() bool {
	um.mu.RLock()
	defer um.mu.RUnlock()
	return um.user == nil
}

// CreateUser persists the first account. An existing account cannot be replaced.
func (um *UserManager) CreateUser(username, password string) error {
	um.mu.Lock()
	defer um.mu.Unlock()
	if um.user != nil {
		return os.ErrExist
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	jwtSecret, err := generateRandomKey(32)
	if err != nil {
		return err
	}

	user := &models.User{
		Username:     username,
		PasswordHash: string(hash),
		JWTSecret:    jwtSecret,
		CreatedAt:    time.Now(),
	}
	data, err := json.MarshalIndent(user, "", "  ")
	if err != nil {
		return err
	}
	if err := um.writeUserFile(data); err != nil {
		return err
	}
	um.user = user
	return nil
}

// CheckPassword verifies the account password.
func (um *UserManager) CheckPassword(username, password string) bool {
	um.mu.RLock()
	defer um.mu.RUnlock()

	if um.user == nil || um.user.Username != username {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(um.user.PasswordHash), []byte(password)) == nil
}

// GetUser returns a copy of the account.
func (um *UserManager) GetUser() *models.User {
	um.mu.RLock()
	defer um.mu.RUnlock()
	if um.user == nil {
		return nil
	}
	u := *um.user
	return &u
}

// persistLocked writes the current account to disk. Call with um.mu held.
func (um *UserManager) persistLocked() error {
	if um.user == nil {
		return os.ErrNotExist
	}
	data, err := json.MarshalIndent(um.user, "", "  ")
	if err != nil {
		return err
	}
	return um.writeUserFile(data)
}

func (um *UserManager) writeUserFile(data []byte) error {
	if err := os.MkdirAll(um.dataDir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(um.dataDir, ".user-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), um.userFilePath())
}

func generateRandomKey(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
