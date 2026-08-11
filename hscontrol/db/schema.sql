-- This file is the representation of the SQLite schema of Headscale.
-- It is the "source of truth" and is used to validate any migrations
-- that are run against the database to ensure it ends in the expected state.

CREATE TABLE migrations(id text,PRIMARY KEY(id));

CREATE TABLE users(
  id integer PRIMARY KEY AUTOINCREMENT,
  name text,
  display_name text,
  email text,
  provider_identifier text,
  provider text,
  profile_pic_url text,

  created_at datetime,
  updated_at datetime,
  deleted_at datetime
);
CREATE INDEX idx_users_deleted_at ON users(deleted_at);


-- The following three UNIQUE indexes work together to enforce the user identity model:
--
-- 1. Users can be either local (provider_identifier is NULL) or from external providers (provider_identifier set)
-- 2. Each external provider identifier must be unique across the system
-- 3. Local usernames must be unique among local users
-- 4. The same username can exist across different providers with different identifiers
--
-- Examples:
-- - Can create local user "alice" (provider_identifier=NULL)
-- - Can create external user "alice" with GitHub (name="alice", provider_identifier="alice_github")
-- - Can create external user "alice" with Google (name="alice", provider_identifier="alice_google")
-- - Cannot create another local user "alice" (blocked by idx_name_no_provider_identifier)
-- - Cannot create another user with provider_identifier="alice_github" (blocked by idx_provider_identifier)
-- - Cannot create user "bob" with provider_identifier="alice_github" (blocked by idx_name_provider_identifier)
CREATE UNIQUE INDEX idx_provider_identifier ON users(provider_identifier) WHERE provider_identifier IS NOT NULL;
CREATE UNIQUE INDEX idx_name_provider_identifier ON users(name, provider_identifier);
CREATE UNIQUE INDEX idx_name_no_provider_identifier ON users(name) WHERE provider_identifier IS NULL;

CREATE TABLE account_groups(
  id integer PRIMARY KEY AUTOINCREMENT,
  name text NOT NULL,

  created_at datetime,
  updated_at datetime,
  deleted_at datetime
);
CREATE INDEX idx_account_groups_deleted_at ON account_groups(deleted_at);
CREATE UNIQUE INDEX idx_account_groups_name ON account_groups(name);
CREATE UNIQUE INDEX idx_account_groups_name_lower ON account_groups(LOWER(name)) WHERE deleted_at IS NULL;

CREATE TABLE accounts(
  id integer PRIMARY KEY AUTOINCREMENT,
  username text NOT NULL,
  password_hash text NOT NULL,
  user_id integer,
  group_id integer,
  role text NOT NULL DEFAULT "user",
  enabled numeric NOT NULL DEFAULT true,
  expires_at datetime,
  password_changed_at datetime NOT NULL,
  must_change_password numeric NOT NULL DEFAULT false,
  password_version integer NOT NULL DEFAULT 1,
  last_login_at datetime,

  created_at datetime,
  updated_at datetime,
  deleted_at datetime,

  CONSTRAINT fk_accounts_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE SET NULL ON UPDATE CASCADE,
  CONSTRAINT fk_accounts_group FOREIGN KEY(group_id) REFERENCES account_groups(id) ON DELETE RESTRICT ON UPDATE CASCADE
);
CREATE INDEX idx_accounts_deleted_at ON accounts(deleted_at);
CREATE UNIQUE INDEX idx_accounts_username ON accounts(username);
CREATE UNIQUE INDEX idx_accounts_user_id ON accounts(user_id);
CREATE INDEX idx_accounts_group_id ON accounts(group_id);

CREATE TABLE account_sessions(
  id integer PRIMARY KEY AUTOINCREMENT,
  token_hash blob NOT NULL,
  account_id integer NOT NULL,
  password_version integer NOT NULL,
  restricted numeric NOT NULL DEFAULT false,
  expires_at datetime NOT NULL,
  last_seen_at datetime NOT NULL,
  created_at datetime,
  revoked_at datetime,

  CONSTRAINT fk_account_sessions_account FOREIGN KEY(account_id) REFERENCES accounts(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_account_sessions_token_hash ON account_sessions(token_hash);
CREATE INDEX idx_account_sessions_account_id ON account_sessions(account_id);
CREATE INDEX idx_account_sessions_expires_at ON account_sessions(expires_at);

CREATE TABLE account_password_histories(
  id integer PRIMARY KEY AUTOINCREMENT,
  account_id integer NOT NULL,
  password_hash text NOT NULL,
  created_at datetime NOT NULL,

  CONSTRAINT fk_account_password_histories_account FOREIGN KEY(account_id) REFERENCES accounts(id) ON DELETE CASCADE
);
CREATE INDEX idx_account_password_histories_account_id ON account_password_histories(account_id);

CREATE TABLE pre_auth_keys(
  id integer PRIMARY KEY AUTOINCREMENT,
  key text,
  prefix text,
  hash blob,
  user_id integer,
  reusable numeric,
  ephemeral numeric DEFAULT false,
  used numeric DEFAULT false,
  tags text,
  expiration datetime,

  created_at datetime,

  CONSTRAINT fk_pre_auth_keys_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX idx_pre_auth_keys_prefix ON pre_auth_keys(prefix) WHERE prefix IS NOT NULL AND prefix != '';

CREATE TABLE api_keys(
  id integer PRIMARY KEY AUTOINCREMENT,
  prefix text,
  hash blob,
  expiration datetime,
  last_seen datetime,

  created_at datetime
);
CREATE UNIQUE INDEX idx_api_keys_prefix ON api_keys(prefix);

CREATE TABLE nodes(
  id integer PRIMARY KEY AUTOINCREMENT,
  machine_key text,
  node_key text,
  disco_key text,

  endpoints text,
  host_info text,
  ipv4 text,
  ipv6 text,
  hostname text,
  given_name varchar(63),
  -- user_id is NULL for tagged nodes (owned by tags, not a user).
  -- Only set for user-owned nodes (no tags).
  user_id integer,
  register_method text,
  tags text,
  auth_key_id integer,
  last_seen datetime,
  expiry datetime,
  approved_routes text,

  created_at datetime,
  updated_at datetime,
  deleted_at datetime,

  CONSTRAINT fk_nodes_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
  CONSTRAINT fk_nodes_auth_key FOREIGN KEY(auth_key_id) REFERENCES pre_auth_keys(id)
);
CREATE TABLE policies(
  id integer PRIMARY KEY AUTOINCREMENT,
  data text,

  created_at datetime,
  updated_at datetime,
  deleted_at datetime
);
CREATE INDEX idx_policies_deleted_at ON policies(deleted_at);

CREATE TABLE database_versions(
  id integer PRIMARY KEY,
  version text NOT NULL,
  updated_at datetime
);
CREATE TABLE runtime_settings(
  key text PRIMARY KEY,
  value text NOT NULL,
  updated_at datetime NOT NULL
);
