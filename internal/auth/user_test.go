package auth

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestUsernameNormalizationAndValidation(t *testing.T) {
	// 1. Normalization
	cases := []struct {
		input    string
		expected string
	}{
		{"alice", "alice"},
		{"Alice", "alice"},
		{"  Bob_123  ", "bob_123"},
		{"USER.NAME@example.com", "user.name@example.com"},
	}

	for _, c := range cases {
		got := NormalizeUsernameKey(c.input)
		if got != c.expected {
			t.Fatalf("NormalizeUsernameKey(%q) = %q, want %q", c.input, got, c.expected)
		}
	}

	// 2. Validation
	validUsernames := []string{
		"alice",
		"bob_123",
		"user.name",
		"admin-01",
		"user@domain.com",
	}
	for _, u := range validUsernames {
		if err := ValidateUsernameFormat(u); err != nil {
			t.Fatalf("ValidateUsernameFormat(%q) expected valid, got error: %v", u, err)
		}
	}

	invalidUsernames := []string{
		"ab",                               // too short (<3)
		strings.Repeat("a", 33),           // too long (>32)
		"user name",                       // whitespace inside
		"user\nname",                      // control char
		"user$name",                       // special char $
		"<script>",                        // xss chars
		"../user",                         // traversal chars
	}
	for _, u := range invalidUsernames {
		if err := ValidateUsernameFormat(u); err == nil {
			t.Fatalf("ValidateUsernameFormat(%q) expected invalid, got nil", u)
		}
	}
}

func TestUserManager_MultiUserCRUDAndInvariants(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "multi_user_auth.json")

	sm, err := NewSessionManager(storePath)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	// 1. Bootstrap first owner
	bootstrapped, err := sm.InitAdminBootstrap("root_owner", "OwnerPassword123")
	if err != nil || !bootstrapped {
		t.Fatalf("InitAdminBootstrap failed: %v", err)
	}

	owner, err := sm.GetUserByUsername("root_owner")
	if err != nil || owner.Role != RoleOwner || owner.Status != UserStatusActive {
		t.Fatalf("Expected active owner, got: %+v, err: %v", owner, err)
	}

	// 2. Create second user (admin)
	adminUser, err := sm.CreateUser("admin_alice", "AdminPass123", RoleAdmin, owner.ID)
	if err != nil {
		t.Fatalf("CreateUser admin failed: %v", err)
	}
	if adminUser.Role != RoleAdmin {
		t.Fatalf("Expected role admin, got: %s", adminUser.Role)
	}

	// 3. Username conflict test (case-insensitive)
	if _, err := sm.CreateUser("Admin_Alice", "AnyPass123", RoleViewer, owner.ID); err != ErrUsernameConflict {
		t.Fatalf("Expected ErrUsernameConflict, got: %v", err)
	}

	// 4. Create third user (viewer)
	viewerUser, err := sm.CreateUser("viewer_bob", "ViewerPass123", RoleViewer, owner.ID)
	if err != nil {
		t.Fatalf("CreateUser viewer failed: %v", err)
	}

	// 5. Last Owner Invariant: demote owner must fail
	if err := sm.UpdateUserRole(owner.ID, RoleAdmin); err != ErrLastOwnerRequired {
		t.Fatalf("Expected ErrLastOwnerRequired on owner demotion, got: %v", err)
	}

	// 6. Last Owner Invariant: disable owner must fail
	if err := sm.DisableUser(owner.ID); err != ErrLastOwnerRequired {
		t.Fatalf("Expected ErrLastOwnerRequired on owner disable, got: %v", err)
	}

	// 7. Last Owner Invariant: delete owner must fail
	if err := sm.DeleteUser(owner.ID); err != ErrLastOwnerRequired {
		t.Fatalf("Expected ErrLastOwnerRequired on owner delete, got: %v", err)
	}

	// 8. Promote alice to owner -> now 2 owners exist
	if err := sm.UpdateUserRole(adminUser.ID, RoleOwner); err != nil {
		t.Fatalf("Promote admin to owner failed: %v", err)
	}
	if count := sm.CountActiveOwners(); count != 2 {
		t.Fatalf("Expected 2 active owners, got %d", count)
	}

	// 9. Now original owner can be demoted
	if err := sm.UpdateUserRole(owner.ID, RoleViewer); err != nil {
		t.Fatalf("Demoting original owner when 2 owners exist failed: %v", err)
	}
	if count := sm.CountActiveOwners(); count != 1 {
		t.Fatalf("Expected 1 active owner left, got %d", count)
	}

	// 10. Disable and Enable viewer_bob
	if err := sm.DisableUser(viewerUser.ID); err != nil {
		t.Fatalf("DisableUser failed: %v", err)
	}
	bobDisabled, _ := sm.GetUser(viewerUser.ID)
	if bobDisabled.Status != UserStatusDisabled || bobDisabled.DisabledAt == nil {
		t.Fatalf("Expected disabled bob, got: %+v", bobDisabled)
	}
	// Disabled user cannot authenticate
	if _, err := sm.AuthenticateUser("viewer_bob", "ViewerPass123"); err != ErrUnauthorized {
		t.Fatalf("Disabled user should fail auth with ErrUnauthorized, got: %v", err)
	}

	if err := sm.EnableUser(viewerUser.ID); err != nil {
		t.Fatalf("EnableUser failed: %v", err)
	}
	// Enabled user can authenticate
	if _, err := sm.AuthenticateUser("viewer_bob", "ViewerPass123"); err != nil {
		t.Fatalf("Enabled user should authenticate successfully: %v", err)
	}

	// 11. Delete viewer_bob
	if err := sm.DeleteUser(viewerUser.ID); err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}
	if _, err := sm.GetUser(viewerUser.ID); err != ErrUserNotFound {
		t.Fatalf("Deleted user should return ErrUserNotFound, got: %v", err)
	}

	// 12. List Users (password hashes must be stripped)
	users := sm.ListUsers()
	if len(users) != 2 {
		t.Fatalf("Expected 2 remaining users, got %d", len(users))
	}
	for _, u := range users {
		if u.PasswordHash != "" {
			t.Fatalf("ListUsers must not expose PasswordHash for user %s", u.Username)
		}
	}
}

