# Traceability

Maps every requirement to its file, milestone and tests. The milestone comes from the `Covers` lines of [14-roadmap-and-milestones](14-roadmap-and-milestones.md). A test is an acceptance criterion (`AC-*`, test name contains the id, see [13](13-testing-and-quality.md#1-test-ids)). Requirements with no AC are covered by a unit or component test whose name contains the REQ id.

Rebuild this file when a requirement is added or moved (see [README](README.md#consistency-checks)). Do not edit rows by hand.

Totals: 372 requirements, 141 acceptance criteria.


## CORE

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-CORE-001 | MUST | [00-vision-and-scope.md](00-vision-and-scope.md) | M0 | unit/component test by REQ id |
| REQ-CORE-002 | MUST | [00-vision-and-scope.md](00-vision-and-scope.md) | M0 | unit/component test by REQ id |
| REQ-CORE-003 | MUST | [00-vision-and-scope.md](00-vision-and-scope.md) | M10 | unit/component test by REQ id |
| REQ-CORE-004 | MUST | [02-architecture.md](02-architecture.md) | M0 | unit/component test by REQ id |
| REQ-CORE-005 | MUST | [02-architecture.md](02-architecture.md) | M0 | unit/component test by REQ id |
| REQ-CORE-006 | MUST | [02-architecture.md](02-architecture.md) | M1 | unit/component test by REQ id |
| REQ-CORE-007 | MUST | [02-architecture.md](02-architecture.md) | M1 | unit/component test by REQ id |
| REQ-CORE-008 | MUST | [02-architecture.md](02-architecture.md) | M1 | unit/component test by REQ id |
| REQ-CORE-009 | MUST | [02-architecture.md](02-architecture.md) | M0 | unit/component test by REQ id |
| REQ-CORE-010 | MUST | [02-architecture.md](02-architecture.md) | M0 | unit/component test by REQ id |
| REQ-CORE-011 | MUST | [02-architecture.md](02-architecture.md) | M0 | unit/component test by REQ id |
| REQ-CORE-012 | SHOULD | [02-architecture.md](02-architecture.md) | M0 | unit/component test by REQ id |
| REQ-CORE-020 | MUST | [03-data-model.md](03-data-model.md) | M0 | unit/component test by REQ id |
| REQ-CORE-021 | MUST | [03-data-model.md](03-data-model.md) | M3 | unit/component test by REQ id |
| REQ-CORE-022 | MUST | [03-data-model.md](03-data-model.md) | M3 | unit/component test by REQ id |
| REQ-CORE-023 | MUST | [03-data-model.md](03-data-model.md) | M2 | unit/component test by REQ id |
| REQ-CORE-024 | MUST | [03-data-model.md](03-data-model.md) | M5 | unit/component test by REQ id |
| REQ-CORE-030 | MUST | [13-testing-and-quality.md](13-testing-and-quality.md) | M0 | unit/component test by REQ id |
| REQ-CORE-031 | MUST | [13-testing-and-quality.md](13-testing-and-quality.md) | M0 | unit/component test by REQ id |
| REQ-CORE-032 | MUST | [13-testing-and-quality.md](13-testing-and-quality.md) | M0 | unit/component test by REQ id |
| REQ-CORE-033 | MUST | [13-testing-and-quality.md](13-testing-and-quality.md) | M0 | unit/component test by REQ id |
| REQ-CORE-034 | MUST | [13-testing-and-quality.md](13-testing-and-quality.md) | M0 | unit/component test by REQ id |
| REQ-CORE-035 | SHOULD | [13-testing-and-quality.md](13-testing-and-quality.md) | M0 | unit/component test by REQ id |

## APP

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-APP-001 | MUST | [04-features/C01-apps-git-and-image.md](04-features/C01-apps-git-and-image.md) | M2 | AC-APP-01 |
| REQ-APP-002 | MUST | [04-features/C01-apps-git-and-image.md](04-features/C01-apps-git-and-image.md) | M2 | AC-APP-01 |
| REQ-APP-003 | MUST | [04-features/C01-apps-git-and-image.md](04-features/C01-apps-git-and-image.md) | M1 | AC-APP-02 |
| REQ-APP-004 | MUST | [04-features/C01-apps-git-and-image.md](04-features/C01-apps-git-and-image.md) | M2 | AC-APP-03 |
| REQ-APP-005 | MUST | [04-features/C01-apps-git-and-image.md](04-features/C01-apps-git-and-image.md) | M2 | AC-APP-04 |
| REQ-APP-006 | MUST | [04-features/C01-apps-git-and-image.md](04-features/C01-apps-git-and-image.md) | M1 | unit/component test by REQ id |
| REQ-APP-007 | MUST | [04-features/C01-apps-git-and-image.md](04-features/C01-apps-git-and-image.md) | M2 | AC-APP-06 |
| REQ-APP-008 | MUST | [04-features/C01-apps-git-and-image.md](04-features/C01-apps-git-and-image.md) | M2 | unit/component test by REQ id |
| REQ-APP-009 | MUST | [04-features/C01-apps-git-and-image.md](04-features/C01-apps-git-and-image.md) | M1 | AC-APP-05 |
| REQ-APP-010 | SHOULD | [04-features/C01-apps-git-and-image.md](04-features/C01-apps-git-and-image.md) | M2 | AC-APP-07 |
| REQ-APP-011 | MUST | [04-features/C01-apps-git-and-image.md](04-features/C01-apps-git-and-image.md) | M2 | unit/component test by REQ id |
| REQ-APP-012 | MUST | [04-features/C01-apps-git-and-image.md](04-features/C01-apps-git-and-image.md) | M1 | unit/component test by REQ id |
| REQ-APP-013 | SHOULD | [04-features/C01-apps-git-and-image.md](04-features/C01-apps-git-and-image.md) | M2 | unit/component test by REQ id |

## DB

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-DB-001 | MUST | [04-features/C02-databases.md](04-features/C02-databases.md) | M4 | unit/component test by REQ id |
| REQ-DB-002 | MUST | [04-features/C02-databases.md](04-features/C02-databases.md) | M4 | AC-DB-02 |
| REQ-DB-003 | MUST | [04-features/C02-databases.md](04-features/C02-databases.md) | M4 | AC-DB-01 |
| REQ-DB-004 | MUST | [04-features/C02-databases.md](04-features/C02-databases.md) | M4 | AC-DB-01, AC-DB-04 |
| REQ-DB-005 | MUST | [04-features/C02-databases.md](04-features/C02-databases.md) | M4 | AC-DB-03 |
| REQ-DB-006 | MUST | [04-features/C02-databases.md](04-features/C02-databases.md) | M4 | AC-DB-03 |
| REQ-DB-007 | MUST | [04-features/C02-databases.md](04-features/C02-databases.md) | M4 | unit/component test by REQ id |
| REQ-DB-008 | SHOULD | [04-features/C02-databases.md](04-features/C02-databases.md) | M4 | unit/component test by REQ id |
| REQ-DB-009 | MUST | [04-features/C02-databases.md](04-features/C02-databases.md) | M4 | unit/component test by REQ id |

## TPL

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-TPL-001 | MUST | [04-features/C03-templates.md](04-features/C03-templates.md) | M4 | unit/component test by REQ id |
| REQ-TPL-002 | MUST | [04-features/C03-templates.md](04-features/C03-templates.md) | M4 | AC-TPL-01 |
| REQ-TPL-003 | MUST | [04-features/C03-templates.md](04-features/C03-templates.md) | M4 | AC-TPL-01, AC-TPL-04 |
| REQ-TPL-004 | MUST | [04-features/C03-templates.md](04-features/C03-templates.md) | M4 | unit/component test by REQ id |
| REQ-TPL-005 | MUST | [04-features/C03-templates.md](04-features/C03-templates.md) | M4 | AC-TPL-02 |
| REQ-TPL-006 | MUST | [04-features/C03-templates.md](04-features/C03-templates.md) | M4 | AC-TPL-03 |
| REQ-TPL-007 | SHOULD | [04-features/C03-templates.md](04-features/C03-templates.md) | M4 | unit/component test by REQ id |
| REQ-TPL-008 | MUST | [04-features/C03-templates.md](04-features/C03-templates.md) | M4 | unit/component test by REQ id |

## DOM

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-DOM-001 | MUST | [04-features/C04-domains-https.md](04-features/C04-domains-https.md) | M1 | AC-DOM-01, AC-DOM-02 |
| REQ-DOM-002 | MUST | [04-features/C04-domains-https.md](04-features/C04-domains-https.md) | M1 | AC-DOM-01 |
| REQ-DOM-003 | MUST | [04-features/C04-domains-https.md](04-features/C04-domains-https.md) | M1 | AC-DOM-05 |
| REQ-DOM-004 | MUST | [04-features/C04-domains-https.md](04-features/C04-domains-https.md) | M1 | AC-DOM-02 |
| REQ-DOM-005 | MUST | [04-features/C04-domains-https.md](04-features/C04-domains-https.md) | M1 | AC-DOM-03 |
| REQ-DOM-006 | SHOULD | [04-features/C04-domains-https.md](04-features/C04-domains-https.md) | M2 | unit/component test by REQ id |
| REQ-DOM-007 | MUST | [04-features/C04-domains-https.md](04-features/C04-domains-https.md) | M9 | unit/component test by REQ id |
| REQ-DOM-008 | MUST | [04-features/C04-domains-https.md](04-features/C04-domains-https.md) | M1 | unit/component test by REQ id |
| REQ-DOM-009 | SHOULD | [04-features/C04-domains-https.md](04-features/C04-domains-https.md) | M2 | unit/component test by REQ id |
| REQ-DOM-010 | MUST | [04-features/C04-domains-https.md](04-features/C04-domains-https.md) | M1 | AC-DOM-04 |
| REQ-DOM-011 | MUST | [04-features/C04-domains-https.md](04-features/C04-domains-https.md) | M1 | unit/component test by REQ id |

## ENV

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-ENV-001 | MUST | [04-features/C05-env-vars.md](04-features/C05-env-vars.md) | M2 | AC-ENV-01 |
| REQ-ENV-002 | MUST | [04-features/C05-env-vars.md](04-features/C05-env-vars.md) | M2 | AC-ENV-05 |
| REQ-ENV-003 | MUST | [04-features/C05-env-vars.md](04-features/C05-env-vars.md) | M2 | AC-ENV-03 |
| REQ-ENV-004 | MUST | [04-features/C05-env-vars.md](04-features/C05-env-vars.md) | M2 | unit/component test by REQ id |
| REQ-ENV-005 | MUST | [04-features/C05-env-vars.md](04-features/C05-env-vars.md) | M2 | AC-ENV-04 |
| REQ-ENV-006 | MUST | [04-features/C05-env-vars.md](04-features/C05-env-vars.md) | M2 | unit/component test by REQ id |
| REQ-ENV-007 | MUST | [04-features/C05-env-vars.md](04-features/C05-env-vars.md) | M2 | AC-ENV-02 |
| REQ-ENV-008 | SHOULD | [04-features/C05-env-vars.md](04-features/C05-env-vars.md) | M2 | unit/component test by REQ id |
| REQ-ENV-009 | MUST | [04-features/C05-env-vars.md](04-features/C05-env-vars.md) | M6 | unit/component test by REQ id |
| REQ-ENV-010 | MUST | [04-features/C05-env-vars.md](04-features/C05-env-vars.md) | M2 | unit/component test by REQ id |

## VOL

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-VOL-001 | MUST | [04-features/C06-volumes.md](04-features/C06-volumes.md) | M2 | AC-VOL-01 |
| REQ-VOL-002 | MUST | [04-features/C06-volumes.md](04-features/C06-volumes.md) | M2 | AC-VOL-02 |
| REQ-VOL-003 | MUST | [04-features/C06-volumes.md](04-features/C06-volumes.md) | M2 | unit/component test by REQ id |
| REQ-VOL-004 | MUST | [04-features/C06-volumes.md](04-features/C06-volumes.md) | M2 | AC-VOL-03 |
| REQ-VOL-005 | MUST | [04-features/C06-volumes.md](04-features/C06-volumes.md) | M2 | unit/component test by REQ id |
| REQ-VOL-006 | SHOULD | [04-features/C06-volumes.md](04-features/C06-volumes.md) | M2 | unit/component test by REQ id |
| REQ-VOL-007 | MUST | [04-features/C06-volumes.md](04-features/C06-volumes.md) | M7 | unit/component test by REQ id |
| REQ-VOL-008 | MUST | [04-features/C06-volumes.md](04-features/C06-volumes.md) | M9 | AC-VOL-04 |
| REQ-VOL-009 | SHOULD | [04-features/C06-volumes.md](04-features/C06-volumes.md) | M2 | unit/component test by REQ id |

## DEPLOY

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-DEPLOY-001 | MUST | [04-features/F01-zero-downtime-deploy.md](04-features/F01-zero-downtime-deploy.md) | M3 | AC-DEPLOY-01 |
| REQ-DEPLOY-002 | MUST | [04-features/F01-zero-downtime-deploy.md](04-features/F01-zero-downtime-deploy.md) | M3 | AC-DEPLOY-01, AC-DEPLOY-02 |
| REQ-DEPLOY-003 | MUST | [04-features/F01-zero-downtime-deploy.md](04-features/F01-zero-downtime-deploy.md) | M3 | AC-DEPLOY-02 |
| REQ-DEPLOY-004 | MUST | [04-features/F01-zero-downtime-deploy.md](04-features/F01-zero-downtime-deploy.md) | M3 | AC-DEPLOY-01 |
| REQ-DEPLOY-005 | MUST | [04-features/F01-zero-downtime-deploy.md](04-features/F01-zero-downtime-deploy.md) | M3 | unit/component test by REQ id |
| REQ-DEPLOY-006 | MUST | [04-features/F01-zero-downtime-deploy.md](04-features/F01-zero-downtime-deploy.md) | M8 | AC-DEPLOY-03 |
| REQ-DEPLOY-007 | MUST | [04-features/F01-zero-downtime-deploy.md](04-features/F01-zero-downtime-deploy.md) | M3 | AC-DEPLOY-04 |
| REQ-DEPLOY-008 | MUST | [04-features/F01-zero-downtime-deploy.md](04-features/F01-zero-downtime-deploy.md) | M3 | AC-DEPLOY-04 |
| REQ-DEPLOY-009 | MUST | [04-features/F01-zero-downtime-deploy.md](04-features/F01-zero-downtime-deploy.md) | M3 | AC-DEPLOY-07, AC-DEPLOY-09 |
| REQ-DEPLOY-010 | MUST | [04-features/F01-zero-downtime-deploy.md](04-features/F01-zero-downtime-deploy.md) | M3 | AC-DEPLOY-05 |
| REQ-DEPLOY-011 | SHOULD | [04-features/F01-zero-downtime-deploy.md](04-features/F01-zero-downtime-deploy.md) | M3 | AC-DEPLOY-08 |
| REQ-DEPLOY-012 | MUST | [04-features/F01-zero-downtime-deploy.md](04-features/F01-zero-downtime-deploy.md) | M3 | AC-DEPLOY-06 |
| REQ-DEPLOY-100 | MUST | [08-deploy-engine.md](08-deploy-engine.md) | M3 | unit/component test by REQ id |
| REQ-DEPLOY-101 | MUST | [08-deploy-engine.md](08-deploy-engine.md) | M3 | unit/component test by REQ id |
| REQ-DEPLOY-102 | MUST | [08-deploy-engine.md](08-deploy-engine.md) | M3 | unit/component test by REQ id |
| REQ-DEPLOY-103 | MUST | [08-deploy-engine.md](08-deploy-engine.md) | M3 | unit/component test by REQ id |
| REQ-DEPLOY-104 | MUST | [08-deploy-engine.md](08-deploy-engine.md) | M3 | unit/component test by REQ id |
| REQ-DEPLOY-105 | MUST | [08-deploy-engine.md](08-deploy-engine.md) | M8 | unit/component test by REQ id |
| REQ-DEPLOY-106 | MUST | [08-deploy-engine.md](08-deploy-engine.md) | M3 | unit/component test by REQ id |
| REQ-DEPLOY-107 | MUST | [08-deploy-engine.md](08-deploy-engine.md) | M3 | unit/component test by REQ id |
| REQ-DEPLOY-108 | MUST | [08-deploy-engine.md](08-deploy-engine.md) | M3 | unit/component test by REQ id |
| REQ-DEPLOY-109 | MUST | [08-deploy-engine.md](08-deploy-engine.md) | M3 | unit/component test by REQ id |
| REQ-DEPLOY-110 | SHOULD | [08-deploy-engine.md](08-deploy-engine.md) | M3 | unit/component test by REQ id |
| REQ-DEPLOY-111 | MUST | [08-deploy-engine.md](08-deploy-engine.md) | M3 | unit/component test by REQ id |

## REL

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-REL-001 | MUST | [04-features/F02-rollback-and-releases.md](04-features/F02-rollback-and-releases.md) | M3 | unit/component test by REQ id |
| REQ-REL-002 | MUST | [04-features/F02-rollback-and-releases.md](04-features/F02-rollback-and-releases.md) | M3 | unit/component test by REQ id |
| REQ-REL-003 | MUST | [04-features/F02-rollback-and-releases.md](04-features/F02-rollback-and-releases.md) | M3 | AC-REL-01, AC-REL-04 |
| REQ-REL-004 | MUST | [04-features/F02-rollback-and-releases.md](04-features/F02-rollback-and-releases.md) | M3 | AC-REL-02 |
| REQ-REL-005 | MUST | [04-features/F02-rollback-and-releases.md](04-features/F02-rollback-and-releases.md) | M3 | AC-REL-05 |
| REQ-REL-006 | MUST | [04-features/F02-rollback-and-releases.md](04-features/F02-rollback-and-releases.md) | M3 | AC-REL-03 |
| REQ-REL-007 | MUST | [04-features/F02-rollback-and-releases.md](04-features/F02-rollback-and-releases.md) | M3 | unit/component test by REQ id |
| REQ-REL-008 | MUST | [04-features/F02-rollback-and-releases.md](04-features/F02-rollback-and-releases.md) | M3 | AC-REL-08 |
| REQ-REL-009 | SHOULD | [04-features/F02-rollback-and-releases.md](04-features/F02-rollback-and-releases.md) | M3 | unit/component test by REQ id |
| REQ-REL-010 | MUST | [04-features/F02-rollback-and-releases.md](04-features/F02-rollback-and-releases.md) | M3 | AC-REL-06 |
| REQ-REL-011 | SHOULD | [04-features/F02-rollback-and-releases.md](04-features/F02-rollback-and-releases.md) | M3 | unit/component test by REQ id |
| REQ-REL-012 | MUST | [04-features/F02-rollback-and-releases.md](04-features/F02-rollback-and-releases.md) | M3 | AC-REL-07 |

## PREV

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-PREV-001 | MUST | [04-features/F03-preview-environments.md](04-features/F03-preview-environments.md) | M9 | unit/component test by REQ id |
| REQ-PREV-002 | MUST | [04-features/F03-preview-environments.md](04-features/F03-preview-environments.md) | M9 | AC-PREV-01 |
| REQ-PREV-003 | MUST | [04-features/F03-preview-environments.md](04-features/F03-preview-environments.md) | M9 | AC-PREV-01 |
| REQ-PREV-004 | MUST | [04-features/F03-preview-environments.md](04-features/F03-preview-environments.md) | M9 | AC-PREV-02 |
| REQ-PREV-005 | MUST | [04-features/F03-preview-environments.md](04-features/F03-preview-environments.md) | M9 | AC-PREV-01 |
| REQ-PREV-006 | MUST | [04-features/F03-preview-environments.md](04-features/F03-preview-environments.md) | M9 | AC-PREV-01, AC-PREV-02 |
| REQ-PREV-007 | MUST | [04-features/F03-preview-environments.md](04-features/F03-preview-environments.md) | M9 | AC-PREV-03 |
| REQ-PREV-008 | MUST | [04-features/F03-preview-environments.md](04-features/F03-preview-environments.md) | M9 | AC-PREV-06, AC-PREV-08 |
| REQ-PREV-009 | MUST | [04-features/F03-preview-environments.md](04-features/F03-preview-environments.md) | M9 | AC-PREV-05 |
| REQ-PREV-010 | MUST | [04-features/F03-preview-environments.md](04-features/F03-preview-environments.md) | M9 | AC-PREV-04 |
| REQ-PREV-011 | MUST | [04-features/F03-preview-environments.md](04-features/F03-preview-environments.md) | M9 | AC-PREV-07 |
| REQ-PREV-012 | MUST | [04-features/F03-preview-environments.md](04-features/F03-preview-environments.md) | M9 | unit/component test by REQ id |
| REQ-PREV-013 | SHOULD | [04-features/F03-preview-environments.md](04-features/F03-preview-environments.md) | M9 | unit/component test by REQ id |
| REQ-PREV-014 | MUST | [04-features/F03-preview-environments.md](04-features/F03-preview-environments.md) | M9 | unit/component test by REQ id |

## PROMO

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-PROMO-001 | MUST | [04-features/F04-promotion.md](04-features/F04-promotion.md) | M9 | AC-PROMO-01 |
| REQ-PROMO-002 | MUST | [04-features/F04-promotion.md](04-features/F04-promotion.md) | M9 | AC-PROMO-02 |
| REQ-PROMO-003 | MUST | [04-features/F04-promotion.md](04-features/F04-promotion.md) | M9 | AC-PROMO-03 |
| REQ-PROMO-004 | MUST | [04-features/F04-promotion.md](04-features/F04-promotion.md) | M9 | AC-PROMO-04, AC-PROMO-08 |
| REQ-PROMO-005 | MUST | [04-features/F04-promotion.md](04-features/F04-promotion.md) | M9 | AC-PROMO-05 |
| REQ-PROMO-006 | MUST | [04-features/F04-promotion.md](04-features/F04-promotion.md) | M9 | unit/component test by REQ id |
| REQ-PROMO-007 | MUST | [04-features/F04-promotion.md](04-features/F04-promotion.md) | M9 | AC-PROMO-06 |
| REQ-PROMO-008 | SHOULD | [04-features/F04-promotion.md](04-features/F04-promotion.md) | M9 | AC-PROMO-07 |
| REQ-PROMO-009 | MUST | [04-features/F04-promotion.md](04-features/F04-promotion.md) | M9 | unit/component test by REQ id |
| REQ-PROMO-010 | MUST | [04-features/F04-promotion.md](04-features/F04-promotion.md) | M9 | unit/component test by REQ id |

## HC

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-HC-001 | MUST | [04-features/F05-health-checks.md](04-features/F05-health-checks.md) | M3 | AC-HC-01 |
| REQ-HC-002 | MUST | [04-features/F05-health-checks.md](04-features/F05-health-checks.md) | M3 | unit/component test by REQ id |
| REQ-HC-003 | MUST | [04-features/F05-health-checks.md](04-features/F05-health-checks.md) | M3 | AC-HC-02 |
| REQ-HC-004 | MUST | [04-features/F05-health-checks.md](04-features/F05-health-checks.md) | M3 | AC-HC-06 |
| REQ-HC-005 | MUST | [04-features/F05-health-checks.md](04-features/F05-health-checks.md) | M3 | AC-HC-01 |
| REQ-HC-006 | MUST | [04-features/F05-health-checks.md](04-features/F05-health-checks.md) | M3 | AC-HC-05 |
| REQ-HC-007 | MUST | [04-features/F05-health-checks.md](04-features/F05-health-checks.md) | M3 | AC-HC-03 |
| REQ-HC-008 | MUST | [04-features/F05-health-checks.md](04-features/F05-health-checks.md) | M3 | AC-HC-04 |
| REQ-HC-009 | MUST | [04-features/F05-health-checks.md](04-features/F05-health-checks.md) | M3 | AC-HC-05 |
| REQ-HC-010 | SHOULD | [04-features/F05-health-checks.md](04-features/F05-health-checks.md) | M3 | unit/component test by REQ id |
| REQ-HC-011 | SHOULD | [04-features/F05-health-checks.md](04-features/F05-health-checks.md) | M3 | unit/component test by REQ id |
| REQ-HC-012 | MUST | [04-features/F05-health-checks.md](04-features/F05-health-checks.md) | M3 | unit/component test by REQ id |

## NODE

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-NODE-001 | MUST | [04-features/F06-multi-server-failover.md](04-features/F06-multi-server-failover.md) | M7 | AC-NODE-01 |
| REQ-NODE-002 | MUST | [04-features/F06-multi-server-failover.md](04-features/F06-multi-server-failover.md) | M1 | unit/component test by REQ id |
| REQ-NODE-003 | MUST | [04-features/F06-multi-server-failover.md](04-features/F06-multi-server-failover.md) | M7 | AC-NODE-08 |
| REQ-NODE-004 | MUST | [04-features/F06-multi-server-failover.md](04-features/F06-multi-server-failover.md) | M7 | unit/component test by REQ id |
| REQ-NODE-005 | MUST | [04-features/F06-multi-server-failover.md](04-features/F06-multi-server-failover.md) | M1 | AC-NODE-04 |
| REQ-NODE-006 | MUST | [04-features/F06-multi-server-failover.md](04-features/F06-multi-server-failover.md) | M7 | AC-NODE-03 |
| REQ-NODE-007 | MUST | [04-features/F06-multi-server-failover.md](04-features/F06-multi-server-failover.md) | M7 | AC-NODE-04, AC-NODE-05, AC-NODE-06, AC-NODE-10 |
| REQ-NODE-008 | MUST | [04-features/F06-multi-server-failover.md](04-features/F06-multi-server-failover.md) | M7 | unit/component test by REQ id |
| REQ-NODE-009 | MUST | [04-features/F06-multi-server-failover.md](04-features/F06-multi-server-failover.md) | M7 | AC-NODE-09 |
| REQ-NODE-010 | MUST | [04-features/F06-multi-server-failover.md](04-features/F06-multi-server-failover.md) | M7 | unit/component test by REQ id |
| REQ-NODE-011 | SHOULD | [04-features/F06-multi-server-failover.md](04-features/F06-multi-server-failover.md) | M7 | unit/component test by REQ id |
| REQ-NODE-012 | MUST | [04-features/F06-multi-server-failover.md](04-features/F06-multi-server-failover.md) | M7 | AC-NODE-07 |
| REQ-NODE-100 | MUST | [09-multi-server.md](09-multi-server.md) | M7 | unit/component test by REQ id |
| REQ-NODE-101 | MUST | [09-multi-server.md](09-multi-server.md) | M7 | unit/component test by REQ id |
| REQ-NODE-102 | MUST | [09-multi-server.md](09-multi-server.md) | M7 | unit/component test by REQ id |
| REQ-NODE-103 | MUST | [09-multi-server.md](09-multi-server.md) | M7 | unit/component test by REQ id |

## AGENT

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-AGENT-001 | MUST | [09-multi-server.md](09-multi-server.md) | M1 | unit/component test by REQ id |
| REQ-AGENT-002 | MUST | [09-multi-server.md](09-multi-server.md) | M1 | AC-NODE-02 |
| REQ-AGENT-003 | MUST | [09-multi-server.md](09-multi-server.md) | M1 | unit/component test by REQ id |
| REQ-AGENT-004 | MUST | [09-multi-server.md](09-multi-server.md) | M1 | unit/component test by REQ id |
| REQ-AGENT-005 | MUST | [09-multi-server.md](09-multi-server.md) | M1 | unit/component test by REQ id |
| REQ-AGENT-006 | MUST | [09-multi-server.md](09-multi-server.md) | M2 | unit/component test by REQ id |
| REQ-AGENT-007 | MUST | [09-multi-server.md](09-multi-server.md) | M1 | unit/component test by REQ id |
| REQ-AGENT-008 | MUST | [09-multi-server.md](09-multi-server.md) | M7 | unit/component test by REQ id |
| REQ-AGENT-009 | MUST | [09-multi-server.md](09-multi-server.md) | M1 | unit/component test by REQ id |
| REQ-AGENT-010 | SHOULD | [09-multi-server.md](09-multi-server.md) | M1 | unit/component test by REQ id |

## AUTH

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-AUTH-001 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M1 | AC-AUTH-01 |
| REQ-AUTH-002 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | unit/component test by REQ id |
| REQ-AUTH-003 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M1 | unit/component test by REQ id |
| REQ-AUTH-004 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | AC-AUTH-02 |
| REQ-AUTH-005 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M1 | unit/component test by REQ id |
| REQ-AUTH-006 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M1 | AC-AUTH-03 |
| REQ-AUTH-007 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | unit/component test by REQ id |
| REQ-AUTH-100 | MUST | [10-security.md](10-security.md) | M1 | unit/component test by REQ id |
| REQ-AUTH-101 | MUST | [10-security.md](10-security.md) | M5 | unit/component test by REQ id |

## RBAC

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-RBAC-001 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | unit/component test by REQ id |
| REQ-RBAC-002 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | AC-RBAC-01 |
| REQ-RBAC-003 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | AC-RBAC-03 |
| REQ-RBAC-004 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | AC-RBAC-02 |
| REQ-RBAC-005 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | unit/component test by REQ id |
| REQ-RBAC-006 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | unit/component test by REQ id |
| REQ-RBAC-007 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | AC-RBAC-04 |
| REQ-RBAC-008 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | unit/component test by REQ id |
| REQ-RBAC-100 | MUST | [10-security.md](10-security.md) | M5 | unit/component test by REQ id |
| REQ-RBAC-101 | MUST | [10-security.md](10-security.md) | M5 | unit/component test by REQ id |

## AUDIT

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-AUDIT-001 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | AC-AUDIT-01 |
| REQ-AUDIT-002 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | AC-AUDIT-01 |
| REQ-AUDIT-003 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | AC-AUDIT-02 |
| REQ-AUDIT-004 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | unit/component test by REQ id |
| REQ-AUDIT-005 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | unit/component test by REQ id |
| REQ-AUDIT-006 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | unit/component test by REQ id |
| REQ-AUDIT-007 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M5 | AC-AUDIT-03 |
| REQ-AUDIT-100 | MUST | [10-security.md](10-security.md) | M5 | unit/component test by REQ id |

## SSO

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-SSO-001 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M10 | AC-SSO-01 |
| REQ-SSO-002 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M10 | AC-SSO-01 |
| REQ-SSO-003 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M10 | AC-SSO-02 |
| REQ-SSO-004 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M10 | AC-SSO-03 |
| REQ-SSO-005 | SHOULD | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M10 | unit/component test by REQ id |
| REQ-SSO-006 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M10 | unit/component test by REQ id |
| REQ-SSO-100 | MUST | [10-security.md](10-security.md) | M10 | unit/component test by REQ id |

## SEC

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-SEC-001 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M2 | AC-SEC-05 |
| REQ-SEC-002 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M2 | AC-SEC-01 |
| REQ-SEC-003 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M2 | AC-SEC-02 |
| REQ-SEC-004 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M2 | unit/component test by REQ id |
| REQ-SEC-005 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M2 | unit/component test by REQ id |
| REQ-SEC-006 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M2 | unit/component test by REQ id |
| REQ-SEC-007 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M2 | AC-SEC-03 |
| REQ-SEC-008 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M2 | AC-SEC-04 |
| REQ-SEC-009 | MUST | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M2 | AC-SEC-05 |
| REQ-SEC-010 | SHOULD | [04-features/F07-teams-rbac-audit-sso-secrets.md](04-features/F07-teams-rbac-audit-sso-secrets.md) | M2 | unit/component test by REQ id |
| REQ-SEC-100 | MUST | [10-security.md](10-security.md) | M7 | unit/component test by REQ id |
| REQ-SEC-101 | MUST | [10-security.md](10-security.md) | M2 | unit/component test by REQ id |
| REQ-SEC-102 | MUST | [10-security.md](10-security.md) | M2 | AC-SEC-06 |
| REQ-SEC-103 | MUST | [10-security.md](10-security.md) | M2 | unit/component test by REQ id |
| REQ-SEC-104 | MUST | [10-security.md](10-security.md) | M2 | unit/component test by REQ id |
| REQ-SEC-105 | MUST | [10-security.md](10-security.md) | M2 | unit/component test by REQ id |
| REQ-SEC-106 | MUST | [10-security.md](10-security.md) | M7 | unit/component test by REQ id |
| REQ-SEC-107 | MUST | [10-security.md](10-security.md) | M2 | unit/component test by REQ id |
| REQ-SEC-108 | MUST | [10-security.md](10-security.md) | M2 | AC-SEC-06 |

## CAC

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-CAC-001 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | unit/component test by REQ id |
| REQ-CAC-002 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | unit/component test by REQ id |
| REQ-CAC-003 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | AC-CAC-02 |
| REQ-CAC-004 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | AC-CAC-01 |
| REQ-CAC-005 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | AC-CAC-03 |
| REQ-CAC-006 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | AC-CAC-04 |
| REQ-CAC-007 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | AC-CAC-05 |
| REQ-CAC-008 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | unit/component test by REQ id |
| REQ-CAC-009 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | AC-CAC-06 |
| REQ-CAC-010 | SHOULD | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | unit/component test by REQ id |
| REQ-CAC-011 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | unit/component test by REQ id |
| REQ-CAC-012 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | AC-CAC-08 |
| REQ-CAC-013 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | AC-CAC-07 |
| REQ-CAC-014 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | AC-CAC-07 |
| REQ-CAC-100 | MUST | [07-config-as-code.md](07-config-as-code.md) | M6 | unit/component test by REQ id |
| REQ-CAC-101 | MUST | [07-config-as-code.md](07-config-as-code.md) | M6 | unit/component test by REQ id |
| REQ-CAC-102 | MUST | [07-config-as-code.md](07-config-as-code.md) | M6 | unit/component test by REQ id |
| REQ-CAC-103 | MUST | [07-config-as-code.md](07-config-as-code.md) | M6 | unit/component test by REQ id |
| REQ-CAC-104 | MUST | [07-config-as-code.md](07-config-as-code.md) | M6 | unit/component test by REQ id |

## API

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-API-001 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M0 | unit/component test by REQ id |
| REQ-API-002 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M0 | AC-API-01 |
| REQ-API-003 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M10 | AC-API-03 |
| REQ-API-004 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M0 | unit/component test by REQ id |
| REQ-API-005 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M3 | AC-API-02 |
| REQ-API-006 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M3 | unit/component test by REQ id |
| REQ-API-007 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M0 | unit/component test by REQ id |
| REQ-API-008 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M0 | unit/component test by REQ id |
| REQ-API-009 | SHOULD | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M8 | unit/component test by REQ id |
| REQ-API-100 | MUST | [05-api.md](05-api.md) | M0 | unit/component test by REQ id |
| REQ-API-101 | MUST | [05-api.md](05-api.md) | M0 | unit/component test by REQ id |
| REQ-API-102 | MUST | [05-api.md](05-api.md) | M2 | unit/component test by REQ id |
| REQ-API-103 | MUST | [05-api.md](05-api.md) | M3 | unit/component test by REQ id |
| REQ-API-104 | MUST | [05-api.md](05-api.md) | M0 | unit/component test by REQ id |
| REQ-API-105 | MUST | [05-api.md](05-api.md) | M0 | unit/component test by REQ id |

## CLI

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-CLI-001 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | unit/component test by REQ id |
| REQ-CLI-002 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | AC-CLI-02 |
| REQ-CLI-003 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | AC-CLI-01 |
| REQ-CLI-004 | SHOULD | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | unit/component test by REQ id |
| REQ-CLI-100 | MUST | [06-cli.md](06-cli.md) | M6 | unit/component test by REQ id |
| REQ-CLI-101 | MUST | [06-cli.md](06-cli.md) | M6 | unit/component test by REQ id |
| REQ-CLI-102 | MUST | [06-cli.md](06-cli.md) | M6 | unit/component test by REQ id |
| REQ-CLI-103 | MUST | [06-cli.md](06-cli.md) | M6 | unit/component test by REQ id |
| REQ-CLI-104 | SHOULD | [06-cli.md](06-cli.md) | M6 | unit/component test by REQ id |

## TF

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-TF-001 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | AC-TF-01 |
| REQ-TF-002 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | AC-TF-01 |
| REQ-TF-003 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | AC-TF-02 |
| REQ-TF-004 | MUST | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | unit/component test by REQ id |
| REQ-TF-005 | SHOULD | [04-features/F08-config-as-code-api-cli-terraform.md](04-features/F08-config-as-code-api-cli-terraform.md) | M6 | unit/component test by REQ id |

## ALERT

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-ALERT-001 | MUST | [04-features/F09-alerts-metrics-logs.md](04-features/F09-alerts-metrics-logs.md) | M8 | AC-ALERT-01 |
| REQ-ALERT-002 | MUST | [04-features/F09-alerts-metrics-logs.md](04-features/F09-alerts-metrics-logs.md) | M8 | unit/component test by REQ id |
| REQ-ALERT-003 | MUST | [04-features/F09-alerts-metrics-logs.md](04-features/F09-alerts-metrics-logs.md) | M8 | unit/component test by REQ id |
| REQ-ALERT-004 | MUST | [04-features/F09-alerts-metrics-logs.md](04-features/F09-alerts-metrics-logs.md) | M8 | AC-ALERT-01, AC-ALERT-02, AC-ALERT-03 |
| REQ-ALERT-005 | MUST | [04-features/F09-alerts-metrics-logs.md](04-features/F09-alerts-metrics-logs.md) | M8 | unit/component test by REQ id |
| REQ-ALERT-006 | MUST | [04-features/F09-alerts-metrics-logs.md](04-features/F09-alerts-metrics-logs.md) | M8 | unit/component test by REQ id |
| REQ-ALERT-007 | MUST | [04-features/F09-alerts-metrics-logs.md](04-features/F09-alerts-metrics-logs.md) | M8 | AC-ALERT-04 |
| REQ-ALERT-008 | MUST | [04-features/F09-alerts-metrics-logs.md](04-features/F09-alerts-metrics-logs.md) | M8 | AC-ALERT-05 |
| REQ-ALERT-009 | SHOULD | [04-features/F09-alerts-metrics-logs.md](04-features/F09-alerts-metrics-logs.md) | M8 | unit/component test by REQ id |

## OBS

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-OBS-001 | MUST | [04-features/C07-logs-metrics-basic.md](04-features/C07-logs-metrics-basic.md) | M8 | unit/component test by REQ id |
| REQ-OBS-002 | MUST | [04-features/C07-logs-metrics-basic.md](04-features/C07-logs-metrics-basic.md) | M8 | AC-OBS-01, AC-OBS-02 |
| REQ-OBS-003 | MUST | [04-features/C07-logs-metrics-basic.md](04-features/C07-logs-metrics-basic.md) | M8 | AC-OBS-03 |
| REQ-OBS-004 | MUST | [04-features/C07-logs-metrics-basic.md](04-features/C07-logs-metrics-basic.md) | M8 | unit/component test by REQ id |
| REQ-OBS-005 | MUST | [04-features/C07-logs-metrics-basic.md](04-features/C07-logs-metrics-basic.md) | M8 | AC-OBS-03 |
| REQ-OBS-006 | MUST | [04-features/C07-logs-metrics-basic.md](04-features/C07-logs-metrics-basic.md) | M8 | AC-OBS-04 |
| REQ-OBS-007 | MUST | [04-features/C07-logs-metrics-basic.md](04-features/C07-logs-metrics-basic.md) | M8 | unit/component test by REQ id |
| REQ-OBS-010 | MUST | [04-features/F09-alerts-metrics-logs.md](04-features/F09-alerts-metrics-logs.md) | M8 | AC-OBS-05 |
| REQ-OBS-011 | MUST | [04-features/F09-alerts-metrics-logs.md](04-features/F09-alerts-metrics-logs.md) | M8 | AC-OBS-05 |
| REQ-OBS-012 | MUST | [04-features/F09-alerts-metrics-logs.md](04-features/F09-alerts-metrics-logs.md) | M8 | AC-OBS-07 |
| REQ-OBS-013 | MUST | [04-features/F09-alerts-metrics-logs.md](04-features/F09-alerts-metrics-logs.md) | M8 | AC-OBS-06 |
| REQ-OBS-014 | SHOULD | [04-features/F09-alerts-metrics-logs.md](04-features/F09-alerts-metrics-logs.md) | M8 | unit/component test by REQ id |
| REQ-OBS-015 | SHOULD | [04-features/F09-alerts-metrics-logs.md](04-features/F09-alerts-metrics-logs.md) | M8 | unit/component test by REQ id |
| REQ-OBS-100 | MUST | [12-observability.md](12-observability.md) | M8 | unit/component test by REQ id |
| REQ-OBS-101 | MUST | [12-observability.md](12-observability.md) | M8 | unit/component test by REQ id |
| REQ-OBS-102 | MUST | [12-observability.md](12-observability.md) | M8 | unit/component test by REQ id |
| REQ-OBS-103 | MUST | [12-observability.md](12-observability.md) | M8 | unit/component test by REQ id |
| REQ-OBS-104 | MUST | [12-observability.md](12-observability.md) | M8 | unit/component test by REQ id |
| REQ-OBS-105 | MUST | [12-observability.md](12-observability.md) | M8 | unit/component test by REQ id |
| REQ-OBS-106 | SHOULD | [12-observability.md](12-observability.md) | M8 | unit/component test by REQ id |
| REQ-OBS-107 | MUST | [12-observability.md](12-observability.md) | M8 | unit/component test by REQ id |
| REQ-OBS-108 | MUST | [12-observability.md](12-observability.md) | M8 | unit/component test by REQ id |
| REQ-OBS-109 | MUST | [12-observability.md](12-observability.md) | M8 | unit/component test by REQ id |
| REQ-OBS-110 | MUST | [12-observability.md](12-observability.md) | M8 | unit/component test by REQ id |

## BKP

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-BKP-001 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | AC-BKP-01 |
| REQ-BKP-002 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | AC-BKP-01, AC-BKP-02 |
| REQ-BKP-003 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | unit/component test by REQ id |
| REQ-BKP-004 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | unit/component test by REQ id |
| REQ-BKP-005 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | AC-BKP-05 |
| REQ-BKP-006 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | AC-BKP-03 |
| REQ-BKP-007 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | AC-BKP-04 |
| REQ-BKP-008 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | AC-BKP-03 |
| REQ-BKP-009 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | AC-BKP-02 |
| REQ-BKP-010 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | AC-BKP-06 |
| REQ-BKP-011 | SHOULD | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | unit/component test by REQ id |
| REQ-BKP-100 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | AC-BKP-07 |
| REQ-BKP-101 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | AC-BKP-07, AC-BKP-09 |
| REQ-BKP-102 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | AC-BKP-09 |
| REQ-BKP-103 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | AC-BKP-08, AC-BKP-10 |
| REQ-BKP-104 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | AC-BKP-07 |
| REQ-BKP-105 | SHOULD | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | AC-BKP-10 |
| REQ-BKP-106 | MUST | [04-features/F10-backups-and-restore-checks.md](04-features/F10-backups-and-restore-checks.md) | M4 | unit/component test by REQ id |

## OSS

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-OSS-001 | MUST | [04-features/F11-open-source.md](04-features/F11-open-source.md) | M0 | AC-OSS-01 |
| REQ-OSS-002 | MUST | [04-features/F11-open-source.md](04-features/F11-open-source.md) | M0 | AC-OSS-02 |
| REQ-OSS-003 | MUST | [04-features/F11-open-source.md](04-features/F11-open-source.md) | M0 | AC-OSS-06 |
| REQ-OSS-004 | MUST | [04-features/F11-open-source.md](04-features/F11-open-source.md) | M10 | AC-OSS-03 |
| REQ-OSS-005 | MUST | [04-features/F11-open-source.md](04-features/F11-open-source.md) | M10 | AC-OSS-04 |
| REQ-OSS-006 | MUST | [04-features/F11-open-source.md](04-features/F11-open-source.md) | M10 | AC-OSS-05 |
| REQ-OSS-007 | SHOULD | [04-features/F11-open-source.md](04-features/F11-open-source.md) | M10 | unit/component test by REQ id |
| REQ-OSS-008 | MUST | [04-features/F11-open-source.md](04-features/F11-open-source.md) | M0 | unit/component test by REQ id |
| REQ-OSS-009 | SHOULD | [04-features/F11-open-source.md](04-features/F11-open-source.md) | M0 | unit/component test by REQ id |

## UI

| REQ | Level | Defined in | Milestone | Tests |
|---|---|---|---|---|
| REQ-UI-001 | MUST | [11-ui.md](11-ui.md) | M10 | unit/component test by REQ id |
| REQ-UI-002 | MUST | [11-ui.md](11-ui.md) | M3 | unit/component test by REQ id |
| REQ-UI-003 | MUST | [11-ui.md](11-ui.md) | M3 | unit/component test by REQ id |
| REQ-UI-004 | MUST | [11-ui.md](11-ui.md) | M5 | unit/component test by REQ id |
| REQ-UI-005 | MUST | [11-ui.md](11-ui.md) | M2 | unit/component test by REQ id |
| REQ-UI-006 | MUST | [11-ui.md](11-ui.md) | M6 | unit/component test by REQ id |
| REQ-UI-007 | MUST | [11-ui.md](11-ui.md) | M3 | unit/component test by REQ id |
| REQ-UI-008 | SHOULD | [11-ui.md](11-ui.md) | M10 | unit/component test by REQ id |
| REQ-UI-009 | SHOULD | [11-ui.md](11-ui.md) | M10 | unit/component test by REQ id |
| REQ-UI-010 | MUST | [11-ui.md](11-ui.md) | M3 | unit/component test by REQ id |
| REQ-UI-011 | MUST | [11-ui.md](11-ui.md) | M10 | unit/component test by REQ id |
| REQ-UI-012 | MUST | [11-ui.md](11-ui.md) | M10 | unit/component test by REQ id |
| REQ-UI-013 | SHOULD | [11-ui.md](11-ui.md) | M3 | unit/component test by REQ id |
| REQ-UI-014 | MUST | [ui/design-system.md](ui/design-system.md) | M0 | unit/component/e2e test by REQ id |
| REQ-UI-015 | MUST | [ui/design-system.md](ui/design-system.md) | M0 | unit/component/e2e test by REQ id |
| REQ-UI-016 | MUST | [ui/responsive.md](ui/responsive.md) | M0 | unit/component/e2e test by REQ id |
| REQ-UI-017 | MUST | [ui/responsive.md](ui/responsive.md) | M0 | unit/component/e2e test by REQ id |
| REQ-UI-018 | MUST | [ui/responsive.md](ui/responsive.md) | M0 | unit/component/e2e test by REQ id |
| REQ-UI-019 | MUST | [ui/navigation.md](ui/navigation.md) | M2 | unit/component/e2e test by REQ id |
| REQ-UI-020 | MUST | [ui/navigation.md](ui/navigation.md) | M2 | unit/component/e2e test by REQ id |
| REQ-UI-021 | SHOULD | [ui/navigation.md](ui/navigation.md) | M10 | unit/component/e2e test by REQ id |
| REQ-UI-022 | MUST | [ui/screens.md](ui/screens.md) | M3 | unit/component/e2e test by REQ id |
| REQ-UI-023 | MUST | [ui/screens.md](ui/screens.md) | M3 | unit/component/e2e test by REQ id |
| REQ-UI-024 | MUST | [ui/parity.md](ui/parity.md) | M10 | unit/component/e2e test by REQ id |
| REQ-UI-025 | MUST | [ui/screens.md](ui/screens.md) | M3 | unit/component/e2e test by REQ id |
| REQ-UI-026 | MUST | [ui/components.md](ui/components.md) | M8 | unit/component/e2e test by REQ id |
| REQ-UI-027 | MUST | [ui/quality.md](ui/quality.md) | M10 | unit/component/e2e test by REQ id |
| REQ-UI-028 | SHOULD | [ui/quality.md](ui/quality.md) | M0 | unit/component/e2e test by REQ id |
| REQ-UI-029 | MUST | [ui/quality.md](ui/quality.md) | M10 | unit/component/e2e test by REQ id |
| REQ-UI-030 | MUST | [ui/quality.md](ui/quality.md) | M10 | unit/component/e2e test by REQ id |
| REQ-UI-031 | MUST | [ui/components.md](ui/components.md) | M2 | unit/component/e2e test by REQ id |
| REQ-UI-032 | MUST | [ui/components.md](ui/components.md) | M3 | unit/component/e2e test by REQ id |
| REQ-UI-033 | SHOULD | [ui/screens.md](ui/screens.md) | M8 | unit/component/e2e test by REQ id |
| REQ-UI-034 | MUST | [ui/terminal-style.md](ui/terminal-style.md#requirements) | M10 | unit/component/e2e test by REQ id |
| REQ-UI-035 | MUST | [ui/terminal-style.md](ui/terminal-style.md#requirements) | M10 | unit/component/e2e test by REQ id |
| REQ-UI-036 | SHOULD | [ui/terminal-style.md](ui/terminal-style.md#requirements) | M10 | unit/component/e2e test by REQ id |
| REQ-UI-037 | MUST | [ui/terminal-style.md](ui/terminal-style.md#requirements) | M10 | unit/component/e2e test by REQ id |
| REQ-UI-038 | MUST | [ui/terminal-style.md](ui/terminal-style.md#requirements) | M10 | unit/component/e2e test by REQ id |
| REQ-UI-039 | MUST | [ui/terminal-style.md](ui/terminal-style.md#requirements) | M10 | unit/component/e2e test by REQ id |
| REQ-UI-040 | SHOULD | [ui/terminal-style.md](ui/terminal-style.md#requirements) | M10 | unit/component/e2e test by REQ id |
| REQ-UI-041 | SHOULD | [ui/terminal-style.md](ui/terminal-style.md#requirements) | M10 | unit/component/e2e test by REQ id |
| REQ-UI-042 | MUST | [ui/terminal-style.md](ui/terminal-style.md#requirements) | M10 | unit/component/e2e test by REQ id |
| REQ-UI-043 | MUST | [ui/terminal-style.md](ui/terminal-style.md#requirements) | M10 | unit/component/e2e test by REQ id |
