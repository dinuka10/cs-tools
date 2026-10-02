-- Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com). All Rights Reserved.
--
-- This software is the property of WSO2 LLC. and its suppliers, if any.
-- Dissemination of any information or reproduction of any material contained
-- herein in any form is strictly forbidden, unless permitted by WSO2 expressly.
-- You may not alter or remove any copyright or other notice from copies of this content.

-- Maps a product name to the GitHub repository an internal issue is filed in.
-- One row per product. Many products may share one repository.
-- No foreign key to product: staging cases are read from ServiceNow, while
-- this table stays in Postgres.

CREATE TABLE IF NOT EXISTS product_repo_mapping (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_on    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_on    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by    VARCHAR(255) NOT NULL,
    updated_by    VARCHAR(255) NOT NULL,
    product_name  VARCHAR(255) NOT NULL,
    abbreviation  VARCHAR(64),
    owner         VARCHAR(255) NOT NULL,
    repository    VARCHAR(255) NOT NULL,
    github_label  VARCHAR(255) NOT NULL,
    is_active     BOOLEAN NOT NULL DEFAULT TRUE,
    CONSTRAINT uq_product_repo_mapping_product_name UNIQUE (product_name),
    CONSTRAINT uq_product_repo_mapping_abbreviation UNIQUE (abbreviation)
);

-- Case-insensitive uniqueness so lookups by name/abbreviation are unambiguous.
CREATE UNIQUE INDEX IF NOT EXISTS uq_product_repo_mapping_product_name_ci
    ON product_repo_mapping (LOWER(product_name));

CREATE UNIQUE INDEX IF NOT EXISTS uq_product_repo_mapping_abbreviation_ci
    ON product_repo_mapping (LOWER(abbreviation));

CREATE INDEX IF NOT EXISTS idx_product_repo_mapping_active
    ON product_repo_mapping (is_active);