func TestSessionVersionInvalidation(t *testing.T) {
	sm, _ := NewSessionManager("")
	_, _ = sm.InitAdminBootstrap("alice", "Password123")
	user, _ := sm.GetUserByUsername("alice")

	// 1. Create Session
	rawToken, _, err := sm.CreateUserSession(user.ID, false)
	if err != nil {
		t.Fatalf("CreateUserSession: %v", err)
	}

	// Session is valid
	sess, err := sm.ValidateSession(rawToken)
	if err != nil || sess.UserID != user.ID {
		t.Fatalf("ValidateSession: %v", err)
	}

	// 2. Change role -> increments SessionVersion -> Session immediately invalid
	_, _ = sm.CreateUser("bob_owner", "Password123", RoleOwner, user.ID)
	if err := sm.UpdateUserRole(user.ID, RoleAdmin); err != nil {
		t.Fatalf("UpdateUserRole: %v", err)
	}

	if _, err := sm.ValidateSession(rawToken); err == nil {
		t.Fatal("Session must be invalid after role change")
	}

	// 3. New Session after role change
	rawToken2, _, err := sm.CreateUserSession(user.ID, false)
	if err != nil {
		t.Fatalf("CreateUserSession 2: %v", err)
	}
	if _, err := sm.ValidateSession(rawToken2); err != nil {
		t.Fatalf("ValidateSession 2 failed: %v", err)
	}

	// 4. Reset Password -> increments SessionVersion -> Session immediately invalid
	if err := sm.ResetUserPassword(user.ID, "NewPassword456"); err != nil {
		t.Fatalf("ResetUserPassword: %v", err)
	}
	if _, err := sm.ValidateSession(rawToken2); err == nil {
		t.Fatal("Session must be invalid after password reset")
	}
}
