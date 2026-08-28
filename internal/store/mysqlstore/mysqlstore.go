package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"homeagent/internal/auth"
	"homeagent/internal/device"
	"homeagent/internal/store"
)

// Config 定义 MySQL 连接池与配置
type Config struct {
	DSN             string        `json:"dsn" yaml:"dsn"`
	MaxOpenConns    int           `json:"max_open_conns" yaml:"max_open_conns"`
	MaxIdleConns    int           `json:"max_idle_conns" yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime" yaml:"conn_max_lifetime"`
}

// MySQLStore 实现 store.UserStore, store.SessionStore, store.DeviceStore, store.AuditStore
type MySQLStore struct {
	db *sql.DB
}

// NewMySQLStore 创建 MySQL 存储实例并自动执行建表
func NewMySQLStore(cfg Config) (*MySQLStore, error) {
	if cfg.DSN == "" {
		return nil, errors.New("mysql: DSN cannot be empty")
	}

	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("mysql open: %w", err)
	}

	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 25
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = 5
	}
	maxLife := cfg.ConnMaxLifetime
	if maxLife <= 0 {
		maxLife = 5 * time.Minute
	}

	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(maxLife)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mysql ping failed: %w", err)
	}

	if err := AutoMigrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mysql auto migrate failed: %w", err)
	}

	return &MySQLStore{db: db}, nil
}

// DB 返回底层 sql.DB 句柄
func (s *MySQLStore) DB() *sql.DB {
	return s.db
}

// Close 关闭数据库连接
func (s *MySQLStore) Close() error {
	return s.db.Close()
}

// ================= UserStore 实现 =================

