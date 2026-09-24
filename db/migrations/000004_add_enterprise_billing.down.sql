-- Down migration: 000004_add_enterprise_billing
DROP TABLE IF EXISTS usage_meters CASCADE;
DROP TABLE IF EXISTS tenant_api_keys CASCADE;
DROP TABLE IF EXISTS tenant_subscriptions CASCADE;
