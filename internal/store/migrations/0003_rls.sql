-- +goose Up
-- Per-tenant row-level security on every hr table. hr_app is NOBYPASSRLS
-- (created by the stack's init-db, not here); every statement runs with
-- app.tenant_id set to the caller's tenant. The event consumer, the scheduled
-- task types, the mail outbox worker and the audit writer set app.system='on'
-- (with app.tenant_id pinned to the nil uuid so the cast stays valid).
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['hr_pools','hr_absence_types','hr_allowances','hr_requests','hr_request_charges',
    'hr_departments','hr_members','hr_holidays','hr_signing_outcomes','hr_tenants','hr_carryover_runs',
    'hr_mail_outbox','hr_audit_events']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on') WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on')$p$, t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO hr_app', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['hr_pools','hr_absence_types','hr_allowances','hr_requests','hr_request_charges',
    'hr_departments','hr_members','hr_holidays','hr_signing_outcomes','hr_tenants','hr_carryover_runs',
    'hr_mail_outbox','hr_audit_events']
  LOOP
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', t);
  END LOOP;
END $$;
-- +goose StatementEnd
