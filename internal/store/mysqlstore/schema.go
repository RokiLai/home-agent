// Package mysqlstore 实现基于 MySQL 8.0 / ProxySQL 的可插拔关系型数据库存储驱动
package mysqlstore

import (
	"context"
	"database/sql"
	"fmt"
)

var tableSchemas = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id VARCHAR(64) PRIMARY KEY,
		username VARCHAR(64) NOT NULL UNIQUE,
		username_key VARCHAR(64) NOT NULL UNIQUE,
		password_hash VARCHAR(255) NOT NULL,
		role VARCHAR(16) NOT NULL DEFAULT 'admin',
		status VARCHAR(16) NOT NULL DEFAULT 'active',
		session_version BIGINT UNSIGNED NOT NULL DEFAULT 1,
		created_by VARCHAR(64) DEFAULT '',
		created_at DATETIME(3) NOT NULL,
		updated_at DATETIME(3) NOT NULL,
		last_login_at DATETIME(3) NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,

	`CREATE TABLE IF NOT EXISTS sessions (
		token_hash VARCHAR(64) PRIMARY KEY,
		user_id VARCHAR(64) NOT NULL,
		username VARCHAR(64) NOT NULL,
		role VARCHAR(16) NOT NULL,
		issued_session_ver BIGINT UNSIGNED NOT NULL,
		expires_at DATETIME(3) NOT NULL,
		created_at DATETIME(3) NOT NULL,
		last_seen_at DATETIME(3) NOT NULL,
		remember_me TINYINT(1) NOT NULL DEFAULT 0,
		INDEX idx_sessions_user_id (user_id),
		INDEX idx_sessions_expires_at (expires_at),
		CONSTRAINT fk_sessions_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,

	`CREATE TABLE IF NOT EXISTS devices (
		id VARCHAR(64) PRIMARY KEY,
		owner_user_id VARCHAR(64) NOT NULL,
		hostname VARCHAR(128) NOT NULL,
		alias VARCHAR(128) DEFAULT '',
		os VARCHAR(32) DEFAULT '',
		arch VARCHAR(32) DEFAULT '',
		ssh_user VARCHAR(64) DEFAULT 'root',
		ssh_port INT NOT NULL DEFAULT 22,
		mac VARCHAR(32) DEFAULT '',
		public_key TEXT NOT NULL,
		addresses_json JSON NULL,
		agent_version VARCHAR(32) DEFAULT '',
		applied_hash VARCHAR(64) DEFAULT '',
		sync_status VARCHAR(32) DEFAULT 'pending',
		created_at DATETIME(3) NOT NULL,
		updated_at DATETIME(3) NOT NULL,
		INDEX idx_devices_owner (owner_user_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,

	`CREATE TABLE IF NOT EXISTS device_grants (
		device_id VARCHAR(64) NOT NULL,
		user_id VARCHAR(64) NOT NULL,
		level VARCHAR(16) NOT NULL DEFAULT 'read',
		granted_by VARCHAR(64) DEFAULT '',
		created_at DATETIME(3) NOT NULL,
		updated_at DATETIME(3) NOT NULL,
		PRIMARY KEY (device_id, user_id),
		INDEX idx_grants_user (user_id),
		CONSTRAINT fk_grants_device FOREIGN KEY (device_id) REFERENCES devices (id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,

	`CREATE TABLE IF NOT EXISTS audit_logs (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		actor_user_id VARCHAR(64) DEFAULT '',
		actor_role VARCHAR(16) DEFAULT '',
		action VARCHAR(64) NOT NULL,
		resource_type VARCHAR(32) NOT NULL,
		resource_id VARCHAR(128) NOT NULL,
		client_ip VARCHAR(45) DEFAULT '',
		status VARCHAR(16) NOT NULL,
		detail TEXT NULL,
		created_at DATETIME(3) NOT NULL,
		INDEX idx_audit_actor (actor_user_id),
		INDEX idx_audit_action (action),
		INDEX idx_audit_created (created_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,

	`CREATE TABLE IF NOT EXISTS claim_tokens (
		token_hash VARCHAR(64) PRIMARY KEY,
		owner_user_id VARCHAR(64) NOT NULL,
		created_by VARCHAR(64) NOT NULL,
		description VARCHAR(255) DEFAULT '',
		ttl_seconds INT NOT NULL,
		max_uses INT NOT NULL,
		used_count INT NOT NULL DEFAULT 0,
		expires_at DATETIME(3) NOT NULL,
		created_at DATETIME(3) NOT NULL,
		INDEX idx_claim_owner (owner_user_id),
		INDEX idx_claim_expires (expires_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,

	`CREATE TABLE IF NOT EXISTS commands (
		id VARCHAR(64) PRIMARY KEY,
		device_id VARCHAR(64) NOT NULL,
		kind VARCHAR(32) NOT NULL,
		state VARCHAR(32) NOT NULL,
		requested_by VARCHAR(64) NOT NULL,
		params_json JSON NULL,
		result_json JSON NULL,
		error_msg TEXT NULL,
		created_at DATETIME(3) NOT NULL,
		updated_at DATETIME(3) NOT NULL,
		expires_at DATETIME(3) NOT NULL,
		INDEX idx_commands_dev (device_id),
		INDEX idx_commands_state (state)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,

	`CREATE TABLE IF NOT EXISTS alert_rules (
		id VARCHAR(64) PRIMARY KEY,
		name VARCHAR(128) NOT NULL,
		type VARCHAR(32) NOT NULL,
		target VARCHAR(128) DEFAULT '',
		severity VARCHAR(16) NOT NULL,
		enabled TINYINT(1) NOT NULL DEFAULT 1,
		config_json JSON NULL,
		created_at DATETIME(3) NOT NULL,
		updated_at DATETIME(3) NOT NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,

	`CREATE TABLE IF NOT EXISTS alert_channels (
		id VARCHAR(64) PRIMARY KEY,
		type VARCHAR(32) NOT NULL,
		config_json JSON NULL,
		enabled TINYINT(1) NOT NULL DEFAULT 1,
		created_at DATETIME(3) NOT NULL,
		updated_at DATETIME(3) NOT NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,

	`CREATE TABLE IF NOT EXISTS alert_states (
		rule_id VARCHAR(64) NOT NULL,
		device_id VARCHAR(64) NOT NULL,
		state VARCHAR(32) NOT NULL,
		firing_since DATETIME(3) NOT NULL,
		last_eval_at DATETIME(3) NOT NULL,
		last_notified_at DATETIME(3) NULL,
		details_json JSON NULL,
		PRIMARY KEY (rule_id, device_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,

	`CREATE TABLE IF NOT EXISTS device_health (
		device_id VARCHAR(64) PRIMARY KEY,
		healthy TINYINT(1) NOT NULL DEFAULT 1,
		status VARCHAR(32) NOT NULL,
		summary TEXT NULL,
		facts_json JSON NULL,
		evaluated_at DATETIME(3) NOT NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,

	`CREATE TABLE IF NOT EXISTS github_sync_config (
		id VARCHAR(64) PRIMARY KEY,
		user_id VARCHAR(64) NOT NULL UNIQUE,
		oauth_token VARCHAR(255) DEFAULT '',
		account_login VARCHAR(128) DEFAULT '',
		account_id BIGINT DEFAULT 0,
		synced_keys_json JSON NULL,
		updated_at DATETIME(3) NOT NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,
}

// AutoMigrate 执行 MySQL 数据表结构自动化创建与更新
func AutoMigrate(ctx context.Context, db *sql.DB) error {
	for idx, ddl := range tableSchemas {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("auto migrate table %d: %w", idx, err)
		}
	}
	return nil
}
