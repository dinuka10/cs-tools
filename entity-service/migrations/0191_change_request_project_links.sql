-- Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
--
-- WSO2 LLC. licenses this file to you under the Apache License,
-- Version 2.0 (the "License"); you may not use this file except
-- in compliance with the License.
-- You may obtain a copy of the License at
--
-- http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing,
-- software distributed under the License is distributed on an
-- "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
-- KIND, either express or implied.  See the License for the
-- specific language governing permissions and limitations
-- under the License.

-- The change request form's customer-scope fields: Customer Project,
-- Deployments (multi-select), Environments (multi-select) and Deployment
-- products (read-only, derived). They are populated from the selected project
-- exactly the way the case form does it -- project -> deployments of that
-- project -> the deployed products of the chosen deployments -- and until now
-- had nowhere to be stored:
--
--   * Customer Project is work_item.project_id (already there; the create path
--     simply never wrote it).
--   * Deployments, Environments and Deployment products are many-to-many and
--     get the three join tables below.
--
-- Environments. The case data model has no environment table: a "deployment"
-- IS an environment instance of a project, and its role (Primary production,
-- Staging, QA, ...) is deployment.type (deployment_type_enum). The CR form's
-- "Environments" field is that role, so it is modelled as a small catalogue,
-- one row per deployment_type_enum label (environment.code), and a deployment's
-- environment is the catalogue row whose code is its type. Environments are
-- therefore DERIVED from the chosen deployments: a CR can reference only
-- environments some chosen deployment is an instance of. The catalogue rows
-- have fixed ids so seed data, tests and clients can refer to them.
--
-- Deployment products are deployed_product rows (the product + version
-- installed in a deployment -- what the case form's "Product" picker lists).
-- Which ones a CR carries is not a free choice: they are derived from the
-- chosen deployments (every active deployed product of a chosen deployment),
-- and stored at write time as a snapshot so a CR keeps the products it was
-- raised against even if the deployment is later edited.
--
-- Join rows are removed with the change request (ON DELETE CASCADE) and with
-- the referenced deployment / deployed product / environment (CASCADE too: a
-- link to a record that no longer exists has no meaning, unlike the nullable
-- reference columns elsewhere, which keep their row).
--
-- The three join tables are under FORCE ROW LEVEL SECURITY with the same
-- project-membership rule as work_item_tag / work_item_watcher (migration
-- 0147): internal callers see everything, a member sees the links of change
-- requests of their own project. Update is deliberately not a policy: the
-- repository only ever deletes and re-inserts the whole list.
--
-- change_request_category_enum (migration 0043) lacks the four values the API
-- enum has had since the field-parity work (regular/hotfix release cloud,
-- devops, cloud computing), so choosing one of them could never be stored.
-- They are added here so every category the form offers persists.
--
-- Idempotent: IF NOT EXISTS / ON CONFLICT DO NOTHING / DROP POLICY IF EXISTS
-- throughout, so a re-run is a no-op.

ALTER TYPE change_request_category_enum ADD VALUE IF NOT EXISTS 'REGULAR_RELEASE_CLOUD';
ALTER TYPE change_request_category_enum ADD VALUE IF NOT EXISTS 'HOTFIX_RELEASE_CLOUD';
ALTER TYPE change_request_category_enum ADD VALUE IF NOT EXISTS 'DEVOPS';
ALTER TYPE change_request_category_enum ADD VALUE IF NOT EXISTS 'CLOUD_COMPUTING';

CREATE TABLE IF NOT EXISTS environment (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- deployment_type_enum label this environment stands for.
    code VARCHAR(64) NOT NULL UNIQUE,
    name VARCHAR(100) NOT NULL,
    created_on TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_on TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO environment (id, code, name) VALUES
    ('e0000000-0000-4000-8000-000000000001', 'PRIMARY_PRODUCTION', 'Primary Production'),
    ('e0000000-0000-4000-8000-000000000002', 'STAGING',            'Staging'),
    ('e0000000-0000-4000-8000-000000000003', 'QA',                 'QA'),
    ('e0000000-0000-4000-8000-000000000004', 'STRESS',             'Stress'),
    ('e0000000-0000-4000-8000-000000000005', 'UAT',                'UAT'),
    ('e0000000-0000-4000-8000-000000000006', 'DEVELOPMENT',        'Development')
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS change_request_deployment (
    change_request_id UUID NOT NULL REFERENCES change_request(id) ON DELETE CASCADE,
    deployment_id UUID NOT NULL REFERENCES deployment(id) ON DELETE CASCADE,
    PRIMARY KEY (change_request_id, deployment_id)
);
CREATE INDEX IF NOT EXISTS idx_change_request_deployment_deployment_id
    ON change_request_deployment (deployment_id);

CREATE TABLE IF NOT EXISTS change_request_environment (
    change_request_id UUID NOT NULL REFERENCES change_request(id) ON DELETE CASCADE,
    environment_id UUID NOT NULL REFERENCES environment(id) ON DELETE CASCADE,
    PRIMARY KEY (change_request_id, environment_id)
);
CREATE INDEX IF NOT EXISTS idx_change_request_environment_environment_id
    ON change_request_environment (environment_id);

CREATE TABLE IF NOT EXISTS change_request_deployed_product (
    change_request_id UUID NOT NULL REFERENCES change_request(id) ON DELETE CASCADE,
    deployed_product_id UUID NOT NULL REFERENCES deployed_product(id) ON DELETE CASCADE,
    PRIMARY KEY (change_request_id, deployed_product_id)
);
CREATE INDEX IF NOT EXISTS idx_change_request_deployed_product_deployed_product_id
    ON change_request_deployed_product (deployed_product_id);

-- Row level security: see the header.
ALTER TABLE change_request_deployment ENABLE ROW LEVEL SECURITY;
ALTER TABLE change_request_deployment FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS change_request_deployment_visibility ON change_request_deployment;
CREATE POLICY change_request_deployment_visibility ON change_request_deployment
    FOR SELECT USING (
        (SELECT current_setting('app.is_internal', true) = 'true')
        OR is_project_member((SELECT wi.project_id FROM work_item wi WHERE wi.id = change_request_deployment.change_request_id))
    );
DROP POLICY IF EXISTS change_request_deployment_write ON change_request_deployment;
CREATE POLICY change_request_deployment_write ON change_request_deployment
    FOR INSERT WITH CHECK (
        (SELECT current_setting('app.is_internal', true) = 'true')
        OR is_project_member((SELECT wi.project_id FROM work_item wi WHERE wi.id = change_request_id))
    );
DROP POLICY IF EXISTS change_request_deployment_delete ON change_request_deployment;
CREATE POLICY change_request_deployment_delete ON change_request_deployment
    FOR DELETE USING (
        (SELECT current_setting('app.is_internal', true) = 'true')
        OR is_project_member((SELECT wi.project_id FROM work_item wi WHERE wi.id = change_request_deployment.change_request_id))
    );

ALTER TABLE change_request_environment ENABLE ROW LEVEL SECURITY;
ALTER TABLE change_request_environment FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS change_request_environment_visibility ON change_request_environment;
CREATE POLICY change_request_environment_visibility ON change_request_environment
    FOR SELECT USING (
        (SELECT current_setting('app.is_internal', true) = 'true')
        OR is_project_member((SELECT wi.project_id FROM work_item wi WHERE wi.id = change_request_environment.change_request_id))
    );
DROP POLICY IF EXISTS change_request_environment_write ON change_request_environment;
CREATE POLICY change_request_environment_write ON change_request_environment
    FOR INSERT WITH CHECK (
        (SELECT current_setting('app.is_internal', true) = 'true')
        OR is_project_member((SELECT wi.project_id FROM work_item wi WHERE wi.id = change_request_id))
    );
DROP POLICY IF EXISTS change_request_environment_delete ON change_request_environment;
CREATE POLICY change_request_environment_delete ON change_request_environment
    FOR DELETE USING (
        (SELECT current_setting('app.is_internal', true) = 'true')
        OR is_project_member((SELECT wi.project_id FROM work_item wi WHERE wi.id = change_request_environment.change_request_id))
    );

ALTER TABLE change_request_deployed_product ENABLE ROW LEVEL SECURITY;
ALTER TABLE change_request_deployed_product FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS change_request_deployed_product_visibility ON change_request_deployed_product;
CREATE POLICY change_request_deployed_product_visibility ON change_request_deployed_product
    FOR SELECT USING (
        (SELECT current_setting('app.is_internal', true) = 'true')
        OR is_project_member((SELECT wi.project_id FROM work_item wi WHERE wi.id = change_request_deployed_product.change_request_id))
    );
DROP POLICY IF EXISTS change_request_deployed_product_write ON change_request_deployed_product;
CREATE POLICY change_request_deployed_product_write ON change_request_deployed_product
    FOR INSERT WITH CHECK (
        (SELECT current_setting('app.is_internal', true) = 'true')
        OR is_project_member((SELECT wi.project_id FROM work_item wi WHERE wi.id = change_request_id))
    );
DROP POLICY IF EXISTS change_request_deployed_product_delete ON change_request_deployed_product;
CREATE POLICY change_request_deployed_product_delete ON change_request_deployed_product
    FOR DELETE USING (
        (SELECT current_setting('app.is_internal', true) = 'true')
        OR is_project_member((SELECT wi.project_id FROM work_item wi WHERE wi.id = change_request_deployed_product.change_request_id))
    );
