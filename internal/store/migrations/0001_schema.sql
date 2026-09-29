-- +goose Up
-- hr module schema (data-model.md). Every table carries tenant_id and is put
-- under row-level security by 0003_rls.sql. Days are numeric(6,1) (half days).
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE hr_pools (
  id             uuid PRIMARY KEY,
  tenant_id      uuid NOT NULL,
  name           text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  description    text NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
  color          text NOT NULL DEFAULT '' CHECK (color = '' OR color ~ '^#[0-9a-fA-F]{6}$'),
  icon           text NOT NULL DEFAULT '' CHECK (char_length(icon) <= 64),
  carry_over_cap numeric(6,1) CHECK (carry_over_cap IS NULL OR carry_over_cap BETWEEN 0 AND 365),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     text NOT NULL DEFAULT '',
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     text NOT NULL DEFAULT '',
  UNIQUE (tenant_id, id)
);
CREATE UNIQUE INDEX hr_pools_name ON hr_pools (tenant_id, lower(name));

CREATE TABLE hr_absence_types (
  id                uuid PRIMARY KEY,
  tenant_id         uuid NOT NULL,
  name              text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  description       text NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
  color             text NOT NULL DEFAULT '' CHECK (color = '' OR color ~ '^#[0-9a-fA-F]{6}$'),
  icon              text NOT NULL DEFAULT '' CHECK (char_length(icon) <= 64),
  sort_order        int NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN 0 AND 10000),
  active            boolean NOT NULL DEFAULT true,
  metadata          jsonb NOT NULL DEFAULT '{}',
  deducts           boolean NOT NULL DEFAULT false,
  requires_approval boolean NOT NULL DEFAULT true,
  pool_id           uuid,
  carry_over_cap    numeric(6,1) CHECK (carry_over_cap IS NULL OR carry_over_cap BETWEEN 0 AND 365),
  requires_signing  boolean NOT NULL DEFAULT false,
  signing           jsonb,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        text NOT NULL DEFAULT '',
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        text NOT NULL DEFAULT '',
  CHECK (pool_id IS NULL OR deducts),
  CHECK (NOT requires_signing OR (requires_approval AND signing IS NOT NULL)),
  UNIQUE (tenant_id, id),
  -- Tenant-qualified keys: a foreign key check ignores row-level security, so a
  -- plain (pool_id) reference could point at another tenant's pool.
  FOREIGN KEY (tenant_id, pool_id) REFERENCES hr_pools (tenant_id, id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX hr_absence_types_name ON hr_absence_types (tenant_id, lower(name));
CREATE INDEX hr_absence_types_pool ON hr_absence_types (pool_id);

CREATE TABLE hr_allowances (
  id               uuid PRIMARY KEY,
  tenant_id        uuid NOT NULL,
  user_id          text NOT NULL CHECK (char_length(user_id) BETWEEN 1 AND 128),
  year             int NOT NULL CHECK (year BETWEEN 2000 AND 2099),
  absence_type_id  uuid,
  pool_id          uuid,
  total_days       numeric(6,1) NOT NULL CHECK (total_days BETWEEN 0 AND 365),
  carried_over     numeric(6,1) NOT NULL DEFAULT 0 CHECK (carried_over BETWEEN 0 AND 365),
  used_days        numeric(6,1) NOT NULL DEFAULT 0 CHECK (used_days >= 0),
  carried_from_run uuid,
  notes            text NOT NULL DEFAULT '' CHECK (char_length(notes) <= 2000),
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       text NOT NULL DEFAULT '',
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       text NOT NULL DEFAULT '',
  CHECK ((absence_type_id IS NULL) <> (pool_id IS NULL)),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, absence_type_id) REFERENCES hr_absence_types (tenant_id, id) ON DELETE RESTRICT,
  FOREIGN KEY (tenant_id, pool_id) REFERENCES hr_pools (tenant_id, id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX hr_allowances_type ON hr_allowances (tenant_id, user_id, year, absence_type_id) WHERE absence_type_id IS NOT NULL;
CREATE UNIQUE INDEX hr_allowances_pool ON hr_allowances (tenant_id, user_id, year, pool_id) WHERE pool_id IS NOT NULL;
CREATE INDEX hr_allowances_list ON hr_allowances (tenant_id, year, user_id);

CREATE TABLE hr_requests (
  id                 uuid PRIMARY KEY,
  tenant_id          uuid NOT NULL,
  user_id            text NOT NULL CHECK (char_length(user_id) BETWEEN 1 AND 128),
  absence_type_id    uuid NOT NULL,
  start_date         date NOT NULL,
  end_date           date NOT NULL,
  half_start         boolean NOT NULL DEFAULT false,
  half_end           boolean NOT NULL DEFAULT false,
  days               numeric(6,1) NOT NULL CHECK (days > 0),
  status             text NOT NULL CHECK (status IN ('pending','awaiting_signing','approved','rejected','cancelled','revoked')),
  reason             text NOT NULL DEFAULT '' CHECK (char_length(reason) <= 1000),
  notes              text NOT NULL DEFAULT '' CHECK (char_length(notes) <= 2000),
  approver_ids       text[] NOT NULL DEFAULT '{}',
  reviewed_by        text NOT NULL DEFAULT '',
  reviewed_at        timestamptz,
  review_notes       text NOT NULL DEFAULT '' CHECK (char_length(review_notes) <= 1000),
  submission_id      uuid,
  signing_note       text NOT NULL DEFAULT '' CHECK (signing_note IN ('', 'declined', 'expired', 'cancelled')),
  signing_started_at timestamptz,
  signing_attempt    int NOT NULL DEFAULT 0,
  version            int NOT NULL DEFAULT 1,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         text NOT NULL DEFAULT '',
  updated_at         timestamptz NOT NULL DEFAULT now(),
  CHECK (end_date >= start_date),
  CHECK (end_date - start_date <= 731),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, absence_type_id) REFERENCES hr_absence_types (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT hr_requests_no_overlap EXCLUDE USING gist (
    tenant_id WITH =, user_id WITH =, daterange(start_date, end_date, '[]') WITH &&
  ) WHERE (status IN ('pending','awaiting_signing','approved'))
);
CREATE INDEX hr_requests_person ON hr_requests (tenant_id, user_id, start_date DESC);
CREATE INDEX hr_requests_status ON hr_requests (tenant_id, status, start_date);
CREATE INDEX hr_requests_active ON hr_requests (tenant_id, start_date, end_date) WHERE status IN ('pending','awaiting_signing','approved');
CREATE INDEX hr_requests_approvers ON hr_requests USING gin (approver_ids);
CREATE UNIQUE INDEX hr_requests_submission ON hr_requests (submission_id) WHERE submission_id IS NOT NULL;
CREATE INDEX hr_requests_signing ON hr_requests (signing_started_at) WHERE status = 'awaiting_signing';

CREATE TABLE hr_request_charges (
  request_id   uuid NOT NULL,
  tenant_id    uuid NOT NULL,
  allowance_id uuid NOT NULL,
  year         int NOT NULL,
  days         numeric(6,1) NOT NULL CHECK (days > 0),
  PRIMARY KEY (request_id, allowance_id),
  FOREIGN KEY (tenant_id, request_id) REFERENCES hr_requests (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, allowance_id) REFERENCES hr_allowances (tenant_id, id) ON DELETE RESTRICT
);
CREATE INDEX hr_request_charges_allowance ON hr_request_charges (allowance_id);

CREATE TABLE hr_departments (
  id         uuid PRIMARY KEY,
  tenant_id  uuid NOT NULL,
  parent_id  uuid,
  name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  manager_id text NOT NULL DEFAULT '' CHECK (char_length(manager_id) <= 128),
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by text NOT NULL DEFAULT '',
  CHECK (parent_id IS NULL OR parent_id <> id),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, parent_id) REFERENCES hr_departments (tenant_id, id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX hr_departments_name ON hr_departments (tenant_id, parent_id, lower(name)) NULLS NOT DISTINCT;

CREATE TABLE hr_members (
  tenant_id     uuid NOT NULL,
  user_id       text NOT NULL CHECK (char_length(user_id) BETWEEN 1 AND 128),
  department_id uuid,
  display_name  text NOT NULL DEFAULT '' CHECK (char_length(display_name) <= 200),
  active        boolean NOT NULL DEFAULT true,
  synced_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, user_id),
  FOREIGN KEY (tenant_id, department_id) REFERENCES hr_departments (tenant_id, id) ON DELETE RESTRICT
);
CREATE INDEX hr_members_department ON hr_members (tenant_id, department_id);

CREATE TABLE hr_holidays (
  id         uuid PRIMARY KEY,
  tenant_id  uuid NOT NULL,
  date       date NOT NULL,
  name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  recurring  boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX hr_holidays_date ON hr_holidays (tenant_id, date);

CREATE TABLE hr_signing_outcomes (
  tenant_id     uuid NOT NULL,
  submission_id uuid NOT NULL,
  outcome       text NOT NULL CHECK (outcome IN ('completed','declined','cancelled','expired')),
  source        text NOT NULL CHECK (source IN ('event','reconcile')),
  event_id      text NOT NULL DEFAULT '',
  request_id    uuid,
  result        text NOT NULL CHECK (result IN ('applied','ignored_status','ignored_unknown')),
  received_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, submission_id)
);

CREATE TABLE hr_tenants (
  tenant_id         uuid PRIMARY KEY,
  stream_cursor     text NOT NULL DEFAULT '',
  cursor_at         timestamptz NOT NULL DEFAULT now(),
  members_synced_at timestamptz
);

CREATE TABLE hr_carryover_runs (
  id          uuid PRIMARY KEY,
  tenant_id   uuid NOT NULL,
  source_year int NOT NULL CHECK (source_year BETWEEN 2000 AND 2098),
  created_at  timestamptz NOT NULL DEFAULT now(),
  created_by  text NOT NULL DEFAULT '',
  created     int NOT NULL DEFAULT 0,
  updated     int NOT NULL DEFAULT 0
);

CREATE TABLE hr_mail_outbox (
  id         uuid PRIMARY KEY,
  tenant_id  uuid NOT NULL,
  key        text NOT NULL CHECK (key LIKE 'hr.%'),
  user_id    text NOT NULL,
  vars       jsonb NOT NULL DEFAULT '{}',
  attempts   int NOT NULL DEFAULT 0,
  next_at    timestamptz NOT NULL DEFAULT now(),
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX hr_mail_outbox_due ON hr_mail_outbox (next_at);

-- +goose Down
DROP TABLE IF EXISTS hr_mail_outbox, hr_carryover_runs, hr_tenants, hr_signing_outcomes, hr_holidays, hr_members,
  hr_request_charges, hr_requests, hr_departments, hr_allowances, hr_absence_types, hr_pools;
