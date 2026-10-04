package auth

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSetupFlow(t *testing.T) {
	dir := t.TempDir()
	um := NewUserManager(dir)
	if err := um.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !um.SetupRequired() {
		t.Fatal("ожидался SetupRequired до создания пользователя")
	}

	if err := um.CreateUser("bob", "password123"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if um.SetupRequired() {
		t.Error("после создания SetupRequired должен быть false")
	}
	if err := um.CreateUser("alice", "another-password"); !os.IsExist(err) {
		t.Fatalf("повторное создание не должно заменять пользователя: %v", err)
	}
	reloaded := NewUserManager(dir)
	if err := reloaded.Load(); err != nil || !reloaded.CheckPassword("bob", "password123") {
		t.Fatalf("учётная запись не сохранилась: %v", err)
	}
}

func TestLoadRemovesLegacyTOTPWithoutChangingAccount(t *testing.T) {
	dir := t.TempDir()
	um := newConfirmedUserIn(t, dir)
	original := um.GetUser()
	path := filepath.Join(dir, "user.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["totp_secret"] = json.RawMessage(`"LEGACY-SECRET"`)
	fields["future_field"] = json.RawMessage(`"preserved"`)
	fields["webauthn_id"] = json.RawMessage(`"AQID"`)
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	reloaded := NewUserManager(dir)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if !reloaded.CheckPassword("bob", "password123") || reloaded.GetUser().JWTSecret != original.JWTSecret {
		t.Fatal("migration changed account credentials")
	}
	if !bytes.Equal(reloaded.GetUser().WebAuthnID, []byte{1, 2, 3}) {
		t.Fatal("migration changed passkey user ID")
	}
	cleaned, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cleaned, []byte("totp_secret")) || !bytes.Contains(cleaned, []byte("future_field")) {
		t.Fatalf("unexpected migrated account: %s", cleaned)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatalf("account file missing: %v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("account file permissions: %v", info.Mode().Perm())
	}
}

func TestCheckPassword(t *testing.T) {
	um := newConfirmedUser(t)

	if !um.CheckPassword("bob", "password123") {
		t.Error("верный пароль не прошёл")
	}
	if um.CheckPassword("bob", "wrong") {
		t.Error("неверный пароль прошёл")
	}
	if um.CheckPassword("alice", "password123") {
		t.Error("неверный логин прошёл")
	}
}

func newConfirmedUser(t *testing.T) *UserManager {
	return newConfirmedUserIn(t, t.TempDir())
}

func newConfirmedUserIn(t *testing.T, dir string) *UserManager {
	t.Helper()
	um := NewUserManager(dir)
	if err := um.CreateUser("bob", "password123"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return um
}
