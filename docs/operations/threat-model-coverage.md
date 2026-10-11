# Threat model coverage

Every row of the threat model in [10-security](../spec/10-security.md#6-threat-model-stride) has tests, or an
accepted risk in the [release notes](release-notes-1.0.md). The test `TestM10b_ThreatModelRowsAreCovered` reads this
table, and fails when a row is missing, a named test does not exist, or an accepted risk is not in the release notes.

| # | Tests | Accepted risks |
|---|---|---|
| 1 | TestAC_NODE_02_JoinTokenSingleUseAndExpiry, TestJoinRefused, TestM1_NodesAndJoinToken | |
| 2 | TestM10b_RevokedCertCannotConnect, TestConnectMTLS, TestREQ_NODE_008_DeleteNodeForce | |
| 3 | TestREQ_SEC_100_NodeGetsOnlyItsOwnSecrets, TestREQ_SEC_106_ApplyIsolation | AR-4 |
| 4 | TestConnectMTLS, TestJoinRefused | |
| 5 | TestAC_APP_03_WebhookSignatures, TestAC_APP_03_AC_AUDIT_03_PushWebhookQueuesDeployment, TestREQ_PREV_001_PullRequestEvents | |
| 6 | TestAC_PREV_01_to_04_AC_PREV_07_AC_VOL_04_Lifecycle, TestAC_PREV_05_ForkRules | AR-5 |
| 7 | TestAC_TPL_02_Privileged, TestAC_TPL_02_UnsafeTemplate, TestComposeUnsafeBind | |
| 8 | TestAC_SEC_06_ComposeBuildNetwork, TestAC_SEC_06_NftablesRules | AR-6 |
| 9 | TestAC_APP_07_ExecProtectedEnvironment, TestAC_APP_07_ExecRelayAuditIdle | |
| 10 | TestAC_AUTH_02_ApiTokenScopes | |
| 11 | TestAC_AUTH_01_03_FirstUserLoginCSRF, TestR9_SecurityHeadersAndBodyLimit | |
| 12 | TestAC_SSO_02_UnverifiedEmailIsNotLinked, TestREQ_SSO_006_StateAndNonce, TestVerifyIDTokenRefusesWeakTokens | |
| 13 | TestAC_SEC_04_RotateKEKAndDEK, TestAC_SEC_05_DumpHasNoPlaintext, TestAC_ENV_02_AC_SEC_01_SecretsAreWriteOnly, TestGate9_SecretLeak | |
| 14 | TestAC_AUDIT_02_T_E2E_14_ChainBreakIsFound, TestM5_EveryStateChangingOperationWritesAnAuditEvent | |
| 15 | TestAC_APP_01_PushOCI, TestAC_APP_02_MovingTagNewDigest, TestR4_ImageServiceCannotUseBuiltInRegistry | |
| 16 | TestBuildOneRoute | AR-3 |
| 17 | TestAC_TPL_02_Privileged | |
| 18 | TestREQ_SEC_104_DenyList, TestREQ_SEC_104_AfterDNS, TestREQ_SEC_104_ClientAndRedirect | |
| 19 | TestREQ_BKP_002_REQ_BKP_004_BackupTargets, TestAC_BKP_01_03_ManualRunTwoTargets | |
| 20 | | AR-7 |
| 21 | TestM0_RateLimit429, TestR7_RateLimitPerIP, TestR9_SecurityHeadersAndBodyLimit, TestM10b_SSEStreamsPerUserLimit | |
| 22 | TestM8_LogSearchIsScopedToTheOrg, TestM8_MetricsAreScopedToTheOrg, TestREQ_SEC_106_RulesetIsolationGolden, TestAC_NODE_09_RemoteEndpointsSameEnvironmentOnly | AR-1 |
| 23 | TestLogQueryEscapesTheText, TestFormat | |
| 24 | TestM10b_RestoreCheckContainerIsIsolated, TestCorruptedSnapshotFailsCheck | |