func (s *MySQLStore) GetUser(id string) (*auth.User, error) {
	query := `SELECT id, username, username_key, password_hash, role, status, session_version, created_by, created_at, updated_at FROM users WHERE id = ? LIMIT 1`
	row := s.db.QueryRow(query, id)

	var u auth.User
	var createdBy sql.NullString
	err := row.Scan(&u.ID, &u.Username, &u.UsernameKey, &u.PasswordHash, &u.Role, &u.Status, &u.SessionVersion, &createdBy, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	u.CreatedBy = createdBy.String
	return &u, nil
}

func (s *MySQLStore) GetUserByUsernameKey(key string) (*auth.User, error) {
	query := `SELECT id, username, username_key, password_hash, role, status, session_version, created_by, created_at, updated_at FROM users WHERE username_key = ? LIMIT 1`
	row := s.db.QueryRow(query, key)

	var u auth.User
	var createdBy sql.NullString
	err := row.Scan(&u.ID, &u.Username, &u.UsernameKey, &u.PasswordHash, &u.Role, &u.Status, &u.SessionVersion, &createdBy, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	u.CreatedBy = createdBy.String
	return &u, nil
}

func (s *MySQLStore) ListUsers() ([]*auth.User, error) {
	query := `SELECT id, username, username_key, password_hash, role, status, session_version, created_by, created_at, updated_at FROM users ORDER BY created_at ASC`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*auth.User
	for rows.Next() {
		var u auth.User
		var createdBy sql.NullString
		if err := rows.Scan(&u.ID, &u.Username, &u.UsernameKey, &u.PasswordHash, &u.Role, &u.Status, &u.SessionVersion, &createdBy, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		u.CreatedBy = createdBy.String
		list = append(list, &u)
	}
	return list, nil
}

func (s *MySQLStore) SaveUser(user *auth.User) error {
	if user.ID == "" {
		user.ID = auth.GenerateUserID()
	}
	if user.UsernameKey == "" {
		user.UsernameKey = auth.NormalizeUsernameKey(user.Username)
	}
	if user.CreatedAt.IsZero() {
		user.CreatedAt = time.Now().UTC()
	}
	user.UpdatedAt = time.Now().UTC()

	query := `INSERT INTO users (id, username, username_key, password_hash, role, status, session_version, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
		username = VALUES(username),
		username_key = VALUES(username_key),
		password_hash = VALUES(password_hash),
		role = VALUES(role),
		status = VALUES(status),
		session_version = VALUES(session_version),
		created_by = VALUES(created_by),
		updated_at = VALUES(updated_at)`

	_, err := s.db.Exec(query, user.ID, user.Username, user.UsernameKey, user.PasswordHash, string(user.Role), string(user.Status), user.SessionVersion, user.CreatedBy, user.CreatedAt, user.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "Duplicate entry") {
			return store.ErrConflict
		}
		return err
	}
	return nil
}

func (s *MySQLStore) DeleteUser(id string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *MySQLStore) CountActiveOwners() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE status = 'active' AND role = 'owner'`).Scan(&count)
	return count, err
}

// ================= SessionStore 实现 =================

func (s *MySQLStore) GetSession(tokenHash string) (*auth.Session, error) {
	query := `SELECT token_hash, user_id, username, role, issued_session_ver, expires_at, created_at, last_seen_at, remember_me FROM sessions WHERE token_hash = ? LIMIT 1`
	row := s.db.QueryRow(query, tokenHash)

	var sess auth.Session
	var remMe int
	err := row.Scan(&sess.TokenHash, &sess.UserID, &sess.Username, &sess.Role, &sess.IssuedSessionVer, &sess.ExpiresAt, &sess.CreatedAt, &sess.LastSeenAt, &remMe)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	sess.RememberMe = remMe == 1
	if time.Now().UTC().After(sess.ExpiresAt) {
		return nil, store.ErrNotFound
	}
	return &sess, nil
}

func (s *MySQLStore) SaveSession(session *auth.Session) error {
	query := `INSERT INTO sessions (token_hash, user_id, username, role, issued_session_ver, expires_at, created_at, last_seen_at, remember_me)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
		expires_at = VALUES(expires_at),
		last_seen_at = VALUES(last_seen_at)`

	rem := 0
	if session.RememberMe {
		rem = 1
	}
	_, err := s.db.Exec(query, session.TokenHash, session.UserID, session.Username, session.Role, session.IssuedSessionVer, session.ExpiresAt, session.CreatedAt, session.LastSeenAt, rem)
	return err
}

func (s *MySQLStore) DeleteSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *MySQLStore) DeleteSessionsByUser(userID string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

func (s *MySQLStore) CleanExpired() error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().UTC())
	return err
}

// ================= DeviceStore 实现 =================

func (s *MySQLStore) GetDevice(id string) (*device.Device, error) {
	query := `SELECT id, owner_user_id, hostname, alias, os, arch, ssh_user, ssh_port, mac, public_key, addresses_json, agent_version, applied_hash, sync_status, created_at, updated_at FROM devices WHERE id = ? LIMIT 1`
	row := s.db.QueryRow(query, id)

	var d device.Device
	var addrJSON sql.NullString
	err := row.Scan(&d.ID, &d.OwnerUserID, &d.Hostname, &d.Alias, &d.OS, &d.Arch, &d.SSHUser, &d.SSHPort, &d.MAC, &d.PublicKey, &addrJSON, &d.AgentVersion, &d.AppliedHash, &d.SyncStatus, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	if addrJSON.Valid && addrJSON.String != "" {
		_ = json.Unmarshal([]byte(addrJSON.String), &d.Addresses)
	}
	return &d, nil
}

func (s *MySQLStore) ListDevices() ([]*device.Device, error) {
	query := `SELECT id, owner_user_id, hostname, alias, os, arch, ssh_user, ssh_port, mac, public_key, addresses_json, agent_version, applied_hash, sync_status, created_at, updated_at FROM devices ORDER BY created_at ASC`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*device.Device
	for rows.Next() {
		var d device.Device
		var addrJSON sql.NullString
		if err := rows.Scan(&d.ID, &d.OwnerUserID, &d.Hostname, &d.Alias, &d.OS, &d.Arch, &d.SSHUser, &d.SSHPort, &d.MAC, &d.PublicKey, &addrJSON, &d.AgentVersion, &d.AppliedHash, &d.SyncStatus, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		if addrJSON.Valid && addrJSON.String != "" {
			_ = json.Unmarshal([]byte(addrJSON.String), &d.Addresses)
		}
		list = append(list, &d)
	}
	return list, nil
}

func (s *MySQLStore) SaveDevice(dev *device.Device) error {
	addrBytes, _ := json.Marshal(dev.Addresses)
	query := `INSERT INTO devices (id, owner_user_id, hostname, alias, os, arch, ssh_user, ssh_port, mac, public_key, addresses_json, agent_version, applied_hash, sync_status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
		owner_user_id = VALUES(owner_user_id),
		hostname = VALUES(hostname),
		alias = VALUES(alias),
		os = VALUES(os),
		arch = VALUES(arch),
		ssh_user = VALUES(ssh_user),
		ssh_port = VALUES(ssh_port),
		mac = VALUES(mac),
		public_key = VALUES(public_key),
		addresses_json = VALUES(addresses_json),
		agent_version = VALUES(agent_version),
		applied_hash = VALUES(applied_hash),
		sync_status = VALUES(sync_status),
		updated_at = VALUES(updated_at)`

	if dev.CreatedAt.IsZero() {
		dev.CreatedAt = time.Now().UTC()
	}
	dev.UpdatedAt = time.Now().UTC()

	_, err := s.db.Exec(query, dev.ID, dev.OwnerUserID, dev.Hostname, dev.Alias, dev.OS, dev.Arch, dev.SSHUser, dev.SSHPort, dev.MAC, dev.PublicKey, string(addrBytes), dev.AgentVersion, dev.AppliedHash, dev.SyncStatus, dev.CreatedAt, dev.UpdatedAt)
	return err
}

func (s *MySQLStore) DeleteDevice(id string) error {
	res, err := s.db.Exec(`DELETE FROM devices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *MySQLStore) DeleteDevicesByOwner(ownerUserID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM devices WHERE owner_user_id = ?`, ownerUserID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	if len(ids) > 0 {
		_, err = s.db.Exec(`DELETE FROM devices WHERE owner_user_id = ?`, ownerUserID)
		if err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// Grants
func (s *MySQLStore) ListGrants(deviceID string) ([]*device.DeviceGrant, error) {
	query := `SELECT device_id, user_id, level, granted_by, created_at, updated_at FROM device_grants WHERE device_id = ?`
	rows, err := s.db.Query(query, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*device.DeviceGrant
	for rows.Next() {
		var g device.DeviceGrant
		if err := rows.Scan(&g.DeviceID, &g.UserID, &g.Level, &g.GrantedBy, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, &g)
	}
	return list, nil
}

func (s *MySQLStore) GetGrant(deviceID, userID string) (*device.DeviceGrant, error) {
	query := `SELECT device_id, user_id, level, granted_by, created_at, updated_at FROM device_grants WHERE device_id = ? AND user_id = ? LIMIT 1`
	row := s.db.QueryRow(query, deviceID, userID)

	var g device.DeviceGrant
	if err := row.Scan(&g.DeviceID, &g.UserID, &g.Level, &g.GrantedBy, &g.CreatedAt, &g.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &g, nil
}

func (s *MySQLStore) SaveGrant(grant *device.DeviceGrant) error {
	query := `INSERT INTO device_grants (device_id, user_id, level, granted_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
		level = VALUES(level),
		granted_by = VALUES(granted_by),
		updated_at = VALUES(updated_at)`

	if grant.CreatedAt.IsZero() {
		grant.CreatedAt = time.Now().UTC()
	}
	grant.UpdatedAt = time.Now().UTC()

	_, err := s.db.Exec(query, grant.DeviceID, grant.UserID, string(grant.Level), grant.GrantedBy, grant.CreatedAt, grant.UpdatedAt)
	return err
}

func (s *MySQLStore) DeleteGrant(deviceID, userID string) error {
	_, err := s.db.Exec(`DELETE FROM device_grants WHERE device_id = ? AND user_id = ?`, deviceID, userID)
	return err
}

func (s *MySQLStore) DeleteGrantsByDevice(deviceID string) error {
	_, err := s.db.Exec(`DELETE FROM device_grants WHERE device_id = ?`, deviceID)
	return err
}

func (s *MySQLStore) DeleteGrantsByUser(userID string) error {
	_, err := s.db.Exec(`DELETE FROM device_grants WHERE user_id = ?`, userID)
	return err
}

// ================= EnrollmentStore 实现 =================

func (s *MySQLStore) GetClaimToken(tokenHash string) (*auth.ClaimToken, error) {
	query := `SELECT token_hash, owner_user_id, created_by, description, ttl_seconds, max_uses, used_count, expires_at, created_at FROM claim_tokens WHERE token_hash = ? LIMIT 1`
	row := s.db.QueryRow(query, tokenHash)

	var tok auth.ClaimToken
	var desc sql.NullString
	var ttlSec, maxUses, usedCount int
	err := row.Scan(&tok.TokenHash, &tok.OwnerUserID, &tok.CreatedByUserID, &desc, &ttlSec, &maxUses, &usedCount, &tok.ExpiresAt, &tok.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	tok.Description = desc.String
	tok.MaxUses = maxUses
	tok.RemainingUses = maxUses - usedCount
	if tok.RemainingUses < 0 {
		tok.RemainingUses = 0
	}
	return &tok, nil
}

func (s *MySQLStore) ListClaimTokens(ownerUserID string) ([]*auth.ClaimToken, error) {
	var query string
	var args []any
	if ownerUserID != "" {
		query = `SELECT token_hash, owner_user_id, created_by, description, ttl_seconds, max_uses, used_count, expires_at, created_at FROM claim_tokens WHERE owner_user_id = ? ORDER BY created_at DESC`
		args = append(args, ownerUserID)
	} else {
		query = `SELECT token_hash, owner_user_id, created_by, description, ttl_seconds, max_uses, used_count, expires_at, created_at FROM claim_tokens ORDER BY created_at DESC`
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*auth.ClaimToken
	for rows.Next() {
		var tok auth.ClaimToken
		var desc sql.NullString
		var ttlSec, maxUses, usedCount int
		if err := rows.Scan(&tok.TokenHash, &tok.OwnerUserID, &tok.CreatedByUserID, &desc, &ttlSec, &maxUses, &usedCount, &tok.ExpiresAt, &tok.CreatedAt); err != nil {
			return nil, err
		}
		tok.Description = desc.String
		tok.MaxUses = maxUses
		tok.RemainingUses = maxUses - usedCount
		if tok.RemainingUses < 0 {
			tok.RemainingUses = 0
		}
		list = append(list, &tok)
	}
	return list, nil
}

func (s *MySQLStore) SaveClaimToken(token *auth.ClaimToken) error {
	query := `INSERT INTO claim_tokens (token_hash, owner_user_id, created_by, description, ttl_seconds, max_uses, used_count, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
		description = VALUES(description),
		used_count = VALUES(used_count),
		expires_at = VALUES(expires_at)`

	usedCount := token.MaxUses - token.RemainingUses
	if usedCount < 0 {
		usedCount = 0
	}
	ttlSec := int(token.ExpiresAt.Sub(token.CreatedAt).Seconds())
	if ttlSec <= 0 {
		ttlSec = 900
	}

	_, err := s.db.Exec(query, token.TokenHash, token.OwnerUserID, token.CreatedByUserID, token.Description, ttlSec, token.MaxUses, usedCount, token.ExpiresAt, token.CreatedAt)
	return err
}

func (s *MySQLStore) DeleteClaimToken(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM claim_tokens WHERE token_hash = ?`, tokenHash)
	return err
}

// ================= AuditStore 实现 =================

func (s *MySQLStore) Record(event auth.AuditEvent) error {
	query := `INSERT INTO audit_logs (actor_user_id, actor_role, action, resource_type, resource_id, client_ip, status, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	_, err := s.db.Exec(query, event.ActorUserID, string(event.ActorRole), event.Action, event.ResourceType, event.ResourceID, event.ClientIP, event.Status, event.Detail, event.Timestamp)
	return err
}

func (s *MySQLStore) Recent(limit int) ([]auth.AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT actor_user_id, actor_role, action, resource_type, resource_id, client_ip, status, detail, created_at FROM audit_logs ORDER BY created_at DESC LIMIT ?`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []auth.AuditEvent
	for rows.Next() {
		var e auth.AuditEvent
		var actorRole, action string
		var detail sql.NullString
		if err := rows.Scan(&e.ActorUserID, &actorRole, &action, &e.ResourceType, &e.ResourceID, &e.ClientIP, &e.Status, &detail, &e.Timestamp); err != nil {
			return nil, err
		}
		e.ActorRole = auth.Role(actorRole)
		e.Action = action
		e.Detail = detail.String
		events = append(events, e)
	}
	return events, nil
}

type mysqlAuditLoggerAdapter struct {
	store *MySQLStore
}

func (a *mysqlAuditLoggerAdapter) Record(event auth.AuditEvent) {
	_ = a.store.Record(event)
}

func (a *mysqlAuditLoggerAdapter) Recent(limit int) []auth.AuditEvent {
	res, err := a.store.Recent(limit)
	if err != nil {
		return []auth.AuditEvent{}
	}
	return res
}

// AsAuditLogger 将 MySQLStore 转换为 auth.AuditLogger 接口实现
func (s *MySQLStore) AsAuditLogger() auth.AuditLogger {
	return &mysqlAuditLoggerAdapter{store: s}
}
