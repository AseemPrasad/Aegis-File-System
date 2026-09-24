-- Up migration: 000004_add_enterprise_billing
CREATE TABLE tenant_subscriptions (
    subscription_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
    plan_tier       VARCHAR(32) NOT NULL CHECK (plan_tier IN ('FREE', 'PRO', 'ENTERPRISE')),
    stripe_customer_id VARCHAR(255),
    stripe_subscription_id VARCHAR(255),
    status          VARCHAR(32) NOT NULL DEFAULT 'ACTIVE',
    current_period_start TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    current_period_end   TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '30 days'),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_tenant_subscription UNIQUE (tenant_id)
);

CREATE TABLE tenant_api_keys (
    key_id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
    key_prefix      VARCHAR(16) NOT NULL,
    key_hash        VARCHAR(64) NOT NULL UNIQUE,
    name            VARCHAR(128) NOT NULL,
    scopes          VARCHAR(255) NOT NULL DEFAULT 'read,write',
    expires_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE usage_meters (
    meter_id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(tenant_id) ON DELETE CASCADE,
    meter_type      VARCHAR(64) NOT NULL CHECK (meter_type IN ('STORAGE_BYTES', 'BANDWIDTH_BYTES', 'WORKER_CREDITS')),
    quantity        BIGINT NOT NULL DEFAULT 0,
    period_timestamp TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
