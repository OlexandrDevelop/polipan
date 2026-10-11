-- SPDX-License-Identifier: Apache-2.0
-- M10 migration: a project role made by an SSO group mapping has source = 'sso' (10-security, section 2).
-- The role of a mapping is replaced on every login. A manual role stays.

ALTER TABLE project_roles ADD COLUMN source text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','sso'));
